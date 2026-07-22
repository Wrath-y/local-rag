package handler

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Wrath-y/local-rag/internal/observe"
)

type hookRequest struct {
	Prompt         string `json:"prompt"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
}

// Hook is the UserPromptSubmit hook endpoint.
// It checks whether rag-mode is enabled (via a flag file in CWD) and, if so,
// performs a retrieval and returns the results as additionalContext.
func (h *Handler) Hook(c *gin.Context) {
	var req hookRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Check rag-mode flag file.
	if !h.ragModeEnabled(req.CWD) {
		c.JSON(http.StatusOK, gin.H{"additional_context": ""})
		return
	}

	if req.Prompt == "" {
		c.JSON(http.StatusOK, gin.H{"additional_context": ""})
		return
	}
	started := time.Now()
	var outcome observe.HookOutcome
	var reason observe.HookReasonCode
	defer func() {
		if outcome != "" {
			h.hookObservations.Record(outcome, time.Since(started), reason, h.verboseEnabled)
		}
	}()

	// Perform retrieval.
	chunks, err := h.doRetrieve(req.Prompt, 0)
	if err != nil || len(chunks) == 0 {
		outcome = observe.HookOutcomeNoResults
		if err != nil {
			reason = observe.HookReasonRetrievalError
		}
		c.JSON(http.StatusOK, gin.H{"additional_context": ""})
		return
	}

	ctx := "[RAG 自动检索结果]\n" +
		strings.Join(chunks, "\n---\n") +
		"\n\n请参考以上内容回答用户问题。若无关则忽略。"

	outcome = observe.HookOutcomeInjected
	c.JSON(http.StatusOK, gin.H{"additional_context": ctx})
}

type hookOutcomeReport struct {
	Outcome    observe.HookOutcome    `json:"outcome"`
	ReasonCode observe.HookReasonCode `json:"reason_code"`
}

// HookOutcomeReport accepts best-effort client classifications. It intentionally
// accepts only outcomes not already recorded by a valid /hook response.
func (h *Handler) HookOutcomeReport(c *gin.Context) {
	var report hookOutcomeReport
	if err := c.ShouldBindJSON(&report); err != nil || !observe.ClientReportedHookOutcome(report.Outcome) || !observe.ValidHookReasonCode(report.ReasonCode) {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	h.hookObservations.Record(report.Outcome, 0, report.ReasonCode, h.verboseEnabled)
	c.AbortWithStatus(http.StatusNoContent)
}

// ragModeEnabled returns true when the .rag-mode flag file exists in cwd.
func (h *Handler) ragModeEnabled(cwd string) bool {
	if cwd == "" {
		return false
	}
	flagPath := filepath.Join(cwd, ".rag-mode")
	_, err := os.Stat(flagPath)
	return err == nil
}
