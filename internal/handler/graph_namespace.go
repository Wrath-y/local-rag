package handler

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Wrath-y/local-rag/internal/graphsnapshot"
	"github.com/Wrath-y/local-rag/internal/store"
	"github.com/gin-gonic/gin"
)

// RegisterNamespaceLifecycle adds the independent versioned deletion task
// resource without extending the original snapshot-build task enum.
func (h *Handler) RegisterNamespaceLifecycle(v1 *gin.RouterGroup) {
	v1.POST("/graphs/:namespace/snapshots/query", h.QueryGraphNamespace)
	v1.POST("/graphs/:namespace/references/sync", h.SyncGraphNamespaceReferences)
	v1.POST("/graphs/:namespace/delete", h.DeleteGraphNamespace)
	v1.GET("/namespace-deletion-tasks/:task_id", h.GetGraphNamespaceDeletion)
	v1.POST("/namespace-deletion-tasks/:task_id/retry", h.RetryGraphNamespaceDeletion)
}
func (h *Handler) withNamespaceStore(c *gin.Context, fn func(*store.Store) error) bool {
	if h.deps.Stores == nil {
		writeGraphError(c, graphsnapshot.NewError(graphsnapshot.CodeGraphStoreUnavailable, nil, nil))
		return false
	}
	err := h.deps.Stores.WithStore(fn)
	if err != nil {
		var public *graphsnapshot.Error
		if errors.As(err, &public) {
			writeGraphError(c, public)
		} else {
			writeGraphLifecycleError(c, err)
		}
		return false
	}
	return true
}
func (h *Handler) namespaceAccessAllowed(c *gin.Context) bool {
	// Existing injected legacy adapters do not advertise the new capability.
	if h.deps.Stores == nil {
		return true
	}
	return h.withNamespaceStore(c, func(st *store.Store) error {
		return st.CheckGraphNamespaceAccess(c.Request.Context(), c.Param("namespace"))
	})
}
func (h *Handler) QueryGraphNamespace(c *gin.Context) {
	var request store.NamespaceQuery
	if !decodeOptionalGraphActivationBody(c, defaultGraphQueryMaxPayloadBytes, &request) {
		return
	}
	var result store.NamespacePage
	if h.withNamespaceStore(c, func(st *store.Store) error {
		var err error
		result, err = st.QueryGraphNamespace(c.Request.Context(), c.Param("namespace"), request)
		return err
	}) {
		c.JSON(http.StatusOK, result)
	}
}
func (h *Handler) SyncGraphNamespaceReferences(c *gin.Context) {
	var body struct {
		Owner            string                    `json:"owner"`
		ExpectedRevision *int64                    `json:"expected_revision"`
		References       []store.SnapshotReference `json:"references"`
	}
	if !decodeOptionalGraphActivationBody(c, defaultGraphQueryMaxPayloadBytes, &body) {
		return
	}
	if body.ExpectedRevision == nil {
		writeGraphError(c, graphsnapshot.NewError(graphsnapshot.CodeInvalidNamespaceRequest, map[string]any{"field": "expected_revision"}, nil))
		return
	}
	request := store.NamespaceReferenceSync{Owner: body.Owner, ExpectedRevision: *body.ExpectedRevision, References: body.References}
	var result store.NamespaceReferenceResult
	if h.withNamespaceStore(c, func(st *store.Store) error {
		var err error
		result, err = st.SyncGraphNamespaceReferences(c.Request.Context(), c.Param("namespace"), request)
		return err
	}) {
		c.JSON(http.StatusOK, result)
	}
}
func (h *Handler) DeleteGraphNamespace(c *gin.Context) {
	var request store.NamespaceDeleteRequest
	if !decodeOptionalGraphActivationBody(c, defaultGraphQueryMaxPayloadBytes, &request) {
		return
	}
	var result store.NamespaceDeletionTask
	if h.withNamespaceStore(c, func(st *store.Store) error {
		var err error
		result, err = st.BeginGraphNamespaceDeletion(c.Request.Context(), c.Param("namespace"), c.GetHeader("Idempotency-Key"), request)
		return err
	}) {
		c.JSON(http.StatusAccepted, result)
	}
}
func (h *Handler) GetGraphNamespaceDeletion(c *gin.Context) {
	var result store.NamespaceDeletionTask
	if h.withNamespaceStore(c, func(st *store.Store) error {
		var err error
		result, err = st.LookupGraphNamespaceDeletion(c.Request.Context(), c.Param("task_id"))
		return err
	}) {
		c.JSON(http.StatusOK, result)
	}
}
func (h *Handler) RetryGraphNamespaceDeletion(c *gin.Context) {
	if !decodeOptionalGraphActivationBody(c, defaultGraphQueryMaxPayloadBytes, &struct{}{}) {
		return
	}
	var result store.NamespaceDeletionTask
	if h.withNamespaceStore(c, func(st *store.Store) error {
		var err error
		result, err = st.RetryGraphNamespaceDeletion(c.Request.Context(), c.Param("task_id"))
		return err
	}) {
		c.JSON(http.StatusAccepted, result)
	}
}
func (h *Handler) startNamespaceCleanup() {
	if h.deps.Stores == nil || h.deps.Stores.Store().GraphUnavailable() != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.namespaceCleanupCancel = cancel
	h.namespaceCleanupWG.Add(1)
	go func() {
		defer h.namespaceCleanupWG.Done()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = h.deps.Stores.WithStore(func(st *store.Store) error { return st.ProcessGraphNamespaceDeletions(ctx) })
			}
		}
	}()
}
