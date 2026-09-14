package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/graphsnapshot"
	"github.com/Wrath-y/local-rag/internal/store"
)

const (
	defaultGraphMaxPayloadBytes = 32 << 20
	defaultGraphMaxNodes        = 10000
	defaultGraphMaxEdges        = 100000
)

// GraphSnapshotService is the write-side dependency of the additive /v1
// graph API. Keeping it narrow makes the handler independently testable and
// does not couple legacy endpoints to graph lifecycle storage.
type GraphSnapshotService interface {
	Put(context.Context, string, string, graphsnapshot.Request) (graphsnapshot.SubmissionCheck, *graphsnapshot.Error)
}

type graphSnapshotRequestCorrelator interface {
	PutWithRequestID(context.Context, string, string, string, graphsnapshot.Request) (graphsnapshot.SubmissionCheck, *graphsnapshot.Error)
}

// GraphTaskReader is the read-side dependency for durable task inspection.
type GraphTaskReader interface {
	LookupGraphTask(context.Context, string) (graphsnapshot.Task, bool, error)
}

// GraphSnapshotLifecycle owns the narrow ready-only activation and guarded
// deletion operations. Its implementation keeps the state checks atomic.
type GraphSnapshotLifecycle interface {
	ActivateGraphSnapshot(context.Context, string, string) (bool, error)
	DeleteGraphSnapshot(context.Context, string, string) error
}

// GraphConditionalActivation is advertised only when the installed lifecycle
// implements both compare-and-swap and consistent head reconciliation.
type GraphConditionalActivation interface {
	ActivateGraphSnapshotConditional(context.Context, string, string, *string) (bool, error)
	LookupActiveGraphSnapshot(context.Context, string) (graphsnapshot.ActiveSnapshot, error)
}

// PutGraphSnapshot accepts an immutable graph snapshot. The durable task is
// keyed by the canonical final content hash, so Idempotency-Key is deliberately
// neither read nor interpreted here.
func (h *Handler) PutGraphSnapshot(c *gin.Context) {
	if !h.namespaceAccessAllowed(c) {
		return
	}
	namespace, version, ok := graphSnapshotIdentity(c)
	if !ok {
		return
	}
	if h.graphService == nil {
		writeGraphError(c, graphsnapshot.NewError(graphsnapshot.CodeGraphStoreUnavailable, nil, nil))
		return
	}

	limits := graphLimits(h.deps.Config)
	if c.Request.ContentLength > int64(limits.maxPayloadBytes) {
		writeGraphError(c, graphLimitError("payload_bytes", limits.maxPayloadBytes))
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, int64(limits.maxPayloadBytes))
	request, err := graphsnapshot.DecodeRequest(c.Request.Body)
	if err != nil {
		writeGraphError(c, graphsnapshot.NewError(graphsnapshot.CodeInvalidSnapshotRequest, map[string]any{"field": "body"}, err))
		return
	}
	if graphErr := validateGraphRequestLimits(request, limits); graphErr != nil {
		writeGraphError(c, graphErr)
		return
	}

	var result graphsnapshot.SubmissionCheck
	var graphErr *graphsnapshot.Error
	if correlated, ok := h.graphService.(graphSnapshotRequestCorrelator); ok {
		result, graphErr = correlated.PutWithRequestID(c.Request.Context(), namespace, version, GraphRequestIDFromContext(c.Request.Context()), request)
	} else {
		result, graphErr = h.graphService.Put(c.Request.Context(), namespace, version, request)
	}
	if graphErr != nil {
		writeGraphError(c, graphErr)
		return
	}
	status := http.StatusAccepted
	if result.Existing && result.Snapshot.Status != graphsnapshot.SnapshotBuilding {
		status = http.StatusOK
	}
	c.JSON(status, result.Snapshot)
}

// GetGraphSnapshot returns the same canonical lifecycle resource used for a
// PUT replay. It cannot inspect staging or unselected derived generations.
func (h *Handler) GetGraphSnapshot(c *gin.Context) {
	if !h.namespaceAccessAllowed(c) {
		return
	}
	namespace, version, ok := graphSnapshotIdentity(c)
	if !ok {
		return
	}
	if h.graphReader == nil {
		writeGraphError(c, graphsnapshot.NewError(graphsnapshot.CodeGraphStoreUnavailable, nil, nil))
		return
	}
	snapshot, found, err := h.graphReader.LookupGraphSnapshot(c.Request.Context(), namespace, version)
	if err != nil {
		writeGraphError(c, graphsnapshot.NewError(graphsnapshot.CodeGraphStoreUnavailable, nil, err))
		return
	}
	if !found {
		writeGraphError(c, graphsnapshot.NewError(graphsnapshot.CodeSnapshotNotFound, map[string]any{"namespace": namespace, "version": version}, nil))
		return
	}
	c.JSON(http.StatusOK, snapshot)
}

// GetGraphTask exposes the durable lifecycle task independently of the
// process-local worker. The same task ID therefore remains useful after a
// restart or when a task is terminal.
func (h *Handler) GetGraphTask(c *gin.Context) {
	if h.graphTaskReader == nil {
		writeGraphError(c, graphsnapshot.NewError(graphsnapshot.CodeGraphStoreUnavailable, nil, nil))
		return
	}
	task, found, err := h.graphTaskReader.LookupGraphTask(c.Request.Context(), c.Param("task_id"))
	if err != nil {
		writeGraphError(c, graphsnapshot.NewError(graphsnapshot.CodeGraphStoreUnavailable, nil, err))
		return
	}
	if !found {
		writeGraphError(c, graphsnapshot.NewError(graphsnapshot.CodeTaskNotFound, map[string]any{"task_id": c.Param("task_id")}, nil))
		return
	}
	c.JSON(http.StatusOK, task)
}

// ActivateGraphSnapshot switches only a ready snapshot into the namespace's
// active pointer. Replays are successful and report changed=false.
func (h *Handler) ActivateGraphSnapshot(c *gin.Context) {
	if !h.namespaceAccessAllowed(c) {
		return
	}
	namespace, version, ok := graphSnapshotIdentity(c)
	if !ok {
		return
	}
	if h.graphLifecycle == nil {
		writeGraphError(c, graphsnapshot.NewError(graphsnapshot.CodeGraphStoreUnavailable, nil, nil))
		return
	}
	var request struct {
		Expected json.RawMessage `json:"expected_active_version"`
	}
	if !decodeOptionalGraphActivationBody(c, graphLimits(h.deps.Config).maxPayloadBytes, &request) {
		return
	}
	var changed bool
	var err error
	if len(request.Expected) > 0 {
		var expected *string
		if err := json.Unmarshal(request.Expected, &expected); err != nil || (expected != nil && *expected == "") {
			writeGraphError(c, graphsnapshot.NewError(graphsnapshot.CodeInvalidSnapshotRequest, map[string]any{"field": "expected_active_version"}, err))
			return
		}
		conditional, supported := h.graphLifecycle.(GraphConditionalActivation)
		if !supported {
			writeGraphError(c, graphsnapshot.NewError(graphsnapshot.CodeGraphStoreUnavailable, map[string]any{"capability": graphsnapshot.ConditionalActivationCapability}, nil))
			return
		}
		changed, err = conditional.ActivateGraphSnapshotConditional(c.Request.Context(), namespace, version, expected)
	} else {
		changed, err = h.graphLifecycle.ActivateGraphSnapshot(c.Request.Context(), namespace, version)
	}
	if err != nil {
		writeGraphLifecycleError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"namespace": namespace, "active_version": version, "changed": changed})
}

// QueryActiveGraphSnapshot is a read-only POST that returns an explicit null
// pair when no version has been activated, including an unknown namespace.
func (h *Handler) QueryActiveGraphSnapshot(c *gin.Context) {
	if !h.namespaceAccessAllowed(c) {
		return
	}
	namespace := c.Param("namespace")
	if namespace == "" {
		writeGraphError(c, graphsnapshot.NewError(graphsnapshot.CodeInvalidSnapshotRequest, map[string]any{"field": "namespace"}, nil))
		return
	}
	conditional, supported := h.graphLifecycle.(GraphConditionalActivation)
	if !supported {
		writeGraphError(c, graphsnapshot.NewError(graphsnapshot.CodeGraphStoreUnavailable, map[string]any{"capability": graphsnapshot.ConditionalActivationCapability}, nil))
		return
	}
	if !decodeOptionalGraphActivationBody(c, graphLimits(h.deps.Config).maxPayloadBytes, &struct{}{}) {
		return
	}
	active, err := conditional.LookupActiveGraphSnapshot(c.Request.Context(), namespace)
	if err != nil {
		writeGraphLifecycleError(c, err)
		return
	}
	c.JSON(http.StatusOK, active)
}

func decodeOptionalGraphActivationBody(c *gin.Context, limit int, target any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, int64(limit)))
	if err == nil && len(bytes.TrimSpace(body)) == 0 {
		return true
	}
	if err == nil {
		if bytes.Equal(bytes.TrimSpace(body), []byte("null")) {
			err = errors.New("body must be an object")
		} else {
			err = graphsnapshot.DecodeStrictJSON(body, target)
		}
	}
	if err != nil {
		writeGraphError(c, graphsnapshot.NewError(graphsnapshot.CodeInvalidSnapshotRequest, map[string]any{"field": "body"}, err))
		return false
	}
	return true
}

// DeleteGraphSnapshot deletes an inactive snapshot only after the storage
// layer atomically confirms no durable writer is queued or running.
func (h *Handler) DeleteGraphSnapshot(c *gin.Context) {
	if !h.namespaceAccessAllowed(c) {
		return
	}
	namespace, version, ok := graphSnapshotIdentity(c)
	if !ok {
		return
	}
	if h.graphLifecycle == nil {
		writeGraphError(c, graphsnapshot.NewError(graphsnapshot.CodeGraphStoreUnavailable, nil, nil))
		return
	}
	if err := h.graphLifecycle.DeleteGraphSnapshot(c.Request.Context(), namespace, version); err != nil {
		writeGraphLifecycleError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func writeGraphLifecycleError(c *gin.Context, err error) {
	var public *graphsnapshot.Error
	if errors.As(err, &public) {
		writeGraphError(c, public)
		return
	}
	var conflict *store.GraphActiveVersionConflict
	switch {
	case errors.As(err, &conflict):
		writeGraphError(c, graphsnapshot.NewError(graphsnapshot.CodeActiveVersionConflict, map[string]any{"namespace": conflict.Namespace, "expected_active_version": conflict.Expected, "active_version": conflict.Active}, nil))
	case errors.Is(err, store.ErrGraphSnapshotNotFound):
		writeGraphError(c, graphsnapshot.NewError(graphsnapshot.CodeSnapshotNotFound, nil, nil))
	case errors.Is(err, store.ErrGraphSnapshotNotReady):
		writeGraphError(c, graphsnapshot.NewError(graphsnapshot.CodeSnapshotNotReady, nil, nil))
	case errors.Is(err, store.ErrGraphSnapshotActive):
		writeGraphError(c, graphsnapshot.NewError(graphsnapshot.CodeActiveSnapshotDeleteForbidden, nil, nil))
	case errors.Is(err, store.ErrGraphSnapshotWriting):
		writeGraphError(c, graphsnapshot.NewError(graphsnapshot.CodeSnapshotWriteInProgress, nil, nil))
	case errors.Is(err, store.ErrGraphUnavailable):
		writeGraphError(c, graphsnapshot.NewError(graphsnapshot.CodeGraphStoreUnavailable, nil, err))
	default:
		writeGraphError(c, graphsnapshot.NewError(graphsnapshot.CodeInternalError, nil, err))
	}
}

func graphSnapshotIdentity(c *gin.Context) (string, string, bool) {
	namespace, version := c.Param("namespace"), c.Param("version")
	if namespace == "" || version == "" {
		writeGraphError(c, graphsnapshot.NewError(graphsnapshot.CodeInvalidSnapshotRequest, map[string]any{"field": "namespace_or_version"}, nil))
		return "", "", false
	}
	return namespace, version, true
}

type graphRequestLimits struct {
	maxPayloadBytes int
	maxNodes        int
	maxEdges        int
}

func graphLimits(cfg *config.Config) graphRequestLimits {
	limits := graphRequestLimits{maxPayloadBytes: defaultGraphMaxPayloadBytes, maxNodes: defaultGraphMaxNodes, maxEdges: defaultGraphMaxEdges}
	if cfg == nil {
		return limits
	}
	if cfg.Graph.MaxPayloadBytes > 0 {
		limits.maxPayloadBytes = cfg.Graph.MaxPayloadBytes
	}
	if cfg.Graph.MaxNodes > 0 {
		limits.maxNodes = cfg.Graph.MaxNodes
	}
	if cfg.Graph.MaxEdges > 0 {
		limits.maxEdges = cfg.Graph.MaxEdges
	}
	return limits
}

func validateGraphRequestLimits(request graphsnapshot.Request, limits graphRequestLimits) *graphsnapshot.Error {
	nodes, edges := len(request.Nodes), len(request.Edges)
	if request.Mode == graphsnapshot.ModeDelta {
		nodes = len(request.NodeUpserts) + len(request.NodeDeletes)
		edges = len(request.EdgeUpserts) + len(request.EdgeDeletes)
	}
	if nodes > limits.maxNodes {
		return graphLimitError("nodes", limits.maxNodes)
	}
	if edges > limits.maxEdges {
		return graphLimitError("edges", limits.maxEdges)
	}
	return nil
}

func graphLimitError(field string, limit int) *graphsnapshot.Error {
	return graphsnapshot.NewError(graphsnapshot.CodeInvalidSnapshotRequest, map[string]any{"field": field, "limit": limit}, nil)
}
