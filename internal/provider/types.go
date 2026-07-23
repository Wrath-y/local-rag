package provider

import (
	"context"
	"encoding/json"
)

// Message represents a single turn in a conversation with an LLM.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"-"`
}

// ToolDefinition is the portable subset of a provider tool schema used by the
// Agent. InputSchema is a JSON Schema object.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ToolCall is a model request to invoke a registered tool. Arguments must be
// a JSON object and are validated by the Agent before any executor runs.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Completion carries an optional final answer and zero or more tool calls.
type Completion struct {
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCallingProvider is implemented by providers with native tool calling.
// LLMProvider remains supported through the Agent's validated JSON fallback.
type ToolCallingProvider interface {
	LLMProvider
	CompleteWithTools(context.Context, []Message, []ToolDefinition) (Completion, error)
}

// RerankResult holds the re-ranked position and relevance score for a document.
type RerankResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}
