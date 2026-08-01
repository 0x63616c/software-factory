package codexresponses

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
)

const maxSSEEventBytes = 4 << 20

// Client calls the unsupported subscription-backed Codex Responses endpoint.
type Client struct {
	httpClient *http.Client
	endpoint   string
	auth       CredentialSource
	log        *slog.Logger
}

// New constructs a direct Codex Responses client.
func New(httpClient *http.Client, endpoint string, auth CredentialSource, logger *slog.Logger) (*Client, error) {
	switch {
	case httpClient == nil:
		return nil, fmt.Errorf("a Codex Responses client needs an HTTP client")
	case httpClient.Timeout <= 0:
		return nil, fmt.Errorf("a Codex Responses client needs a bounded HTTP timeout")
	case endpoint == "":
		return nil, fmt.Errorf("a Codex Responses client needs an endpoint")
	case auth == nil:
		return nil, fmt.Errorf("a Codex Responses client needs a credential source")
	case logger == nil:
		return nil, fmt.Errorf("a Codex Responses client needs a logger")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parsing the Codex Responses endpoint: %w", err)
	}
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || !loopback(parsed.Host)) {
		return nil, fmt.Errorf("the Codex Responses endpoint must be HTTPS, or loopback HTTP for tests")
	}
	return &Client{httpClient: httpClient, endpoint: endpoint, auth: auth, log: logger}, nil
}

func loopback(host string) bool {
	name, _, err := net.SplitHostPort(host)
	if err != nil {
		name = host
	}
	if name == "localhost" {
		return true
	}
	ip := net.ParseIP(name)
	return ip != nil && ip.IsLoopback()
}

// Turn runs one streamed model turn and returns only its durable result.
func (c *Client) Turn(ctx context.Context, request TurnRequest, emit EmitFunc) (TurnResult, error) {
	credential, err := c.auth.Credential(ctx)
	if err != nil {
		return TurnResult{}, fmt.Errorf("loading the Codex Responses credential: %w", err)
	}
	if credential.AccessToken.Reveal() == "" || credential.AccountID == "" {
		return TurnResult{}, fmt.Errorf("the Codex Responses credential is incomplete")
	}

	body, err := encodeRequest(request)
	if err != nil {
		return TurnResult{}, fmt.Errorf("encoding the Codex Responses request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return TurnResult{}, fmt.Errorf("building the Codex Responses request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+credential.AccessToken.Reveal())
	req.Header.Set("chatgpt-account-id", credential.AccountID)
	req.Header.Set("originator", "software-factory")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	if request.PromptCacheKey != "" {
		req.Header.Set("session-id", request.PromptCacheKey)
	}
	requestID := request.IdempotencyKey
	if requestID == "" {
		requestID = request.PromptCacheKey
	}
	if requestID != "" {
		req.Header.Set("x-client-request-id", requestID)
	}
	if request.IdempotencyKey != "" {
		req.Header.Set("Idempotency-Key", request.IdempotencyKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return TurnResult{}, fmt.Errorf("calling the Codex Responses endpoint: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		metadata := safeProviderErrorMetadata(body)
		err := fmt.Errorf("the Codex Responses endpoint answered HTTP %d%s", resp.StatusCode, metadata)
		if resp.StatusCode == http.StatusTooManyRequests {
			return TurnResult{}, fmt.Errorf("%w: %w", ErrRateLimited, err)
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return TurnResult{}, fmt.Errorf("%w: %w", ErrAuth, err)
		}
		return TurnResult{}, fmt.Errorf("responses request failed: %w", err)
	}

	result, err := parseStream(resp.Body, emit)
	if err != nil {
		return TurnResult{}, fmt.Errorf("reading the Codex Responses stream: %w", err)
	}
	c.log.DebugContext(ctx, "Codex Responses turn completed",
		"response_id", result.ResponseID,
		"outcome", result.Outcome,
		"input_tokens", result.Usage.InputTokens,
		"output_tokens", result.Usage.OutputTokens,
	)
	return result, nil
}

func safeProviderErrorMetadata(body []byte) string {
	var response struct {
		Code  string `json:"code"`
		Param string `json:"param"`
		Type  string `json:"type"`
		Error struct {
			Code    string `json:"code"`
			Param   string `json:"param"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return ""
	}
	code := firstNonEmpty(response.Error.Code, response.Code)
	param := firstNonEmpty(response.Error.Param, response.Param)
	if param == "" {
		param = invalidTypeField(response.Error.Message)
	}
	errorType := firstNonEmpty(response.Error.Type, response.Type)
	parts := make([]string, 0, 3)
	for _, field := range []struct{ key, value string }{
		{key: "code", value: code},
		{key: "param", value: param},
		{key: "type", value: errorType},
	} {
		if safeProviderLabel(field.value) {
			parts = append(parts, field.key+"="+field.value)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, " ") + ")"
}

func invalidTypeField(message string) string {
	const prefix = "Invalid type for '"
	start := strings.Index(message, prefix)
	if start < 0 {
		return ""
	}
	remainder := message[start+len(prefix):]
	end := strings.IndexByte(remainder, '\'')
	if end < 1 {
		return ""
	}
	field := remainder[:end]
	if len(field) > 128 {
		return ""
	}
	for _, character := range field {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && !strings.ContainsRune("_.-[]", character) {
			return ""
		}
	}
	return field
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func safeProviderLabel(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && !strings.ContainsRune("_.-/[]", character) {
			return false
		}
	}
	return true
}

type wireRequest struct {
	Model              string            `json:"model"`
	Store              bool              `json:"store"`
	Stream             bool              `json:"stream"`
	Instructions       string            `json:"instructions"`
	Input              []wireInputItem   `json:"input"`
	Tools              []wireTool        `json:"tools,omitempty"`
	ToolChoice         ToolChoice        `json:"tool_choice"`
	ParallelToolCalls  bool              `json:"parallel_tool_calls"`
	Reasoning          *ReasoningOptions `json:"reasoning,omitempty"`
	Text               wireText          `json:"text"`
	PromptCacheKey     string            `json:"prompt_cache_key,omitempty"`
	PreviousResponseID string            `json:"previous_response_id,omitempty"`
	Include            []string          `json:"include,omitempty"`
}

type wireInputItem struct {
	Role      string             `json:"role,omitempty"`
	Content   []wireInputContent `json:"content,omitempty"`
	Type      string             `json:"type,omitempty"`
	CallID    string             `json:"call_id,omitempty"`
	Output    string             `json:"output,omitempty"`
	Name      string             `json:"name,omitempty"`
	Arguments string             `json:"arguments,omitempty"`
}

type wireInputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type wireText struct {
	Verbosity TextVerbosity       `json:"verbosity"`
	Format    *wireResponseFormat `json:"format,omitempty"`
}

type wireResponseFormat struct {
	Type   string          `json:"type"`
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict"`
}

type wireTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"`
}

func encodeRequest(request TurnRequest) ([]byte, error) {
	var responseFormat *wireResponseFormat
	if request.ResponseFormat != nil {
		if request.ResponseFormat.Name == "" || !json.Valid(request.ResponseFormat.Schema) {
			return nil, fmt.Errorf("encoding the Codex Responses request: response format needs a name and valid schema")
		}
		responseFormat = &wireResponseFormat{
			Type: "json_schema", Name: request.ResponseFormat.Name, Schema: request.ResponseFormat.Schema, Strict: true,
		}
	}
	if request.Model == "" || request.Instructions == "" || len(request.Input) == 0 {
		return nil, fmt.Errorf("a Codex Responses turn needs a model, instructions, and input")
	}
	input := make([]wireInputItem, 0, len(request.Input))
	for _, item := range request.Input {
		switch item.Type {
		case InputUserText:
			if item.Text == "" {
				return nil, fmt.Errorf("the Codex Responses turn contains a blank user input")
			}
			input = append(input, wireInputItem{
				Role:    "user",
				Content: []wireInputContent{{Type: "input_text", Text: item.Text}},
			})
		case InputAssistantText:
			if item.Text == "" {
				return nil, fmt.Errorf("the Codex Responses turn contains a blank assistant input")
			}
			input = append(input, wireInputItem{
				Role:    "assistant",
				Content: []wireInputContent{{Type: "input_text", Text: item.Text}},
			})
		case InputFunctionOutput:
			if item.CallID == "" || item.Output == "" {
				return nil, fmt.Errorf("the Codex Responses turn contains an incomplete function output")
			}
			input = append(input, wireInputItem{
				Type:   "function_call_output",
				CallID: item.CallID,
				Output: item.Output,
			})
		case InputFunctionCall:
			if item.CallID == "" || item.Name == "" || !json.Valid(item.Arguments) {
				return nil, fmt.Errorf("the Codex Responses turn contains an incomplete function call")
			}
			input = append(input, wireInputItem{
				Type: "function_call", CallID: item.CallID, Name: item.Name, Arguments: string(item.Arguments),
			})
		default:
			return nil, fmt.Errorf("the Codex Responses turn contains an unsupported or blank input item")
		}
	}
	tools := make([]wireTool, 0, len(request.Tools))
	for _, tool := range request.Tools {
		if tool.Name == "" || tool.Description == "" || !json.Valid(tool.Parameters) {
			return nil, fmt.Errorf("the Codex Responses turn contains an invalid tool definition")
		}
		tools = append(tools, wireTool{
			Type:        "function",
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
			Strict:      true,
		})
	}
	var reasoning *ReasoningOptions
	if request.Reasoning.Effort != "" || request.Reasoning.Summary != "" {
		reasoning = &request.Reasoning
	}
	encoded, err := json.Marshal(wireRequest{
		Model:              request.Model,
		Store:              request.Store,
		Stream:             true,
		Instructions:       request.Instructions,
		Input:              input,
		Tools:              tools,
		ToolChoice:         request.ToolChoice,
		ParallelToolCalls:  request.ParallelToolCalls,
		Reasoning:          reasoning,
		Text:               wireText{Verbosity: request.TextVerbosity, Format: responseFormat},
		PromptCacheKey:     request.PromptCacheKey,
		PreviousResponseID: request.PreviousResponseID,
		Include:            request.Include,
	})
	if err != nil {
		return nil, fmt.Errorf("encoding the Codex Responses request: %w", err)
	}
	return encoded, nil
}

type wireEvent struct {
	Type        string       `json:"type"`
	OutputIndex int          `json:"output_index"`
	Delta       string       `json:"delta"`
	Arguments   string       `json:"arguments"`
	Item        wireItem     `json:"item"`
	Response    wireResponse `json:"response"`
}

type wireItem struct {
	Type      string        `json:"type"`
	ID        string        `json:"id"`
	CallID    string        `json:"call_id"`
	Name      string        `json:"name"`
	Arguments string        `json:"arguments"`
	Content   []wireContent `json:"content"`
}

type wireContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type wireResponse struct {
	ID     string    `json:"id"`
	Status string    `json:"status"`
	Usage  wireUsage `json:"usage"`
}

type wireUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

func parseStream(reader io.Reader, emit EmitFunc) (TurnResult, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), maxSSEEventBytes)
	var data []string
	var result TurnResult
	pendingCalls := make(map[int]wireItem)
	terminal := false
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := consumeEvent(data, &result, pendingCalls, emit, &terminal); err != nil {
				return TurnResult{}, fmt.Errorf("consuming a Codex Responses SSE event: %w", err)
			}
			data = data[:0]
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return TurnResult{}, fmt.Errorf("scanning SSE events: %w", err)
	}
	if err := consumeEvent(data, &result, pendingCalls, emit, &terminal); err != nil {
		return TurnResult{}, fmt.Errorf("consuming the final Codex Responses SSE event: %w", err)
	}
	if !terminal {
		return TurnResult{}, fmt.Errorf("%w: the stream ended before a terminal response event", ErrStreamInterrupted)
	}
	if result.Outcome == "" {
		return TurnResult{}, fmt.Errorf("the completed response contained neither final text nor tool calls")
	}
	return result, nil
}

func consumeEvent(
	data []string,
	result *TurnResult,
	pendingCalls map[int]wireItem,
	emit EmitFunc,
	terminal *bool,
) error {
	if len(data) == 0 {
		return nil
	}
	payload := strings.Join(data, "\n")
	if payload == "[DONE]" {
		return nil
	}
	var event wireEvent
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return fmt.Errorf("decoding SSE event: %w", err)
	}
	switch event.Type {
	case "response.created":
		result.ResponseID = event.Response.ID
	case "response.output_item.added":
		if event.Item.Type == "function_call" {
			pendingCalls[event.OutputIndex] = event.Item
		}
	case "response.output_text.delta":
		result.Text += event.Delta
		if emit != nil {
			emit(Event{Type: EventTextDelta, Delta: event.Delta})
		}
	case "response.reasoning_summary_text.delta":
		if emit != nil {
			emit(Event{Type: EventReasoningDelta, Delta: event.Delta})
		}
	case "response.function_call_arguments.delta":
		call := pendingCalls[event.OutputIndex]
		call.Arguments += event.Delta
		pendingCalls[event.OutputIndex] = call
	case "response.function_call_arguments.done":
		call := pendingCalls[event.OutputIndex]
		call.Arguments = event.Arguments
		pendingCalls[event.OutputIndex] = call
	case "response.output_item.done":
		switch event.Item.Type {
		case "message":
			var text strings.Builder
			for _, content := range event.Item.Content {
				if content.Type == "output_text" {
					text.WriteString(content.Text)
				}
			}
			if text.Len() > 0 {
				result.Text = text.String()
				result.Outcome = OutcomeFinalText
			}
		case "function_call":
			call := pendingCalls[event.OutputIndex]
			if event.Item.ID != "" {
				call.ID = event.Item.ID
			}
			if event.Item.CallID != "" {
				call.CallID = event.Item.CallID
			}
			if event.Item.Name != "" {
				call.Name = event.Item.Name
			}
			if event.Item.Arguments != "" {
				call.Arguments = event.Item.Arguments
			}
			if call.ID == "" || call.CallID == "" || call.Name == "" || !json.Valid([]byte(call.Arguments)) {
				return fmt.Errorf("the provider completed an invalid function call")
			}
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:        call.ID,
				CallID:    call.CallID,
				Name:      call.Name,
				Arguments: json.RawMessage(call.Arguments),
			})
			result.Outcome = OutcomeToolCalls
			delete(pendingCalls, event.OutputIndex)
		}
	case "response.completed":
		*terminal = true
		result.Status = event.Response.Status
		if event.Response.ID != "" {
			result.ResponseID = event.Response.ID
		}
		result.Usage = Usage{
			InputTokens:  event.Response.Usage.InputTokens,
			OutputTokens: event.Response.Usage.OutputTokens,
			TotalTokens:  event.Response.Usage.TotalTokens,
		}
		if event.Response.Status != "completed" {
			return fmt.Errorf("the response completed with status %q", event.Response.Status)
		}
	case "response.failed", "response.incomplete", "error":
		return fmt.Errorf("the provider emitted terminal event %q", event.Type)
	}
	return nil
}
