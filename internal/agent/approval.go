package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Wrath-y/local-rag/internal/provider"
)

// PermissionRequest is safe to return to a client: it identifies the action
// but deliberately omits mutation arguments, which can contain document text.
type PermissionRequest struct {
	Token     string    `json:"token"`
	Tool      string    `json:"tool"`
	Operation string    `json:"operation"`
	ExpiresAt time.Time `json:"expires_at"`
}

type pendingApproval struct {
	sessionID string
	call      provider.ToolCall
	budget    ToolBudget
	expiresAt time.Time
}

type approvalManager struct {
	mu      sync.Mutex
	pending map[string]pendingApproval
	ttl     time.Duration
}

func newApprovalManager() *approvalManager {
	return &approvalManager{pending: make(map[string]pendingApproval), ttl: 5 * time.Minute}
}

func (m *approvalManager) request(sessionID string, call provider.ToolCall, budget ToolBudget) PermissionRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for token, pending := range m.pending {
		if !pending.expiresAt.After(now) {
			delete(m.pending, token)
		}
	}
	token := approvalToken()
	expiresAt := now.Add(m.ttl)
	m.pending[token] = pendingApproval{sessionID: sessionID, call: call, budget: budget, expiresAt: expiresAt}
	return PermissionRequest{Token: token, Tool: call.Name, Operation: operationForTool(call.Name), ExpiresAt: expiresAt}
}

func (m *approvalManager) take(sessionID, token string) (pendingApproval, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pending, ok := m.pending[token]
	if !ok || !pending.expiresAt.After(time.Now()) {
		delete(m.pending, token)
		return pendingApproval{}, fmt.Errorf("permission request not found or expired")
	}
	if pending.sessionID != sessionID {
		return pendingApproval{}, fmt.Errorf("permission request does not belong to this session")
	}
	delete(m.pending, token)
	return pending, nil
}

func approvalToken() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err == nil {
		return hex.EncodeToString(raw)
	}
	return fmt.Sprintf("approval-%d", time.Now().UnixNano())
}

func operationForTool(tool string) string {
	switch tool {
	case "rag_ingest":
		return "ingest knowledge-base content"
	case "rag_delete_source":
		return "delete a knowledge-base source"
	case "rag_index_rebuild":
		return "rebuild the knowledge-base index"
	default:
		return "perform a knowledge-base mutation"
	}
}

type ApprovalResult struct {
	Approved bool       `json:"approved"`
	Executed bool       `json:"executed"`
	Tool     string     `json:"tool"`
	Outcome  string     `json:"outcome"`
	Result   ToolResult `json:"result,omitempty"`
}

// Approve executes exactly one pending validated mutation after a user choice.
func (a *AgentLoop) Approve(ctx context.Context, sessionID, token string, approved bool) (ApprovalResult, error) {
	pending, err := a.approvals.take(sessionID, token)
	if err != nil {
		slog.Warn("agent permission request rejected", "session_id", sessionID, "approved", approved, "err", err)
		return ApprovalResult{}, err
	}
	slog.Info("agent permission decision received", "session_id", sessionID, "tool", pending.call.Name, "call_id", pending.call.ID, "approved", approved)
	result := ApprovalResult{Approved: approved, Tool: pending.call.Name}
	if !approved {
		result.Outcome = "denied"
		_ = a.sessions.RecordToolTrace(ToolTrace{SessionID: sessionID, CallID: pending.call.ID, Tool: pending.call.Name, Outcome: "denied", ErrorCategory: "permission_denied"})
		slog.Info("agent tool call denied", "session_id", sessionID, "tool", pending.call.Name, "call_id", pending.call.ID)
		return result, nil
	}
	ctx, cancel := context.WithTimeout(ctx, pending.budget.Deadline)
	defer cancel()
	started := time.Now()
	toolResult, category, execErr := a.tools.Execute(ctx, pending.call, pending.budget)
	trace := ToolTrace{SessionID: sessionID, CallID: pending.call.ID, Tool: pending.call.Name, StartedAt: started, Duration: time.Since(started), ResultCount: len(toolResult.Evidence), Outcome: "completed", ErrorCategory: category}
	if execErr != nil {
		trace.Outcome = "failed"
		if trace.ErrorCategory == "" {
			trace.ErrorCategory = "tool_error"
		}
	}
	slog.Info("agent approved tool call finished", "session_id", sessionID, "tool", pending.call.Name, "call_id", pending.call.ID, "outcome", trace.Outcome, "duration_ms", trace.Duration.Milliseconds(), "result_evidence_count", len(toolResult.Evidence), "result_content_chars", len([]rune(toolResult.Content)))
	_ = a.sessions.RecordToolTrace(trace)
	result.Executed, result.Result, result.Outcome = execErr == nil, toolResult, trace.Outcome
	if execErr != nil {
		return result, fmt.Errorf("execute approved %s: %w", pending.call.Name, execErr)
	}
	return result, nil
}
