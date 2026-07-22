package handler

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Wrath-y/local-rag/internal/management"
)

// BackupRun creates an immediate backup of the database file.
func (h *Handler) BackupRun(c *gin.Context) {
	submission, err := h.management.BackupRun()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	task, found, err := h.management.Tasks().Wait(context.Background(), submission.TaskID)
	if err != nil || !found || task.State != management.TaskSucceeded {
		errorText := "backup failed"
		if err != nil {
			errorText = err.Error()
		} else if found && task.Error != "" {
			errorText = task.Error
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": errorText})
		return
	}
	c.JSON(http.StatusOK, task.Result)
}

// BackupList lists available backup zip files, newest first.
func (h *Handler) BackupList(c *gin.Context) {
	backups, err := h.management.BackupList()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"backups": backups})
}

type restoreRequest struct {
	File    string `json:"file" binding:"required"`
	Confirm bool   `json:"confirm"`
}

// BackupRestore restores a confirmed project backup through RestoreService.
func (h *Handler) BackupRestore(c *gin.Context) {
	var req restoreRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !req.Confirm {
		c.JSON(http.StatusBadRequest, gin.H{"error": "confirm must be true"})
		return
	}
	submission, err := h.management.BackupRestore(management.ImportRequest{Path: req.File, Confirm: true})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	task, found, err := h.management.Tasks().Wait(context.Background(), submission.TaskID)
	if err != nil || !found || task.State != management.TaskSucceeded {
		errorText := "restore failed"
		if err != nil {
			errorText = err.Error()
		} else if found && task.Error != "" {
			errorText = task.Error
		}
		c.JSON(http.StatusInternalServerError, gin.H{"status": "failed", "error": errorText})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "stage": RestoreStageComplete})
}
