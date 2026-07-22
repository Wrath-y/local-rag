package agent

import (
	"context"
	"fmt"

	"github.com/Wrath-y/local-rag/internal/provider"
)

// AgentLoop runs the tool-use chat loop for an agent session.
type AgentLoop struct {
	llm      provider.LLMProvider
	sessions *SessionManager
	tools    *ToolRegistry
}

// NewAgentLoop creates an AgentLoop.
func NewAgentLoop(llm provider.LLMProvider, sessions *SessionManager, tools *ToolRegistry) *AgentLoop {
	return &AgentLoop{
		llm:      llm,
		sessions: sessions,
		tools:    tools,
	}
}

// Chat handles a user message in a session, running the chat loop.
// v1: basic chat with persistent history; full tool-use loop to be added later.
func (a *AgentLoop) Chat(ctx context.Context, sessionID, userMessage string) (string, error) {
	return a.ChatWithContext(ctx, sessionID, userMessage, "")
}

// ChatWithContext adds request-scoped instructions immediately before the
// session history. It is intentionally not persisted, so retrieval evidence
// cannot leak into a later request or become stale session history.
func (a *AgentLoop) ChatWithContext(ctx context.Context, sessionID, userMessage, requestContext string) (string, error) {
	// 1. Append user message to history.
	if err := a.sessions.AppendMessage(sessionID, "user", userMessage); err != nil {
		return "", fmt.Errorf("agent chat: append user message: %w", err)
	}

	// 2. Load full history.
	history, err := a.sessions.LoadHistory(sessionID)
	if err != nil {
		return "", fmt.Errorf("agent chat: load history: %w", err)
	}

	// 3. Convert history to provider.Message slice.
	messages := make([]provider.Message, 0, len(history)+1)
	if requestContext != "" {
		// Anthropic's Messages API does not accept the OpenAI-style system role.
		// A prefixed user message keeps this portable across all configured LLMs.
		messages = append(messages, provider.Message{Role: "user", Content: requestContext})
	}
	for _, m := range history {
		messages = append(messages, provider.Message{
			Role:    m["role"],
			Content: m["content"],
		})
	}

	// 4. Call LLM.
	if a.llm == nil {
		return "", fmt.Errorf("agent chat: LLM provider not configured")
	}
	reply, err := a.llm.Complete(ctx, messages)
	if err != nil {
		return "", fmt.Errorf("agent chat: LLM complete: %w", err)
	}

	// 5. Append assistant response to history.
	if err := a.sessions.AppendMessage(sessionID, "assistant", reply); err != nil {
		return "", fmt.Errorf("agent chat: append assistant message: %w", err)
	}

	return reply, nil
}
