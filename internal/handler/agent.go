package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Wrath-y/local-rag/internal/agent"
	"github.com/Wrath-y/local-rag/internal/citation"
	"github.com/Wrath-y/local-rag/internal/management"
	"github.com/Wrath-y/local-rag/internal/provider"
)

// agentState holds lazily-initialised agent components.
type agentState struct {
	once     sync.Once
	sessions *agent.SessionManager
	loop     *agent.AgentLoop
	initErr  error
}

// agentComponents returns the lazily-initialised agent session manager and loop.
// Initialization is done once and the result (or error) is cached.
func (h *Handler) agentComponents() (*agent.SessionManager, *agent.AgentLoop, error) {
	h.agent.once.Do(func() {
		if h.deps.Stores == nil {
			h.agent.initErr = fmt.Errorf("agent: store lifecycle is unavailable")
			return
		}
		st := h.deps.Stores.Store()
		if st == nil {
			h.agent.initErr = fmt.Errorf("agent: store is unavailable")
			return
		}
		sm, err := agent.NewSessionManager(st.DB())
		if err != nil {
			h.agent.initErr = err
			return
		}
		tools := agent.NewToolRegistry(append([]agent.ToolExecutor{agent.NewRAGRetrieveTool(h.doAgentRetrieveEvidence)}, h.agentMutationTools()...)...)
		budget := agent.DefaultToolBudget()
		if h.deps.Config != nil {
			cfg := h.deps.Config.Agent
			budget = agent.ToolBudget{MaxRounds: cfg.MaxRounds, MaxToolCalls: cfg.MaxToolCalls, Deadline: time.Duration(cfg.DeadlineSeconds) * time.Second, MaxContextBytes: cfg.MaxContextBytes, MaxResultBytes: cfg.MaxResultBytes, MaxTopK: cfg.MaxTopK}
		}
		loop := agent.NewAgentLoop(h.deps.LLM, sm, tools, budget)
		h.agent.sessions = sm
		h.agent.loop = loop
	})
	return h.agent.sessions, h.agent.loop, h.agent.initErr
}

// AgentChat handles POST /agent/chat
// Body: {"session_id": "...", "message": "..."}
// Response: {"response": "..."}
func (h *Handler) AgentChat(c *gin.Context) {
	started := time.Now()
	var req struct {
		SessionID string `json:"session_id" binding:"required"`
		Message   string `json:"message"    binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		slog.Warn("agent chat http request rejected", "err", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	slog.Info("agent chat http request accepted", "session_id", req.SessionID, "message_bytes", len(req.Message), "remote_addr", c.ClientIP())

	_, loop, err := h.agentComponents()
	if err != nil {
		slog.Error("agent chat http initialization failed", "session_id", req.SessionID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent init: " + err.Error()})
		return
	}

	result, err := loop.ChatWithResult(c.Request.Context(), req.SessionID, req.Message, citation.RenderAnswerInstructions(nil))
	if err != nil {
		slog.Error("agent chat http request failed", "session_id", req.SessionID, "duration_ms", time.Since(started).Milliseconds(), "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	manifest := h.citations.Create(result.Evidence)
	validation, _ := h.citations.Validate(manifest.Token, result.Response)
	slog.Info("agent chat http response", "session_id", req.SessionID, "outcome", result.Outcome, "rounds", result.Rounds, "tool_calls", result.ToolCalls, "evidence_count", len(result.Evidence), "permission_required", result.Permission != nil, "duration_ms", time.Since(started).Milliseconds())
	c.JSON(http.StatusOK, gin.H{
		"response":            result.Response,
		"citations":           manifest.Citations,
		"evidence_token":      manifest.Token,
		"citation_validation": validation,
		"outcome":             result.Outcome,
		"rounds":              result.Rounds,
		"tool_calls":          result.ToolCalls,
		"permission_request":  result.Permission,
	})
}

// AgentApprovePermission handles POST /agent/permission/:token. Approval is
// bound to the session that received the prompt and executes exactly one call.
func (h *Handler) AgentApprovePermission(c *gin.Context) {
	started := time.Now()
	var req struct {
		SessionID string `json:"session_id" binding:"required"`
		Approved  bool   `json:"approved"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		slog.Warn("agent permission http request rejected", "err", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	_, loop, err := h.agentComponents()
	if err != nil {
		slog.Error("agent permission http initialization failed", "session_id", req.SessionID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent init: " + err.Error()})
		return
	}
	result, err := loop.Approve(c.Request.Context(), req.SessionID, c.Param("token"), req.Approved)
	if err != nil {
		slog.Warn("agent permission http request failed", "session_id", req.SessionID, "approved", req.Approved, "duration_ms", time.Since(started).Milliseconds(), "err", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	slog.Info("agent permission http response", "session_id", req.SessionID, "tool", result.Tool, "approved", result.Approved, "executed", result.Executed, "outcome", result.Outcome, "duration_ms", time.Since(started).Milliseconds())
	c.JSON(http.StatusOK, result)
}

func (h *Handler) agentMutationTools() []agent.ToolExecutor {
	return []agent.ToolExecutor{
		agent.ApprovedTool{Def: agentToolDefinition("rag_ingest", "Add or replace a source in the knowledge base.", `{"type":"object","additionalProperties":false,"required":["source","text"],"properties":{"source":{"type":"string","minLength":1},"text":{"type":"string","minLength":1}}}`), Check: validateIngestTool, Run: h.runAgentIngest},
		agent.ApprovedTool{Def: agentToolDefinition("rag_delete_source", "Delete every chunk from one knowledge-base source.", `{"type":"object","additionalProperties":false,"required":["source"],"properties":{"source":{"type":"string","minLength":1}}}`), Check: validateDeleteTool, Run: h.runAgentDeleteSource},
		agent.ApprovedTool{Def: agentToolDefinition("rag_index_rebuild", "Rebuild the knowledge-base vector index.", `{"type":"object","additionalProperties":false,"properties":{}}`), Check: validateRebuildTool, Run: h.runAgentIndexRebuild},
	}
}

func agentToolDefinition(name, description, schema string) provider.ToolDefinition {
	return provider.ToolDefinition{Name: name, Description: description, InputSchema: json.RawMessage(schema)}
}
func strictDecode(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}
func validateIngestTool(raw json.RawMessage) error {
	var in struct {
		Source string `json:"source"`
		Text   string `json:"text"`
	}
	if err := strictDecode(raw, &in); err != nil {
		return err
	}
	if strings.TrimSpace(in.Source) == "" || strings.TrimSpace(in.Text) == "" {
		return fmt.Errorf("source and text are required")
	}
	return nil
}
func validateDeleteTool(raw json.RawMessage) error {
	var in struct {
		Source string `json:"source"`
	}
	if err := strictDecode(raw, &in); err != nil {
		return err
	}
	if strings.TrimSpace(in.Source) == "" {
		return fmt.Errorf("source is required")
	}
	return nil
}
func validateRebuildTool(raw json.RawMessage) error {
	var in map[string]any
	if err := strictDecode(raw, &in); err != nil {
		return err
	}
	if len(in) != 0 {
		return fmt.Errorf("rag_index_rebuild accepts no arguments")
	}
	return nil
}
func (h *Handler) runAgentIngest(ctx context.Context, raw json.RawMessage) (agent.ToolResult, error) {
	var in struct {
		Source string `json:"source"`
		Text   string `json:"text"`
	}
	if err := strictDecode(raw, &in); err != nil {
		return agent.ToolResult{}, err
	}
	submission, err := h.management.UpdateSource(ctx, management.UpdateSourceRequest{Source: strings.TrimSpace(in.Source), Content: in.Text, Confirm: true})
	if err != nil {
		return agent.ToolResult{}, err
	}
	task, found, err := h.management.Tasks().Wait(ctx, submission.TaskID)
	if err != nil || !found || task.State != management.TaskSucceeded {
		return agent.ToolResult{}, fmt.Errorf("ingest task failed")
	}
	return agent.ToolResult{Content: "Knowledge-base source ingested."}, nil
}
func (h *Handler) runAgentDeleteSource(_ context.Context, raw json.RawMessage) (agent.ToolResult, error) {
	var in struct {
		Source string `json:"source"`
	}
	if err := strictDecode(raw, &in); err != nil {
		return agent.ToolResult{}, err
	}
	deleted, err := h.management.DeleteSource(strings.TrimSpace(in.Source))
	if err != nil {
		return agent.ToolResult{}, err
	}
	return agent.ToolResult{Content: fmt.Sprintf("Deleted %d chunks.", deleted)}, nil
}
func (h *Handler) runAgentIndexRebuild(_ context.Context, _ json.RawMessage) (agent.ToolResult, error) {
	status, started, err := h.indexRebuild.Start()
	if err != nil {
		return agent.ToolResult{}, err
	}
	if !started {
		return agent.ToolResult{Content: "Index rebuild is already running: " + status.TaskID}, nil
	}
	return agent.ToolResult{Content: "Index rebuild started: " + status.TaskID}, nil
}

// doAgentRetrieveEvidence is the sole Agent tool adapter. The Agent passes a
// bounded top_k and request context; no write-capable Handler method is wired.
func (h *Handler) doAgentRetrieveEvidence(ctx context.Context, text string, topK int) ([]citation.Evidence, error) {
	return h.retrieveEvidence(ctx, text, 0, topK)
}

// AgentCreateSession handles POST /agent/session
// Body: {"metadata": "{}"}
// Response: {"session_id": "..."}
func (h *Handler) AgentCreateSession(c *gin.Context) {
	var req struct {
		Metadata string `json:"metadata"`
	}
	// Not binding-required; metadata is optional.
	_ = c.ShouldBindJSON(&req)

	sessions, _, err := h.agentComponents()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent init: " + err.Error()})
		return
	}

	id, err := sessions.Create(req.Metadata)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"session_id": id})
}

// AgentListSessions handles GET /agent/sessions
// Response: {"sessions": [...]}
func (h *Handler) AgentListSessions(c *gin.Context) {
	sessions, _, err := h.agentComponents()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent init: " + err.Error()})
		return
	}

	list, err := sessions.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if list == nil {
		list = []agent.Session{}
	}
	c.JSON(http.StatusOK, gin.H{"sessions": list})
}

// AgentDeleteSession handles DELETE /agent/session/:id
// Response: {"message": "deleted"}
func (h *Handler) AgentDeleteSession(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id is required"})
		return
	}

	sessions, _, err := h.agentComponents()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent init: " + err.Error()})
		return
	}

	if err := sessions.Delete(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}
