package handler

import (
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/Wrath-y/local-rag/internal/agent"
)

// agentState holds lazily-initialised agent components.
type agentState struct {
	once     sync.Once
	sessions *agent.SessionManager
	loop     *agent.AgentLoop
	initErr  error
}

var globalAgentState agentState

// agentComponents returns the lazily-initialised agent session manager and loop.
// Initialization is done once and the result (or error) is cached.
func (h *Handler) agentComponents() (*agent.SessionManager, *agent.AgentLoop, error) {
	globalAgentState.once.Do(func() {
		sm, err := agent.NewSessionManager(h.deps.Store.DB())
		if err != nil {
			globalAgentState.initErr = err
			return
		}
		tools := agent.NewToolRegistry()
		loop := agent.NewAgentLoop(h.deps.LLM, sm, tools)
		globalAgentState.sessions = sm
		globalAgentState.loop = loop
	})
	return globalAgentState.sessions, globalAgentState.loop, globalAgentState.initErr
}

// AgentChat handles POST /agent/chat
// Body: {"session_id": "...", "message": "..."}
// Response: {"response": "..."}
func (h *Handler) AgentChat(c *gin.Context) {
	var req struct {
		SessionID string `json:"session_id" binding:"required"`
		Message   string `json:"message"    binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	_, loop, err := h.agentComponents()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent init: " + err.Error()})
		return
	}

	reply, err := loop.Chat(c.Request.Context(), req.SessionID, req.Message)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"response": reply})
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
