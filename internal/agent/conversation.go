package agent

import "encoding/json"

// ConversationRef identifies one immutable conversation revision.
type ConversationRef struct {
	Key      string `json:"key"`
	Revision int    `json:"revision"`
	Bytes    int64  `json:"bytes"`
	Digest   string `json:"digest"`
}

// ConversationItemKind identifies a provider-neutral conversation item.
type ConversationItemKind string

const (
	// ItemInstructions is the durable system/developer instruction block.
	ItemInstructions ConversationItemKind = "instructions"
	// ItemUserText is an original user request.
	ItemUserText ConversationItemKind = "user_text"
	// ItemAssistantText is a terminal assistant response.
	ItemAssistantText ConversationItemKind = "assistant_text"
	// ItemFunctionCall is a model function request.
	ItemFunctionCall ConversationItemKind = "function_call"
	// ItemFunctionOutput is the result paired with a function call.
	ItemFunctionOutput ConversationItemKind = "function_output"
)

// ConversationItem is one provider-neutral item added by a revision.
type ConversationItem struct {
	Kind      ConversationItemKind `json:"kind"`
	Text      string               `json:"text,omitempty"`
	ID        string               `json:"id,omitempty"`
	CallID    string               `json:"call_id,omitempty"`
	Name      string               `json:"name,omitempty"`
	Arguments json.RawMessage      `json:"arguments,omitempty"`
	Output    string               `json:"output,omitempty"`
}

// ConversationRevision is one immutable delta and its predecessor.
type ConversationRevision struct {
	Predecessor *ConversationRef   `json:"predecessor"`
	Items       []ConversationItem `json:"items"`
}
