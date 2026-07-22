package handler

import (
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Wrath-y/local-rag/internal/citation"
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
	evidence, err := h.doRetrieveEvidence(req.Prompt, 0)
	if err != nil || len(evidence) == 0 {
		outcome = observe.HookOutcomeNoResults
		if err != nil {
			reason = observe.HookReasonRetrievalError
		}
		c.JSON(http.StatusOK, gin.H{"additional_context": ""})
		return
	}

	manifest := h.citations.Create(evidence)
	ctx := citation.RenderAnswerInstructions(manifest.Citations)

	outcome = observe.HookOutcomeInjected
	c.JSON(http.StatusOK, gin.H{
		"additional_context": ctx,
		"citations":          manifest.Citations,
		"evidence_token":     manifest.Token,
	})
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
