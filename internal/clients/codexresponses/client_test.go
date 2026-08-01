package codexresponses

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/0x63616c/software-factory/internal/work"
)

func TestEncodeRequestIncludesStrictJSONSchemaResponseFormat(t *testing.T) {
	t.Parallel()

	encoded, err := encodeRequest(TurnRequest{
		Model: "gpt-test", Instructions: "work carefully", Input: []InputItem{UserText("ship it")},
		ToolChoice: ToolChoiceAuto, TextVerbosity: TextVerbosityMedium,
		ResponseFormat: &ResponseFormat{
			Name:   "implement_result",
			Schema: json.RawMessage(`{"type":"object","properties":{"report":{"type":"string"}},"required":["report"],"additionalProperties":false}`),
		},
	})
	if err != nil {
		t.Fatalf("encodeRequest() error = %v", err)
	}
	var request struct {
		Text struct {
			Format struct {
				Type   string          `json:"type"`
				Name   string          `json:"name"`
				Schema json.RawMessage `json:"schema"`
				Strict bool            `json:"strict"`
			} `json:"format"`
		} `json:"text"`
	}
	if err := json.Unmarshal(encoded, &request); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if request.Text.Format.Type != "json_schema" || request.Text.Format.Name != "implement_result" ||
		!request.Text.Format.Strict || !json.Valid(request.Text.Format.Schema) {
		t.Fatalf("encoded response format = %#v", request.Text.Format)
	}
}

type staticCredentialSource struct {
	credential Credential
}

func TestTurnEncodesAFunctionOutputContinuation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("originator") != "software-factory" {
			t.Errorf("originator = %q, want standalone software-factory identity", r.Header.Get("originator"))
		}
		if r.Header.Get("session-id") != "workflow-123" ||
			r.Header.Get("x-client-request-id") != "agent/run-7/plan/model/1" ||
			r.Header.Get("Idempotency-Key") != "agent/run-7/plan/model/1" {
			t.Errorf("session affinity headers are absent")
		}
		var request struct {
			PreviousResponseID string `json:"previous_response_id"`
			Input              []struct {
				Type      string `json:"type"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
				Output    string `json:"output"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decoding request: %v", err)
			return
		}
		if request.PreviousResponseID != "resp_tool" || len(request.Input) != 2 {
			t.Fatalf("continuation request = %#v", request)
		}
		if got := request.Input[0]; got.Type != "function_call" || got.CallID != "call_123" || got.Name != "lookup_weather" || got.Arguments != `{"city":"London"}` {
			t.Errorf("function call = %#v", got)
		}
		if got := request.Input[1]; got.Type != "function_call_output" || got.CallID != "call_123" || got.Output != `{"temperature_c":18}` {
			t.Errorf("function output = %#v", got)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"It is 18 C.\"}]}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_final\",\"status\":\"completed\"}}\n\n")
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	result, err := client.Turn(context.Background(), TurnRequest{
		Model:        "gpt-test",
		Instructions: "Answer briefly.",
		Input: []InputItem{
			FunctionCall(ToolCall{CallID: "call_123", Name: "lookup_weather", Arguments: json.RawMessage(`{"city":"London"}`)}),
			FunctionOutput("call_123", `{"temperature_c":18}`),
		},
		Store:              true,
		PreviousResponseID: "resp_tool",
		ToolChoice:         ToolChoiceAuto,
		TextVerbosity:      TextVerbosityLow,
		PromptCacheKey:     "workflow-123",
		IdempotencyKey:     "agent/run-7/plan/model/1",
	}, nil)
	if err != nil {
		t.Fatalf("running continuation: %v", err)
	}
	if result.Outcome != OutcomeFinalText || result.Text != "It is 18 C." {
		t.Fatalf("result = %#v", result)
	}
}

func TestTurnReportsTerminalFailuresWithoutLeakingProviderBodies(t *testing.T) {
	t.Parallel()

	const secret = "super-secret-access-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `credential rejected: `+secret)
	}))
	defer server.Close()

	client, err := New(
		&http.Client{Timeout: 2 * time.Second},
		server.URL,
		staticCredentialSource{credential: Credential{
			AccessToken: work.NewCredential(secret),
			AccountID:   "account-123",
		}},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatalf("constructing client: %v", err)
	}

	_, err = client.Turn(context.Background(), TurnRequest{
		Model:         "gpt-test",
		Instructions:  "Answer briefly.",
		Input:         []InputItem{UserText("Hello")},
		ToolChoice:    ToolChoiceNone,
		TextVerbosity: TextVerbosityLow,
	}, nil)
	if err == nil {
		t.Fatal("running turn succeeded, want HTTP error")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "credential rejected") {
		t.Fatalf("error leaked provider body: %v", err)
	}
}

func TestTurnClassifiesRateLimitResponses(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error"}}`)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	_, err := client.Turn(context.Background(), TurnRequest{
		Model: "gpt-test", Instructions: "Answer.", Input: []InputItem{UserText("Hello")},
		ToolChoice: ToolChoiceNone, TextVerbosity: TextVerbosityLow,
	}, nil)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Turn() error = %v, want ErrRateLimited", err)
	}
}

func TestTurnClassifiesAuthenticationResponses(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"type":"authentication_error"}}`)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	_, err := client.Turn(context.Background(), TurnRequest{
		Model: "gpt-test", Instructions: "Answer.", Input: []InputItem{UserText("Hello")},
		ToolChoice: ToolChoiceNone, TextVerbosity: TextVerbosityLow,
	}, nil)
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("Turn() error = %v, want ErrAuth", err)
	}
}

func TestTurnReportsOnlySafeProviderErrorMetadata(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":"invalid_type","type":"invalid_request_error","message":"Invalid type for 'tools[0].strict': sensitive provider prose"}}`)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	_, err := client.Turn(context.Background(), TurnRequest{
		Model: "gpt-test", Instructions: "Answer.", Input: []InputItem{UserText("Hello")},
		ToolChoice: ToolChoiceNone, TextVerbosity: TextVerbosityLow,
	}, nil)
	if err == nil {
		t.Fatal("running turn succeeded, want HTTP error")
	}
	if !strings.Contains(err.Error(), "code=invalid_type") || !strings.Contains(err.Error(), "param=tools[0].strict") ||
		strings.Contains(err.Error(), "sensitive provider prose") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseStreamClassifiesTerminalEventsAndReasoningProgress(t *testing.T) {
	t.Parallel()

	var events []Event
	_, err := parseStream(strings.NewReader(
		"data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"Checking the tool\"}\n\n"+
			"data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_partial\",\"status\":\"incomplete\"}}\n\n",
	), func(event Event) { events = append(events, event) })
	if err == nil || !strings.Contains(err.Error(), "response.incomplete") {
		t.Fatalf("error = %v, want incomplete classification", err)
	}
	if len(events) != 1 || events[0].Type != EventReasoningDelta || events[0].Delta != "Checking the tool" {
		t.Fatalf("events = %#v", events)
	}

	_, err = parseStream(strings.NewReader("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_cut\"}}\n\n"), nil)
	if err == nil || !errors.Is(err, ErrStreamInterrupted) {
		t.Fatalf("error = %v, want ErrStreamInterrupted", err)
	}
}

func newTestClient(t *testing.T, endpoint string) *Client {
	t.Helper()
	client, err := New(
		&http.Client{Timeout: 2 * time.Second},
		endpoint,
		staticCredentialSource{credential: Credential{
			AccessToken: work.NewCredential("access-token"),
			AccountID:   "account-123",
		}},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatalf("constructing client: %v", err)
	}
	return client
}

func (s staticCredentialSource) Credential(context.Context) (Credential, error) {
	return s.credential, nil
}

func TestTurnReturnsACompletedTextResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Errorf("Authorization = %q, want redacted test credential", got)
		}
		if got := r.Header.Get("chatgpt-account-id"); got != "account-123" {
			t.Errorf("chatgpt-account-id = %q, want account-123", got)
		}

		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decoding request: %v", err)
			return
		}
		if request["model"] != "gpt-test" || request["instructions"] != "Answer briefly." || request["stream"] != true {
			t.Errorf("request = %#v", request)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_123\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg_123\",\"role\":\"assistant\",\"content\":[]}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"hello \"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"world\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg_123\",\"role\":\"assistant\",\"phase\":\"final_answer\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello world\"}]}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_123\",\"status\":\"completed\",\"usage\":{\"input_tokens\":12,\"output_tokens\":3,\"total_tokens\":15}}}\n\n")
	}))
	defer server.Close()

	client, err := New(
		&http.Client{Timeout: 2 * time.Second},
		server.URL,
		staticCredentialSource{credential: Credential{
			AccessToken: work.NewCredential("access-token"),
			AccountID:   "account-123",
		}},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatalf("constructing client: %v", err)
	}

	result, err := client.Turn(context.Background(), TurnRequest{
		Model:         "gpt-test",
		Instructions:  "Answer briefly.",
		Input:         []InputItem{UserText("Say hello.")},
		Store:         false,
		ToolChoice:    ToolChoiceNone,
		TextVerbosity: TextVerbosityLow,
	}, nil)
	if err != nil {
		t.Fatalf("running turn: %v", err)
	}
	if result.Outcome != OutcomeFinalText || result.Text != "hello world" || result.ResponseID != "resp_123" {
		t.Fatalf("result = %#v", result)
	}
	if result.Usage.InputTokens != 12 || result.Usage.OutputTokens != 3 || result.Usage.TotalTokens != 15 {
		t.Fatalf("usage = %#v", result.Usage)
	}
}

func TestTurnReturnsACompleteToolCallFromStreamedArguments(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools             []Tool           `json:"tools"`
			ToolChoice        ToolChoice       `json:"tool_choice"`
			ParallelToolCalls bool             `json:"parallel_tool_calls"`
			Reasoning         ReasoningOptions `json:"reasoning"`
			Text              struct {
				Verbosity TextVerbosity `json:"verbosity"`
			} `json:"text"`
			PromptCacheKey string `json:"prompt_cache_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decoding request: %v", err)
			return
		}
		if len(request.Tools) != 1 || request.Tools[0].Name != "lookup_weather" || request.ToolChoice != ToolChoiceAuto {
			t.Errorf("tool request = %#v", request)
		}
		if request.ParallelToolCalls || request.Reasoning.Effort != ReasoningEffortMedium ||
			request.Reasoning.Summary != ReasoningSummaryAuto || request.Text.Verbosity != TextVerbosityMedium ||
			request.PromptCacheKey != "workflow-123" {
			t.Errorf("request options = %#v", request)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_tool\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_123\",\"call_id\":\"call_123\",\"name\":\"lookup_weather\",\"arguments\":\"\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"delta\":\"{\\\"city\\\":\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"delta\":\"\\\"London\\\"}\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.function_call_arguments.done\",\"output_index\":0,\"arguments\":\"{\\\"city\\\":\\\"London\\\"}\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_123\",\"call_id\":\"call_123\",\"name\":\"lookup_weather\",\"arguments\":\"{\\\"city\\\":\\\"London\\\"}\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_tool\",\"status\":\"completed\",\"usage\":{\"input_tokens\":20,\"output_tokens\":8,\"total_tokens\":28}}}\n\n")
	}))
	defer server.Close()

	client, err := New(
		&http.Client{Timeout: 2 * time.Second},
		server.URL,
		staticCredentialSource{credential: Credential{
			AccessToken: work.NewCredential("access-token"),
			AccountID:   "account-123",
		}},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatalf("constructing client: %v", err)
	}

	result, err := client.Turn(context.Background(), TurnRequest{
		Model:        "gpt-test",
		Instructions: "Use the weather tool.",
		Input:        []InputItem{UserText("Weather in London?")},
		Store:        true,
		Tools: []Tool{{
			Name:        "lookup_weather",
			Description: "Look up deterministic prototype weather.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}`),
		}},
		ToolChoice:        ToolChoiceAuto,
		ParallelToolCalls: false,
		Reasoning: ReasoningOptions{
			Effort:  ReasoningEffortMedium,
			Summary: ReasoningSummaryAuto,
		},
		TextVerbosity:  TextVerbosityMedium,
		PromptCacheKey: "workflow-123",
	}, nil)
	if err != nil {
		t.Fatalf("running turn: %v", err)
	}
	if result.Outcome != OutcomeToolCalls || result.ResponseID != "resp_tool" || len(result.ToolCalls) != 1 {
		t.Fatalf("result = %#v", result)
	}
	call := result.ToolCalls[0]
	if call.ID != "fc_123" || call.CallID != "call_123" || call.Name != "lookup_weather" || string(call.Arguments) != `{"city":"London"}` {
		t.Fatalf("tool call = %#v", call)
	}
}
