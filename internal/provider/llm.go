package provider

import "context"

// LLMProvider generates text completions from a conversation history.
type LLMProvider interface {
	// Complete sends the messages to the LLM and returns the assistant reply.
	Complete(ctx context.Context, messages []Message) (string, error)
}
