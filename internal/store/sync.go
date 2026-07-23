package store

// The incremental-sync persistence model intentionally lives beside the legacy
// chunk store.  The new tables are additive: databases created by earlier
// releases retain readable chunks and acquire a baseline only after an
// explicit source sync succeeds.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type SyncError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *SyncError) Error() string       { return e.Code + ": " + e.Message }
func syncErr(code, message string) error { return &SyncError{Code: code, Message: message} }
func SyncErrorCode(err error) string {
	var e *SyncError
	if errors.As(err, &e) {
		return e.Code
	}
	return "internal"
}

const (
	SyncQueued    = "queued"
	SyncRunning   = "running"
	SyncSucceeded = "succeeded"
	SyncFailed    = "failed"
	SyncCancelled = "cancelled"
)

type SyncIdentity struct {
	Canonicalization string `json:"canonicalization"`
	Chunker          string `json:"chunker"`
	EmbeddingModel   string `json:"embedding_model"`
}
type SyncDocument struct {
	ID      string      `json:"id"`
	Content string      `json:"content"`
	Title   string      `json:"title,omitempty"`
	URI     string      `json:"uri,omitempty"`
	Chunks  []SyncChunk `json:"chunks,omitempty"`
}

// A caller may provide stable chunk keys. Otherwise the orchestrator derives
// deterministic ordinal keys after chunking the document.
type SyncChunk struct {
	Key        string `json:"key"`
	Content    string `json:"content"`
	ParentText string `json:"parent_text,omitempty"`
	ParentID   string `json:"parent_id,omitempty"`
	Title      string `json:"title,omitempty"`
	URI        string `json:"uri,omitempty"`
	Location   string `json:"location,omitempty"`
}
type SyncSnapshot struct {
	Source    string         `json:"source"`
	Documents []SyncDocument `json:"documents"`
	Identity  SyncIdentity   `json:"identity"`
}

type DiffCounts struct {
	Unchanged int `json:"unchanged"`
	Added     int `json:"added"`
	Changed   int `json:"changed"`
	Deleted   int `json:"deleted"`
}
type SyncReport struct {
	TaskID         string       `json:"task_id"`
	Source         string       `json:"source"`
	BaseRevision   int64        `json:"base_revision"`
	ResultRevision int64        `json:"result_revision,omitempty"`
	Documents      DiffCounts   `json:"documents"`
	Chunks         DiffCounts   `json:"chunks"`
	ReusedChunks   int          `json:"reused_chunks"`
	EmbeddedChunks int          `json:"embedded_chunks"`
	DeletedChunks  int          `json:"deleted_chunks"`
	Identity       SyncIdentity `json:"identity"`
	StartedAt      time.Time    `json:"started_at"`
	FinishedAt     time.Time    `json:"finished_at"`
	RootTaskID     string       `json:"root_task_id"`
	Attempt        int          `json:"attempt"`
	Error          *SyncError   `json:"error,omitempty"`
}
type SyncTask struct {
	ID                    string     `json:"id"`
	Source                string     `json:"source"`
	State                 string     `json:"state"`
	RequestedBaseRevision int64      `json:"requested_base_revision"`
	CommittedRevision     int64      `json:"committed_revision,omitempty"`
	Attempt               int        `json:"attempt"`
	RootTaskID            string     `json:"root_task_id"`
	ParentTaskID          string     `json:"parent_task_id,omitempty"`
	IdempotencyKey        string     `json:"idempotency_key,omitempty"`
	SnapshotHash          string     `json:"snapshot_hash"`
	CreatedAt             time.Time  `json:"created_at"`
	StartedAt             *time.Time `json:"started_at,omitempty"`
	FinishedAt            *time.Time `json:"finished_at,omitempty"`
	Error                 *SyncError `json:"error,omitempty"`
}
type SyncBaseline struct {
	Source    string       `json:"source"`
	Revision  int64        `json:"revision"`
	Documents int          `json:"documents"`
	Chunks    int          `json:"chunks"`
	Identity  SyncIdentity `json:"identity"`
}
type SyncDiff struct {
	Documents  DiffCounts
	Chunks     DiffCounts
	ReusedKeys map[string]bool
	EmbedKeys  map[string]bool
	DeleteKeys map[string]bool
}

func CanonicalizeSyncText(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
}
func SHA256(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
func ChunkKey(source, documentID, key string) string {
	return SHA256(source + "\x00" + documentID + "\x00" + key)
}

func ensureSyncSchema(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS sync_sources (source TEXT PRIMARY KEY, revision INTEGER NOT NULL DEFAULT 0, canonicalization TEXT NOT NULL, chunker_identity TEXT NOT NULL, embedding_identity TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS sync_documents (source TEXT NOT NULL, document_id TEXT NOT NULL, content_hash TEXT NOT NULL, revision INTEGER NOT NULL, PRIMARY KEY(source, document_id), FOREIGN KEY(source) REFERENCES sync_sources(source) ON DELETE CASCADE);
CREATE TABLE IF NOT EXISTS sync_chunks (source TEXT NOT NULL, document_id TEXT NOT NULL, chunk_key TEXT NOT NULL, content_hash TEXT NOT NULL, document_revision INTEGER NOT NULL, revision INTEGER NOT NULL, chunk_id INTEGER NOT NULL, PRIMARY KEY(source, chunk_key), FOREIGN KEY(source, document_id) REFERENCES sync_documents(source, document_id) ON DELETE CASCADE);
CREATE TABLE IF NOT EXISTS sync_tasks (id TEXT PRIMARY KEY, source TEXT NOT NULL, snapshot_json TEXT NOT NULL, snapshot_hash TEXT NOT NULL, state TEXT NOT NULL, requested_base_revision INTEGER NOT NULL, committed_revision INTEGER NOT NULL DEFAULT 0, attempt INTEGER NOT NULL, root_task_id TEXT NOT NULL, parent_task_id TEXT, idempotency_key TEXT, created_at TEXT NOT NULL, started_at TEXT, finished_at TEXT, error_code TEXT, error_message TEXT, report_json TEXT);
CREATE TABLE IF NOT EXISTS sync_staging (task_id TEXT NOT NULL, source TEXT NOT NULL, document_id TEXT NOT NULL, chunk_key TEXT NOT NULL, content_hash TEXT NOT NULL, text TEXT NOT NULL, parent_text TEXT, parent_id TEXT, title TEXT, uri TEXT, location TEXT, embedding BLOB NOT NULL, PRIMARY KEY(task_id, chunk_key));
CREATE TABLE IF NOT EXISTS sync_leases (source TEXT PRIMARY KEY, task_id TEXT NOT NULL, acquired_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS sync_idempotency (source TEXT NOT NULL, idempotency_key TEXT NOT NULL, snapshot_hash TEXT NOT NULL, task_id TEXT NOT NULL, PRIMARY KEY(source, idempotency_key));
CREATE INDEX IF NOT EXISTS idx_sync_tasks_source_state ON sync_tasks(source, state, created_at);
CREATE INDEX IF NOT EXISTS idx_sync_chunks_source_document ON sync_chunks(source, document_id);
CREATE INDEX IF NOT EXISTS idx_sync_staging_task ON sync_staging(task_id);
`)
	return err
}

func nowSync() time.Time          { return time.Now().UTC().Round(0) }
func syncTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
func parseSyncTime(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s.String)
	if err != nil {
		return nil
	}
	return &t
}

func validateSnapshot(snapshot *SyncSnapshot) error {
	snapshot.Source = strings.TrimSpace(snapshot.Source)
	if snapshot.Source == "" {
		return syncErr("validation", "source is required")
	}
	if len(snapshot.Documents) == 0 {
		return syncErr("validation", "at least one document is required")
	}
	seen := map[string]bool{}
	for i := range snapshot.Documents {
		d := &snapshot.Documents[i]
		d.ID = strings.TrimSpace(d.ID)
		d.Content = CanonicalizeSyncText(d.Content)
		if d.ID == "" {
			return syncErr("validation", "document id is required")
		}
		if seen[d.ID] {
			return syncErr("validation", "duplicate document id: "+d.ID)
		}
		seen[d.ID] = true
		if d.Content == "" && len(d.Chunks) == 0 {
			return syncErr("validation", "document content is required")
		}
		keys := map[string]bool{}
		for j := range d.Chunks {
			c := &d.Chunks[j]
			c.Key = strings.TrimSpace(c.Key)
			c.Content = CanonicalizeSyncText(c.Content)
			if c.Key == "" || c.Content == "" {
				return syncErr("validation", "chunk key and content are required")
			}
			if keys[c.Key] {
				return syncErr("validation", "duplicate chunk key in document "+d.ID)
			}
			keys[c.Key] = true
		}
	}
	if snapshot.Identity.Canonicalization == "" {
		snapshot.Identity.Canonicalization = "utf8-trim-crlf-v1"
	}
	if snapshot.Identity.Chunker == "" {
		snapshot.Identity.Chunker = "caller-chunks-v1"
	}
	if snapshot.Identity.EmbeddingModel == "" {
		snapshot.Identity.EmbeddingModel = "default"
	}
	return nil
}
func SnapshotHash(snapshot SyncSnapshot) (string, error) {
	if err := validateSnapshot(&snapshot); err != nil {
		return "", err
	}
	b, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	return SHA256(string(b)), nil
}

func (s *Store) SubmitSync(snapshot SyncSnapshot, idempotencyKey string, maxBytes int) (SyncTask, bool, error) {
	if err := validateSnapshot(&snapshot); err != nil {
		return SyncTask{}, false, err
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return SyncTask{}, false, err
	}
	if maxBytes > 0 && len(payload) > maxBytes {
		return SyncTask{}, false, syncErr("validation", "snapshot exceeds configured size limit")
	}
	hash := SHA256(string(payload))
	now := nowSync()
	task := SyncTask{ID: fmt.Sprintf("sync_%x", sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", snapshot.Source, hash, now.UnixNano()))))[:16], Source: snapshot.Source, State: SyncQueued, Attempt: 1, RootTaskID: "", IdempotencyKey: idempotencyKey, SnapshotHash: hash, CreatedAt: now}
	tx, err := s.db.Begin()
	if err != nil {
		return SyncTask{}, false, err
	}
	defer tx.Rollback()
	if idempotencyKey != "" {
		var existingID, existingHash string
		err := tx.QueryRow(`SELECT task_id, snapshot_hash FROM sync_idempotency WHERE source=? AND idempotency_key=?`, snapshot.Source, idempotencyKey).Scan(&existingID, &existingHash)
		if err == nil {
			if existingHash != hash {
				return SyncTask{}, false, syncErr("idempotency_conflict", "idempotency key belongs to a different snapshot")
			}
			existing, err := scanTask(tx.QueryRow(`SELECT id,source,state,requested_base_revision,committed_revision,attempt,root_task_id,COALESCE(parent_task_id,''),COALESCE(idempotency_key,''),snapshot_hash,created_at,started_at,finished_at,COALESCE(error_code,''),COALESCE(error_message,'') FROM sync_tasks WHERE id=?`, existingID))
			return existing, true, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return SyncTask{}, false, err
		}
	}
	var active string
	err = tx.QueryRow(`SELECT id FROM sync_tasks WHERE source=? AND state IN ('queued','running') LIMIT 1`, snapshot.Source).Scan(&active)
	if err == nil {
		return SyncTask{}, false, syncErr("active_task_conflict", active)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return SyncTask{}, false, err
	}
	_ = tx.QueryRow(`SELECT revision FROM sync_sources WHERE source=?`, snapshot.Source).Scan(&task.RequestedBaseRevision)
	task.RootTaskID = task.ID
	_, err = tx.Exec(`INSERT INTO sync_tasks(id,source,snapshot_json,snapshot_hash,state,requested_base_revision,attempt,root_task_id,idempotency_key,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, task.ID, task.Source, string(payload), hash, task.State, task.RequestedBaseRevision, task.Attempt, task.RootTaskID, nullString(idempotencyKey), syncTime(now))
	if err != nil {
		return SyncTask{}, false, err
	}
	if idempotencyKey != "" {
		if _, err := tx.Exec(`INSERT INTO sync_idempotency(source,idempotency_key,snapshot_hash,task_id) VALUES(?,?,?,?)`, snapshot.Source, idempotencyKey, hash, task.ID); err != nil {
			return SyncTask{}, false, err
		}
	}
	return task, false, tx.Commit()
}
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func scanTask(row *sql.Row) (SyncTask, error) {
	var task SyncTask
	var created sql.NullString
	var started, finished sql.NullString
	var code, msg string
	err := row.Scan(&task.ID, &task.Source, &task.State, &task.RequestedBaseRevision, &task.CommittedRevision, &task.Attempt, &task.RootTaskID, &task.ParentTaskID, &task.IdempotencyKey, &task.SnapshotHash, &created, &started, &finished, &code, &msg)
	if err != nil {
		return task, err
	}
	if created.Valid {
		task.CreatedAt, _ = time.Parse(time.RFC3339Nano, created.String)
	}
	task.StartedAt = parseSyncTime(started)
	task.FinishedAt = parseSyncTime(finished)
	if code != "" {
		task.Error = &SyncError{Code: code, Message: msg}
	}
	return task, nil
}
func (s *Store) GetSyncTask(source, id string) (SyncTask, error) {
	task, err := scanTask(s.db.QueryRow(`SELECT id,source,state,requested_base_revision,committed_revision,attempt,root_task_id,COALESCE(parent_task_id,''),COALESCE(idempotency_key,''),snapshot_hash,created_at,started_at,finished_at,COALESCE(error_code,''),COALESCE(error_message,'') FROM sync_tasks WHERE id=? AND source=?`, id, source))
	if errors.Is(err, sql.ErrNoRows) {
		return task, syncErr("not_found", "sync task not found")
	}
	return task, err
}
func (s *Store) GetSyncReport(source, id string) (SyncReport, error) {
	var payload string
	err := s.db.QueryRow(`SELECT report_json FROM sync_tasks WHERE id=? AND source=?`, id, source).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return SyncReport{}, syncErr("not_found", "sync task not found")
	}
	if err != nil {
		return SyncReport{}, err
	}
	if payload == "" {
		task, e := s.GetSyncTask(source, id)
		if e != nil {
			return SyncReport{}, e
		}
		return SyncReport{TaskID: task.ID, Source: task.Source, BaseRevision: task.RequestedBaseRevision, Attempt: task.Attempt, RootTaskID: task.RootTaskID, Error: task.Error}, nil
	}
	var report SyncReport
	err = json.Unmarshal([]byte(payload), &report)
	return report, err
}
func (s *Store) GetSyncBaseline(source string) (SyncBaseline, error) {
	var b SyncBaseline
	err := s.db.QueryRow(`SELECT source,revision,canonicalization,chunker_identity,embedding_identity FROM sync_sources WHERE source=?`, source).Scan(&b.Source, &b.Revision, &b.Identity.Canonicalization, &b.Identity.Chunker, &b.Identity.EmbeddingModel)
	if errors.Is(err, sql.ErrNoRows) {
		return b, syncErr("not_found", "sync baseline not found")
	}
	if err != nil {
		return b, err
	}
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM sync_documents WHERE source=?`, source).Scan(&b.Documents)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM sync_chunks WHERE source=?`, source).Scan(&b.Chunks)
	return b, nil
}

// ClaimNextSyncTask durably claims a queued task and survives a process crash:
// RecoverSyncTasks turns interrupted running work into an explicit retryable failure.
func (s *Store) ClaimNextSyncTask() (SyncTask, SyncSnapshot, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return SyncTask{}, SyncSnapshot{}, false, err
	}
	defer tx.Rollback()
	var id, payload string
	err = tx.QueryRow(`SELECT id,snapshot_json FROM sync_tasks WHERE state='queued' ORDER BY created_at LIMIT 1`).Scan(&id, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return SyncTask{}, SyncSnapshot{}, false, nil
	}
	if err != nil {
		return SyncTask{}, SyncSnapshot{}, false, err
	}
	task, err := scanTask(tx.QueryRow(`SELECT id,source,state,requested_base_revision,committed_revision,attempt,root_task_id,COALESCE(parent_task_id,''),COALESCE(idempotency_key,''),snapshot_hash,created_at,started_at,finished_at,COALESCE(error_code,''),COALESCE(error_message,'') FROM sync_tasks WHERE id=?`, id))
	if err != nil {
		return task, SyncSnapshot{}, false, err
	}
	now := nowSync()
	result, err := tx.Exec(`UPDATE sync_tasks SET state='running',started_at=? WHERE id=? AND state='queued'`, syncTime(now), id)
	if err != nil {
		return task, SyncSnapshot{}, false, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return SyncTask{}, SyncSnapshot{}, false, nil
	}
	if _, err = tx.Exec(`INSERT INTO sync_leases(source,task_id,acquired_at) VALUES(?,?,?) ON CONFLICT(source) DO UPDATE SET task_id=excluded.task_id,acquired_at=excluded.acquired_at`, task.Source, id, syncTime(now)); err != nil {
		return task, SyncSnapshot{}, false, err
	}
	var snapshot SyncSnapshot
	if err = json.Unmarshal([]byte(payload), &snapshot); err != nil {
		return task, SyncSnapshot{}, false, err
	}
	task.State = SyncRunning
	task.StartedAt = &now
	return task, snapshot, true, tx.Commit()
}

func (s *Store) RecoverSyncTasks() error {
	// A queued record has never mutated the baseline and is safe to retain.
	// A running record has no resumable provider request, so make failure explicit.
	now := nowSync()
	rows, err := s.db.Query(`SELECT id,source,requested_base_revision,attempt,root_task_id,started_at FROM sync_tasks WHERE state='running'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, source, root string
		var base int64
		var attempt int
		var started sql.NullString
		if err := rows.Scan(&id, &source, &base, &attempt, &root, &started); err != nil {
			return err
		}
		report := SyncReport{TaskID: id, Source: source, BaseRevision: base, Attempt: attempt, RootTaskID: root, FinishedAt: now, Error: &SyncError{Code: "interrupted", Message: "server restarted before sync promotion"}}
		if started.Valid {
			report.StartedAt, _ = time.Parse(time.RFC3339Nano, started.String)
		}
		b, _ := json.Marshal(report)
		if _, err := s.db.Exec(`UPDATE sync_tasks SET state='failed',finished_at=?,error_code='interrupted',error_message='server restarted before sync promotion',report_json=? WHERE id=? AND state='running'`, syncTime(now), string(b), id); err != nil {
			return err
		}
	}
	return s.CleanupSyncStaging(24)
}

// CleanupSyncStaging removes only staging data associated with terminal tasks
// older than the retention window. Baselines and reports are never affected.
func (s *Store) CleanupSyncStaging(retentionHours int) error {
	if retentionHours < 1 {
		retentionHours = 24
	}
	cutoff := syncTime(nowSync().Add(-time.Duration(retentionHours) * time.Hour))
	_, err := s.db.Exec(`DELETE FROM sync_staging WHERE task_id IN (SELECT id FROM sync_tasks WHERE state IN ('failed','cancelled','succeeded') AND finished_at < ?)`, cutoff)
	return err
}

// PrepareSyncDiff validates the ready-to-embed candidate and compares it to
// the currently committed source map. Chunks must have stable source-scoped
// keys before this method is called.
func (s *Store) PrepareSyncDiff(snapshot SyncSnapshot) (SyncDiff, error) {
	if err := validateSnapshot(&snapshot); err != nil {
		return SyncDiff{}, err
	}
	baseline, err := s.GetSyncBaseline(snapshot.Source)
	if err != nil && SyncErrorCode(err) != "not_found" {
		return SyncDiff{}, err
	}
	identityChanged := err == nil && baseline.Identity != snapshot.Identity
	type oldDoc struct {
		hash     string
		revision int64
	}
	docs := map[string]oldDoc{}
	rows, err := s.db.Query(`SELECT document_id,content_hash,revision FROM sync_documents WHERE source=?`, snapshot.Source)
	if err != nil {
		return SyncDiff{}, err
	}
	for rows.Next() {
		var id, h string
		var r int64
		if err := rows.Scan(&id, &h, &r); err != nil {
			rows.Close()
			return SyncDiff{}, err
		}
		docs[id] = oldDoc{h, r}
	}
	rows.Close()
	type oldChunk struct{ hash, doc string }
	chunks := map[string]oldChunk{}
	rows, err = s.db.Query(`SELECT chunk_key,content_hash,document_id FROM sync_chunks WHERE source=?`, snapshot.Source)
	if err != nil {
		return SyncDiff{}, err
	}
	for rows.Next() {
		var k, h, d string
		if err := rows.Scan(&k, &h, &d); err != nil {
			rows.Close()
			return SyncDiff{}, err
		}
		chunks[k] = oldChunk{h, d}
	}
	rows.Close()
	diff := SyncDiff{ReusedKeys: map[string]bool{}, EmbedKeys: map[string]bool{}, DeleteKeys: map[string]bool{}}
	seenDocs := map[string]bool{}
	seenChunks := map[string]bool{}
	for _, d := range snapshot.Documents {
		seenDocs[d.ID] = true
		old, exists := docs[d.ID]
		h := SHA256(d.Content)
		if !exists {
			diff.Documents.Added++
		} else if identityChanged || old.hash != h {
			diff.Documents.Changed++
		} else {
			diff.Documents.Unchanged++
		}
		for _, c := range d.Chunks {
			k := ChunkKey(snapshot.Source, d.ID, c.Key)
			seenChunks[k] = true
			prior, ok := chunks[k]
			ch := SHA256(c.Content)
			if ok && prior.hash == ch && !identityChanged {
				diff.Chunks.Unchanged++
				diff.ReusedKeys[k] = true
			} else if ok {
				diff.Chunks.Changed++
				diff.EmbedKeys[k] = true
			} else {
				diff.Chunks.Added++
				diff.EmbedKeys[k] = true
			}
		}
	}
	for id := range docs {
		if !seenDocs[id] {
			diff.Documents.Deleted++
		}
	}
	for k := range chunks {
		if !seenChunks[k] {
			diff.Chunks.Deleted++
			diff.DeleteKeys[k] = true
		}
	}
	return diff, nil
}

// StageSyncEmbedding persists only new vectors. Retrieval never joins this
// table, so a failed task cannot leak partially prepared candidate content.
func (s *Store) StageSyncEmbedding(taskID, source, documentID string, c SyncChunk, embedding []float32) error {
	if taskID == "" || source == "" || documentID == "" || c.Key == "" {
		return syncErr("validation", "invalid staging record")
	}
	_, err := s.db.Exec(`INSERT INTO sync_staging(task_id,source,document_id,chunk_key,content_hash,text,parent_text,parent_id,title,uri,location,embedding) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(task_id,chunk_key) DO UPDATE SET content_hash=excluded.content_hash,text=excluded.text,parent_text=excluded.parent_text,parent_id=excluded.parent_id,title=excluded.title,uri=excluded.uri,location=excluded.location,embedding=excluded.embedding`, taskID, source, documentID, ChunkKey(source, documentID, c.Key), SHA256(c.Content), c.Content, c.ParentText, c.ParentID, c.Title, c.URI, c.Location, Float32ToBytes(embedding))
	return err
}

func (s *Store) DiscardSyncStaging(taskID string) error {
	_, err := s.db.Exec(`DELETE FROM sync_staging WHERE task_id=?`, taskID)
	return err
}

// PromoteSync atomically installs vectors, reconciliation deletes, baseline
// metadata, task success and the immutable report. Any error rolls back all of
// those effects, preserving the previous retrievable source.
func (s *Store) PromoteSync(task SyncTask, snapshot SyncSnapshot, diff SyncDiff) (SyncReport, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return SyncReport{}, err
	}
	defer tx.Rollback()
	var state string
	var current int64
	err = tx.QueryRow(`SELECT state,requested_base_revision FROM sync_tasks WHERE id=?`, task.ID).Scan(&state, &current)
	if err != nil {
		return SyncReport{}, err
	}
	if state != "running" {
		return SyncReport{}, syncErr("conflict", "task is not running")
	}
	var actual int64
	_ = tx.QueryRow(`SELECT revision FROM sync_sources WHERE source=?`, task.Source).Scan(&actual)
	if actual != current {
		return SyncReport{}, syncErr("revision_conflict", "source baseline changed")
	}
	// First sync replaces legacy rows for this source only; identities are not inferred.
	if actual == 0 {
		if err := deleteSourceTx(tx, task.Source); err != nil {
			return SyncReport{}, err
		}
	}
	// Remove deleted and changed existing chunk rows before installing staged replacements.
	for key := range diff.DeleteKeys {
		if err := deleteSyncChunkTx(tx, task.Source, key); err != nil {
			return SyncReport{}, err
		}
	}
	for key := range diff.EmbedKeys {
		if err := deleteSyncChunkTx(tx, task.Source, key); err != nil {
			return SyncReport{}, err
		}
	}
	// Capture existing metadata prior to replacing source baseline tables.
	oldDocRev := map[string]int64{}
	rows, err := tx.Query(`SELECT document_id,revision FROM sync_documents WHERE source=?`, task.Source)
	if err != nil {
		return SyncReport{}, err
	}
	for rows.Next() {
		var id string
		var r int64
		if err := rows.Scan(&id, &r); err != nil {
			rows.Close()
			return SyncReport{}, err
		}
		oldDocRev[id] = r
	}
	rows.Close()
	oldChunkRev := map[string]int64{}
	oldChunkID := map[string]int64{}
	rows, err = tx.Query(`SELECT chunk_key,revision,chunk_id FROM sync_chunks WHERE source=?`, task.Source)
	if err != nil {
		return SyncReport{}, err
	}
	for rows.Next() {
		var k string
		var r, id int64
		if err := rows.Scan(&k, &r, &id); err != nil {
			rows.Close()
			return SyncReport{}, err
		}
		oldChunkRev[k] = r
		oldChunkID[k] = id
	}
	rows.Close()
	if _, err = tx.Exec(`DELETE FROM sync_chunks WHERE source=?`, task.Source); err != nil {
		return SyncReport{}, err
	}
	if _, err = tx.Exec(`DELETE FROM sync_documents WHERE source=?`, task.Source); err != nil {
		return SyncReport{}, err
	}
	newRevision := actual + 1
	if _, err = tx.Exec(`INSERT INTO sync_sources(source,revision,canonicalization,chunker_identity,embedding_identity,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(source) DO UPDATE SET revision=excluded.revision,canonicalization=excluded.canonicalization,chunker_identity=excluded.chunker_identity,embedding_identity=excluded.embedding_identity,updated_at=excluded.updated_at`, task.Source, newRevision, snapshot.Identity.Canonicalization, snapshot.Identity.Chunker, snapshot.Identity.EmbeddingModel, syncTime(nowSync())); err != nil {
		return SyncReport{}, err
	}
	for _, d := range snapshot.Documents {
		dr := oldDocRev[d.ID] + 1
		if oldDocRev[d.ID] == 0 {
			dr = 1
		}
		if _, err = tx.Exec(`INSERT INTO sync_documents(source,document_id,content_hash,revision) VALUES(?,?,?,?)`, task.Source, d.ID, SHA256(d.Content), dr); err != nil {
			return SyncReport{}, err
		}
		for _, c := range d.Chunks {
			key := ChunkKey(task.Source, d.ID, c.Key)
			var chunkID int64
			if diff.ReusedKeys[key] {
				chunkID = oldChunkID[key]
				if chunkID == 0 {
					return SyncReport{}, syncErr("internal", "missing retained chunk")
				}
			} else {
				var text, hash string
				var parentText, parentID, title, uri, location sql.NullString
				var vec []byte
				err = tx.QueryRow(`SELECT text,content_hash,parent_text,parent_id,title,uri,location,embedding FROM sync_staging WHERE task_id=? AND chunk_key=?`, task.ID, key).Scan(&text, &hash, &parentText, &parentID, &title, &uri, &location, &vec)
				if err != nil {
					return SyncReport{}, fmt.Errorf("staged chunk %s: %w", key, err)
				}
				res, e := tx.Exec(`INSERT INTO chunks(text,source,md5,parent_text,parent_id,document_title,document_uri,location,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, text, task.Source, hash, parentText.String, parentID.String, title.String, uri.String, location.String, syncTime(nowSync()))
				if e != nil {
					return SyncReport{}, e
				}
				chunkID, _ = res.LastInsertId()
				if _, e = tx.Exec(`INSERT INTO vec_chunks(chunk_id,embedding) VALUES(?,?)`, chunkID, vec); e != nil {
					return SyncReport{}, e
				}
			}
			cr := oldChunkRev[key] + 1
			if oldChunkRev[key] == 0 {
				cr = 1
			}
			if _, err = tx.Exec(`INSERT INTO sync_chunks(source,document_id,chunk_key,content_hash,document_revision,revision,chunk_id) VALUES(?,?,?,?,?,?,?)`, task.Source, d.ID, key, SHA256(c.Content), dr, cr, chunkID); err != nil {
				return SyncReport{}, err
			}
		}
	}
	finished := nowSync()
	report := SyncReport{TaskID: task.ID, Source: task.Source, BaseRevision: actual, ResultRevision: newRevision, Documents: diff.Documents, Chunks: diff.Chunks, ReusedChunks: len(diff.ReusedKeys), EmbeddedChunks: len(diff.EmbedKeys), DeletedChunks: len(diff.DeleteKeys), Identity: snapshot.Identity, FinishedAt: finished, RootTaskID: task.RootTaskID, Attempt: task.Attempt}
	if task.StartedAt != nil {
		report.StartedAt = *task.StartedAt
	}
	payload, _ := json.Marshal(report)
	if _, err = tx.Exec(`UPDATE sync_tasks SET state='succeeded',committed_revision=?,finished_at=?,report_json=? WHERE id=?`, newRevision, syncTime(finished), string(payload), task.ID); err != nil {
		return SyncReport{}, err
	}
	if _, err = tx.Exec(`DELETE FROM sync_staging WHERE task_id=?`, task.ID); err != nil {
		return SyncReport{}, err
	}
	if _, err = tx.Exec(`DELETE FROM sync_leases WHERE source=? AND task_id=?`, task.Source, task.ID); err != nil {
		return SyncReport{}, err
	}
	return report, tx.Commit()
}
func deleteSourceTx(tx *sql.Tx, source string) error {
	rows, err := tx.Query(`SELECT id FROM chunks WHERE source=?`, source)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return err
		}
		if _, err = tx.Exec(`DELETE FROM vec_chunks WHERE chunk_id=?`, id); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`DELETE FROM chunks WHERE source=?`, source)
	return err
}
func deleteSyncChunkTx(tx *sql.Tx, source, key string) error {
	var id int64
	err := tx.QueryRow(`SELECT chunk_id FROM sync_chunks WHERE source=? AND chunk_key=?`, source, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM vec_chunks WHERE chunk_id=?`, id); err != nil {
		return err
	}
	_, err = tx.Exec(`DELETE FROM chunks WHERE id=?`, id)
	return err
}

func (s *Store) FailSyncTask(task SyncTask, cause error) error {
	_ = s.DiscardSyncStaging(task.ID)
	code := SyncErrorCode(cause)
	now := nowSync()
	report := SyncReport{TaskID: task.ID, Source: task.Source, BaseRevision: task.RequestedBaseRevision, Attempt: task.Attempt, RootTaskID: task.RootTaskID, FinishedAt: now, Error: &SyncError{Code: code, Message: cause.Error()}}
	if task.StartedAt != nil {
		report.StartedAt = *task.StartedAt
	}
	b, _ := json.Marshal(report)
	_, err := s.db.Exec(`UPDATE sync_tasks SET state='failed',finished_at=?,error_code=?,error_message=?,report_json=? WHERE id=? AND state='running'`, syncTime(now), code, cause.Error(), string(b), task.ID)
	if err == nil {
		_, _ = s.db.Exec(`DELETE FROM sync_leases WHERE source=? AND task_id=?`, task.Source, task.ID)
	}
	return err
}
func (s *Store) RetrySyncTask(source, id string, maxAttempts int) (SyncTask, error) {
	old, err := s.GetSyncTask(source, id)
	if err != nil {
		return SyncTask{}, err
	}
	if old.State != SyncFailed && old.State != SyncCancelled {
		return SyncTask{}, syncErr("validation", "only failed or cancelled tasks can be retried")
	}
	if old.Attempt >= maxAttempts {
		return SyncTask{}, syncErr("retry_limit", "maximum attempts reached")
	}
	var payload string
	err = s.db.QueryRow(`SELECT snapshot_json FROM sync_tasks WHERE id=?`, id).Scan(&payload)
	if err != nil {
		return SyncTask{}, err
	}
	now := nowSync()
	digest := sha256.Sum256([]byte(fmt.Sprintf("retry:%s:%d", id, now.UnixNano())))
	task := SyncTask{ID: fmt.Sprintf("sync_%x", digest[:16]), Source: source, State: SyncQueued, RequestedBaseRevision: old.CommittedRevision, Attempt: old.Attempt + 1, RootTaskID: old.RootTaskID, ParentTaskID: old.ID, SnapshotHash: old.SnapshotHash, CreatedAt: now}
	_ = s.db.QueryRow(`SELECT revision FROM sync_sources WHERE source=?`, source).Scan(&task.RequestedBaseRevision)
	_, err = s.db.Exec(`INSERT INTO sync_tasks(id,source,snapshot_json,snapshot_hash,state,requested_base_revision,attempt,root_task_id,parent_task_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, task.ID, source, payload, task.SnapshotHash, task.State, task.RequestedBaseRevision, task.Attempt, task.RootTaskID, task.ParentTaskID, syncTime(now))
	return task, err
}

// Stable ordering helps reports and direct callers produce deterministic work.
func SortSyncSnapshot(snapshot *SyncSnapshot) {
	sort.Slice(snapshot.Documents, func(i, j int) bool { return snapshot.Documents[i].ID < snapshot.Documents[j].ID })
	for i := range snapshot.Documents {
		sort.Slice(snapshot.Documents[i].Chunks, func(a, b int) bool { return snapshot.Documents[i].Chunks[a].Key < snapshot.Documents[i].Chunks[b].Key })
	}
}
