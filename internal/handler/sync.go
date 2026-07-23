package handler

import (
	"errors"
	"net/http"

	"github.com/Wrath-y/local-rag/internal/store"
	"github.com/gin-gonic/gin"
)

type syncSubmitRequest struct {
	Documents []store.SyncDocument `json:"documents"`
	Identity  store.SyncIdentity   `json:"identity"`
}

func (h *Handler) SyncSubmit(c *gin.Context) {
	if h.syncService == nil {
		writeSyncError(c, &store.SyncError{Code: "disabled", Message: "incremental sync is unavailable"})
		return
	}
	var req syncSubmitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeSyncError(c, &store.SyncError{Code: "validation", Message: "provide a valid sync snapshot"})
		return
	}
	source := c.Param("source")
	task, replayed, err := h.syncService.Submit(store.SyncSnapshot{Source: source, Documents: req.Documents, Identity: req.Identity}, c.GetHeader("Idempotency-Key"))
	if err != nil {
		writeSyncError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, taskResource(task, replayed))
}
func (h *Handler) SyncStatus(c *gin.Context) {
	if h.syncService == nil {
		writeSyncError(c, &store.SyncError{Code: "disabled", Message: "incremental sync is unavailable"})
		return
	}
	task, err := h.syncService.Store.GetSyncTask(c.Param("source"), c.Param("task"))
	if err != nil {
		writeSyncError(c, err)
		return
	}
	c.JSON(http.StatusOK, taskResource(task, false))
}
func (h *Handler) SyncReport(c *gin.Context) {
	if h.syncService == nil {
		writeSyncError(c, &store.SyncError{Code: "disabled", Message: "incremental sync is unavailable"})
		return
	}
	report, err := h.syncService.Store.GetSyncReport(c.Param("source"), c.Param("task"))
	if err != nil {
		writeSyncError(c, err)
		return
	}
	c.JSON(http.StatusOK, report)
}
func (h *Handler) SyncRetry(c *gin.Context) {
	if h.syncService == nil {
		writeSyncError(c, &store.SyncError{Code: "disabled", Message: "incremental sync is unavailable"})
		return
	}
	task, err := h.syncService.Retry(c.Param("source"), c.Param("task"))
	if err != nil {
		writeSyncError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, taskResource(task, false))
}
func (h *Handler) SyncBaseline(c *gin.Context) {
	if h.syncService == nil {
		writeSyncError(c, &store.SyncError{Code: "disabled", Message: "incremental sync is unavailable"})
		return
	}
	baseline, err := h.syncService.Store.GetSyncBaseline(c.Param("source"))
	if err != nil {
		writeSyncError(c, err)
		return
	}
	c.JSON(http.StatusOK, baseline)
}
func taskResource(task store.SyncTask, replayed bool) gin.H {
	return gin.H{"task": task, "replayed": replayed, "status_url": "/sources/" + task.Source + "/syncs/" + task.ID, "report_url": "/sources/" + task.Source + "/syncs/" + task.ID + "/report"}
}
func writeSyncError(c *gin.Context, err error) {
	code := store.SyncErrorCode(err)
	status := http.StatusInternalServerError
	switch code {
	case "validation", "disabled":
		status = http.StatusBadRequest
	case "not_found":
		status = http.StatusNotFound
	case "active_task_conflict", "revision_conflict", "idempotency_conflict", "conflict", "retry_limit":
		status = http.StatusConflict
	}
	var e *store.SyncError
	msg := "sync operation failed"
	if errors.As(err, &e) {
		msg = e.Message
	}
	c.JSON(status, gin.H{"code": code, "error": msg})
}
