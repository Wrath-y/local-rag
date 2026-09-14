package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wrath-y/local-rag/internal/graphquery"
	"github.com/Wrath-y/local-rag/internal/graphsnapshot"
)

const NamespaceLifecycleCapability = "namespace_lifecycle_v1"

type NamespaceSnapshot struct {
	Version     string `json:"version"`
	ContentHash string `json:"content_hash"`
	Status      string `json:"status"`
	QueryReady  bool   `json:"query_ready"`
}
type SnapshotReference struct {
	Owner       string `json:"owner,omitempty"`
	ReferenceID string `json:"reference_id"`
	Version     string `json:"version"`
	Kind        string `json:"kind"`
}
type NamespacePage struct {
	Namespace          string              `json:"namespace"`
	Items              []NamespaceSnapshot `json:"items"`
	NextCursor         *string             `json:"next_cursor"`
	Generation         string              `json:"generation"`
	ManifestHash       string              `json:"manifest_hash"`
	ActiveVersion      *string             `json:"active_version"`
	References         []SnapshotReference `json:"references"`
	ReferenceRevisions map[string]int64    `json:"reference_revisions"`
	State              string              `json:"state"`
}
type NamespaceQuery struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}
type NamespaceReferenceSync struct {
	Owner            string              `json:"owner"`
	ExpectedRevision int64               `json:"expected_revision"`
	References       []SnapshotReference `json:"references"`
}
type NamespaceReferenceResult struct {
	Owner        string              `json:"owner"`
	Revision     int64               `json:"revision"`
	References   []SnapshotReference `json:"references"`
	Generation   string              `json:"generation"`
	ManifestHash string              `json:"manifest_hash"`
}
type NamespaceDeleteRequest struct {
	ExpectedGeneration string `json:"expected_generation"`
	ManifestHash       string `json:"manifest_hash"`
	Confirm            bool   `json:"confirm"`
}
type NamespaceDeletionTask struct {
	TaskID             string              `json:"task_id"`
	TaskKind           string              `json:"task_kind"`
	Namespace          string              `json:"namespace"`
	State              string              `json:"state"`
	Phase              string              `json:"phase"`
	StatusPath         string              `json:"status_path"`
	Generation         string              `json:"generation"`
	ManifestHash       string              `json:"manifest_hash"`
	Scope              []NamespaceSnapshot `json:"scope"`
	RemainingArtifacts []string            `json:"remaining_artifacts"`
	RetainedArtifacts  []string            `json:"retained_artifacts"`
	Retryable          bool                `json:"retryable"`
	Complete           bool                `json:"complete"`
	Replayed           bool                `json:"replayed"`
	ErrorCode          string              `json:"error_code,omitempty"`
	CreatedAt          string              `json:"created_at"`
	UpdatedAt          string              `json:"updated_at"`
}

type namespaceCursor struct {
	Namespace, Generation, After string
	Limit                        int
}
type namespaceQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func namespaceError(code graphsnapshot.Code, details map[string]any) error {
	return graphsnapshot.NewError(code, details, nil)
}
func (s *Store) namespaceReadLock(namespace string) *sync.RWMutex {
	lock, _ := s.namespaceReadLocks.LoadOrStore(namespace, &sync.RWMutex{})
	return lock.(*sync.RWMutex)
}

// In-flight read views and their request-scoped caches finish before purge.
// The persisted gate rejects every later reader, including after a restart.
func (s *Store) acquireNamespaceRead(ctx context.Context, namespace string) (func(), error) {
	lock := s.namespaceReadLock(namespace)
	lock.RLock()
	if err := s.CheckGraphNamespaceAccess(ctx, namespace); err != nil {
		lock.RUnlock()
		var public *graphsnapshot.Error
		if errors.As(err, &public) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", graphquery.ErrStoreUnavailable, err)
	}
	return lock.RUnlock, nil
}
func (s *Store) CheckGraphNamespaceAccess(ctx context.Context, namespace string) error {
	if err := s.GraphUnavailable(); err != nil {
		return err
	}
	var state string
	err := s.db.QueryRowContext(ctx, `SELECT state FROM graph_namespace_lifecycle WHERE namespace=?`, namespace).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if state != "active" {
		return namespaceError(graphsnapshot.CodeNamespaceDeleting, map[string]any{"namespace": namespace, "state": state})
	}
	return nil
}

func namespaceView(ctx context.Context, q namespaceQuerier, namespace string) (NamespacePage, error) {
	page := NamespacePage{Namespace: namespace, Items: []NamespaceSnapshot{}, References: []SnapshotReference{}, ReferenceRevisions: map[string]int64{}, State: "active"}
	var generation int64
	if err := q.QueryRowContext(ctx, `SELECT generation,state FROM graph_namespace_lifecycle WHERE namespace=?`, namespace).Scan(&generation, &page.State); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return page, err
	}
	page.Generation = strconv.FormatInt(generation, 10)
	var active string
	if err := q.QueryRowContext(ctx, `SELECT active_version FROM graph_namespace_heads WHERE namespace=?`, namespace).Scan(&active); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return page, err
	}
	page.ActiveVersion = stringPointer(active)
	rows, err := q.QueryContext(ctx, `SELECT version,content_hash,status,query_ready FROM graph_snapshots WHERE namespace=? ORDER BY version`, namespace)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		var item NamespaceSnapshot
		if err = rows.Scan(&item.Version, &item.ContentHash, &item.Status, &item.QueryReady); err != nil {
			rows.Close()
			return page, err
		}
		page.Items = append(page.Items, item)
	}
	if err = rows.Close(); err != nil {
		return page, err
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	rows, err = q.QueryContext(ctx, `SELECT owner,revision FROM graph_reference_owners WHERE namespace=? ORDER BY owner`, namespace)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		var owner string
		var revision int64
		if err = rows.Scan(&owner, &revision); err != nil {
			rows.Close()
			return page, err
		}
		page.ReferenceRevisions[owner] = revision
	}
	if err = rows.Close(); err != nil {
		return page, err
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	rows, err = q.QueryContext(ctx, `SELECT owner,reference_id,version,kind FROM graph_snapshot_references WHERE namespace=? ORDER BY owner,reference_id`, namespace)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		var ref SnapshotReference
		if err = rows.Scan(&ref.Owner, &ref.ReferenceID, &ref.Version, &ref.Kind); err != nil {
			rows.Close()
			return page, err
		}
		page.References = append(page.References, ref)
	}
	if err = rows.Close(); err != nil {
		return page, err
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	// Mutable build readiness does not expand the approved identity set.
	identities := make([][2]string, 0, len(page.Items))
	for _, item := range page.Items {
		identities = append(identities, [2]string{item.Version, item.ContentHash})
	}
	payload, _ := json.Marshal(struct {
		Namespace, Generation string
		Active                *string
		Snapshots             [][2]string
		References            []SnapshotReference
		Revisions             map[string]int64
	}{namespace, page.Generation, page.ActiveVersion, identities, page.References, page.ReferenceRevisions})
	sum := sha256.Sum256(payload)
	page.ManifestHash = hex.EncodeToString(sum[:])
	return page, nil
}

func (s *Store) QueryGraphNamespace(ctx context.Context, namespace string, request NamespaceQuery) (NamespacePage, error) {
	if err := s.GraphUnavailable(); err != nil {
		return NamespacePage{}, err
	}
	if namespace == "" || request.Limit < 0 || request.Limit > 100 {
		return NamespacePage{}, namespaceError(graphsnapshot.CodeInvalidNamespaceRequest, nil)
	}
	if request.Limit == 0 {
		request.Limit = 20
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NamespacePage{}, err
	}
	defer tx.Rollback()
	page, err := namespaceView(ctx, tx, namespace)
	if err != nil {
		return page, err
	}
	after := ""
	if request.Cursor != "" {
		raw, e := base64.RawURLEncoding.DecodeString(request.Cursor)
		var cursor namespaceCursor
		if e != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Namespace != namespace || cursor.Limit != request.Limit {
			return page, namespaceError(graphsnapshot.CodeInvalidNamespaceRequest, map[string]any{"field": "cursor"})
		}
		if cursor.Generation != page.Generation {
			return page, namespaceError(graphsnapshot.CodeNamespaceGenerationConflict, map[string]any{"generation": page.Generation})
		}
		after = cursor.After
	}
	start := sort.Search(len(page.Items), func(i int) bool { return page.Items[i].Version > after })
	end := start + request.Limit
	if end < len(page.Items) {
		raw, _ := json.Marshal(namespaceCursor{namespace, page.Generation, page.Items[end-1].Version, request.Limit})
		encoded := base64.RawURLEncoding.EncodeToString(raw)
		page.NextCursor = &encoded
	} else {
		end = len(page.Items)
	}
	page.Items = page.Items[start:end]
	return page, tx.Commit()
}

// BEGIN IMMEDIATE serializes CAS across processes, and every connection used
// for cascades explicitly enables SQLite's connection-local foreign keys.
func (s *Store) namespaceWrite(ctx context.Context, fn func(*sql.Conn) error) error {
	if err := s.GraphUnavailable(); err != nil {
		return err
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	for _, sql := range []string{`PRAGMA foreign_keys=ON`, `PRAGMA busy_timeout=5000`, `BEGIN IMMEDIATE`} {
		if _, err = conn.ExecContext(ctx, sql); err != nil {
			return err
		}
	}
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	if err = fn(conn); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `COMMIT`)
	return err
}
func namespaceRequireActive(ctx context.Context, conn namespaceQuerier, namespace string) error {
	var state string
	err := conn.QueryRowContext(ctx, `SELECT state FROM graph_namespace_lifecycle WHERE namespace=?`, namespace).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if state != "active" {
		return namespaceError(graphsnapshot.CodeNamespaceDeleting, map[string]any{"namespace": namespace, "state": state})
	}
	return nil
}

func (s *Store) SyncGraphNamespaceReferences(ctx context.Context, namespace string, request NamespaceReferenceSync) (NamespaceReferenceResult, error) {
	result := NamespaceReferenceResult{Owner: request.Owner, References: []SnapshotReference{}}
	if namespace == "" || strings.TrimSpace(request.Owner) == "" || len(request.Owner) > 256 || request.ExpectedRevision < 0 || request.References == nil || len(request.References) > 10000 {
		return result, namespaceError(graphsnapshot.CodeInvalidNamespaceRequest, nil)
	}
	ids := map[string]bool{}
	for i, ref := range request.References {
		if ref.ReferenceID == "" || ref.Version == "" || ref.Kind == "" || len(ref.ReferenceID) > 256 || len(ref.Kind) > 128 || ids[ref.ReferenceID] || (ref.Owner != "" && ref.Owner != request.Owner) {
			return result, namespaceError(graphsnapshot.CodeInvalidNamespaceRequest, map[string]any{"field": "references"})
		}
		ids[ref.ReferenceID] = true
		request.References[i].Owner = request.Owner
	}
	sort.Slice(request.References, func(i, j int) bool { return request.References[i].ReferenceID < request.References[j].ReferenceID })
	err := s.namespaceWrite(ctx, func(conn *sql.Conn) error {
		if err := namespaceRequireActive(ctx, conn, namespace); err != nil {
			return err
		}
		var current int64
		err := conn.QueryRowContext(ctx, `SELECT revision FROM graph_reference_owners WHERE namespace=? AND owner=?`, namespace, request.Owner).Scan(&current)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if current != request.ExpectedRevision {
			return namespaceError(graphsnapshot.CodeReferenceRevisionConflict, map[string]any{"owner": request.Owner, "revision": current})
		}
		// Resolve every target before replacing any owner state.
		for _, ref := range request.References {
			var found int
			if err := conn.QueryRowContext(ctx, `SELECT 1 FROM graph_snapshots WHERE namespace=? AND version=?`, namespace, ref.Version).Scan(&found); errors.Is(err, sql.ErrNoRows) {
				return namespaceError(graphsnapshot.CodeSnapshotNotFound, map[string]any{"version": ref.Version})
			} else if err != nil {
				return err
			}
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err = conn.ExecContext(ctx, `INSERT INTO graph_namespaces(namespace,created_at) VALUES(?,?) ON CONFLICT(namespace) DO NOTHING`, namespace, now); err != nil {
			return err
		}
		if _, err = conn.ExecContext(ctx, `INSERT INTO graph_reference_owners(namespace,owner,revision) VALUES(?,?,?) ON CONFLICT(namespace,owner) DO UPDATE SET revision=excluded.revision`, namespace, request.Owner, current+1); err != nil {
			return err
		}
		if _, err = conn.ExecContext(ctx, `DELETE FROM graph_snapshot_references WHERE namespace=? AND owner=?`, namespace, request.Owner); err != nil {
			return err
		}
		for _, ref := range request.References {
			if _, err = conn.ExecContext(ctx, `INSERT INTO graph_snapshot_references(namespace,owner,reference_id,version,kind) VALUES(?,?,?,?,?)`, namespace, request.Owner, ref.ReferenceID, ref.Version, ref.Kind); err != nil {
				return err
			}
		}
		if _, err = conn.ExecContext(ctx, `UPDATE graph_namespace_lifecycle SET generation=generation+1 WHERE namespace=?`, namespace); err != nil {
			return err
		}
		page, err := namespaceView(ctx, conn, namespace)
		if err != nil {
			return err
		}
		result.Revision = current + 1
		result.References = request.References
		result.Generation = page.Generation
		result.ManifestHash = page.ManifestHash
		return nil
	})
	return result, err
}

func (s *Store) BeginGraphNamespaceDeletion(ctx context.Context, namespace, key string, request NamespaceDeleteRequest) (NamespaceDeletionTask, error) {
	var result NamespaceDeletionTask
	if namespace == "" || strings.TrimSpace(key) == "" || len(key) > 128 || !request.Confirm || request.ExpectedGeneration == "" || len(request.ManifestHash) != 64 {
		return result, namespaceError(graphsnapshot.CodeInvalidNamespaceRequest, nil)
	}
	payload, _ := json.Marshal(request)
	sum := sha256.Sum256(payload)
	fingerprint := hex.EncodeToString(sum[:])
	err := s.namespaceWrite(ctx, func(conn *sql.Conn) error {
		var id, existingKey, hash string
		err := conn.QueryRowContext(ctx, `SELECT id,idempotency_key,request_hash FROM graph_namespace_deletion_tasks WHERE namespace=?`, namespace).Scan(&id, &existingKey, &hash)
		if err == nil {
			if existingKey != key || hash != fingerprint {
				return namespaceError(graphsnapshot.CodeIdempotencyConflict, nil)
			}
			result.TaskID = id
			result.Replayed = true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		page, err := namespaceView(ctx, conn, namespace)
		if err != nil {
			return err
		}
		if page.Generation != request.ExpectedGeneration || page.ManifestHash != request.ManifestHash {
			return namespaceError(graphsnapshot.CodeNamespaceGenerationConflict, map[string]any{"generation": page.Generation, "manifest_hash": page.ManifestHash})
		}
		if len(page.References) > 0 {
			return namespaceError(graphsnapshot.CodeSnapshotReferenced, map[string]any{"references": page.References})
		}
		if err = namespaceRequireActive(ctx, conn, namespace); err != nil {
			return err
		}
		random := make([]byte, 16)
		if _, err = rand.Read(random); err != nil {
			return err
		}
		result.TaskID = "namespace-delete-" + hex.EncodeToString(random)
		scope, _ := json.Marshal(page.Items)
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err = conn.ExecContext(ctx, `INSERT INTO graph_namespace_lifecycle(namespace,generation,state) VALUES(?,0,'deleting') ON CONFLICT(namespace) DO UPDATE SET state='deleting'`, namespace); err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `INSERT INTO graph_namespace_deletion_tasks(id,namespace,idempotency_key,request_hash,manifest_hash,generation,scope_json,state,phase,created_at,updated_at) VALUES(?,?,?,?,?,?,?,'pending','draining',?,?)`, result.TaskID, namespace, key, fingerprint, request.ManifestHash, request.ExpectedGeneration, string(scope), now, now)
		return err
	})
	if err != nil {
		return result, err
	}
	replayed := result.Replayed
	result, err = s.LookupGraphNamespaceDeletion(ctx, result.TaskID)
	result.Replayed = replayed
	return result, err
}
func (s *Store) LookupGraphNamespaceDeletion(ctx context.Context, id string) (NamespaceDeletionTask, error) {
	task := NamespaceDeletionTask{TaskKind: "namespace_delete", RemainingArtifacts: []string{}, RetainedArtifacts: []string{"independent_backups", "service_logs", "deletion_receipt"}, Scope: []NamespaceSnapshot{}}
	var scope string
	err := s.db.QueryRowContext(ctx, `SELECT id,namespace,state,phase,generation,manifest_hash,scope_json,error_code,created_at,updated_at FROM graph_namespace_deletion_tasks WHERE id=?`, id).Scan(&task.TaskID, &task.Namespace, &task.State, &task.Phase, &task.Generation, &task.ManifestHash, &scope, &task.ErrorCode, &task.CreatedAt, &task.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return task, namespaceError(graphsnapshot.CodeTaskNotFound, nil)
	}
	if err != nil {
		return task, err
	}
	if err = json.Unmarshal([]byte(scope), &task.Scope); err != nil {
		return task, err
	}
	task.StatusPath = "/v1/namespace-deletion-tasks/" + id
	task.Complete = task.State == "complete"
	task.Retryable = task.State == "failed"
	if !task.Complete {
		task.RemainingArtifacts = []string{"namespace_online_data"}
	}
	return task, nil
}
func (s *Store) RetryGraphNamespaceDeletion(ctx context.Context, id string) (NamespaceDeletionTask, error) {
	_, err := s.db.ExecContext(ctx, `UPDATE graph_namespace_deletion_tasks SET state='pending',error_code='',updated_at=? WHERE id=? AND state='failed'`, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return NamespaceDeletionTask{}, err
	}
	return s.LookupGraphNamespaceDeletion(ctx, id)
}

// ProcessGraphNamespaceDeletions resumes the same receipt after interruption.
// Builds accepted before the gate are allowed to reach terminal state. Each
// namespace purge and its completion receipt commit in one transaction.
func (s *Store) ProcessGraphNamespaceDeletions(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id,namespace FROM graph_namespace_deletion_tasks WHERE state IN ('pending','draining') ORDER BY created_at,id`)
	if err != nil {
		return err
	}
	type item struct{ id, namespace string }
	items := []item{}
	for rows.Next() {
		var it item
		if err = rows.Scan(&it.id, &it.namespace); err != nil {
			rows.Close()
			return err
		}
		items = append(items, it)
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for _, it := range items {
		if err = ctx.Err(); err != nil {
			return err
		}
		// TryLock keeps one slow reader from delaying unrelated namespaces.
		lock := s.namespaceReadLock(it.namespace)
		if !lock.TryLock() {
			continue
		}
		err = s.processGraphNamespaceDeletion(ctx, it.id, it.namespace)
		lock.Unlock()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if _, writeErr := s.db.ExecContext(ctx, `UPDATE graph_namespace_deletion_tasks SET state='failed',error_code='NAMESPACE_CLEANUP_FAILED',updated_at=? WHERE id=? AND state<>'complete'`, time.Now().UTC().Format(time.RFC3339Nano), it.id); writeErr != nil {
				return writeErr
			}
		}
	}
	return nil
}
func (s *Store) processGraphNamespaceDeletion(ctx context.Context, id, namespace string) error {
	return s.namespaceWrite(ctx, func(conn *sql.Conn) error {
		var state string
		if err := conn.QueryRowContext(ctx, `SELECT state FROM graph_namespace_deletion_tasks WHERE id=?`, id).Scan(&state); err != nil {
			return err
		}
		if state == "complete" || state == "failed" {
			return nil
		}
		var writers int
		if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM graph_tasks WHERE namespace=? AND state IN ('queued','running')`, namespace).Scan(&writers); err != nil {
			return err
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if writers > 0 {
			_, err := conn.ExecContext(ctx, `UPDATE graph_namespace_deletion_tasks SET state='draining',phase='draining',updated_at=? WHERE id=?`, now, id)
			return err
		}
		var refs int
		if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM graph_snapshot_references WHERE namespace=?`, namespace).Scan(&refs); err != nil {
			return err
		}
		if refs > 0 {
			return namespaceError(graphsnapshot.CodeSnapshotReferenced, nil)
		}
		if s.graphNamespaceDeleteFailpoint != nil {
			if err := s.graphNamespaceDeleteFailpoint("before_purge"); err != nil {
				return err
			}
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM graph_namespace_heads WHERE namespace=?`, namespace); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM graph_namespaces WHERE namespace=?`, namespace); err != nil {
			return err
		}
		if s.graphNamespaceDeleteFailpoint != nil {
			if err := s.graphNamespaceDeleteFailpoint("after_purge"); err != nil {
				return err
			}
		}
		for _, table := range []string{"graph_snapshots", "graph_nodes", "graph_edges", "graph_snapshot_components", "graph_snapshot_staging", "graph_search_documents", "graph_vector_items", "graph_retrieval_generations", "graph_index_adjacency", "graph_tasks", "graph_namespace_heads", "graph_reference_owners"} {
			var count int
			if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE namespace=?`, namespace).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				return fmt.Errorf("namespace purge left %s rows", table)
			}
		}
		if _, err := conn.ExecContext(ctx, `UPDATE graph_namespace_lifecycle SET state='deleted' WHERE namespace=?`, namespace); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, `UPDATE graph_namespace_deletion_tasks SET state='complete',phase='complete',error_code='',updated_at=? WHERE id=?`, now, id)
		return err
	})
}
