package management

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wrath-y/local-rag/internal/chunk"
	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/provider"
	"github.com/Wrath-y/local-rag/internal/store"
)

type Deps struct {
	Config    *config.Config
	Store     *store.Store
	Embedder  provider.EmbedProvider
	Chunker   chunk.Chunker
	Tasks     *TaskManager
	Lifecycle RebuildLifecycle
}

// RebuildLifecycle coordinates a rebuild with a host application's active
// store lifecycle. It deliberately contains no HTTP or handler types.
type RebuildLifecycle interface {
	WithStore(func(*store.Store) error) error
	WithExclusiveStore(func(*store.Store) error) error
	BeginRebuild() error
	EndRebuild()
}

type Service struct {
	config      *config.Config
	store       *store.Store
	embedder    provider.EmbedProvider
	chunker     chunk.Chunker
	tasks       *TaskManager
	lifecycle   RebuildLifecycle
	indexMu     sync.RWMutex
	indexTaskID string
	runtime     runtimeConfig
}

func New(deps Deps) *Service {
	tasks := deps.Tasks
	if tasks == nil {
		tasks = NewTaskManager(100)
	}
	strategy := "fixed"
	if deps.Config != nil && deps.Config.Chunk.Strategy != "" {
		strategy = deps.Config.Chunk.Strategy
	}
	return &Service{config: deps.Config, store: deps.Store, embedder: deps.Embedder, chunker: deps.Chunker, tasks: tasks, lifecycle: deps.Lifecycle, runtime: runtimeConfig{ChunkStrategy: strategy, QueryRewriteStrategy: "expansion"}}
}

func (s *Service) Tasks() *TaskManager { return s.tasks }

func (s *Service) ListSources() ([]store.SourceInfo, error) {
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	return s.store.ListSources()
}

func (s *Service) DeleteSource(source string) (int, error) {
	if strings.TrimSpace(source) == "" {
		return 0, fmt.Errorf("source is required")
	}
	if err := s.requireStore(); err != nil {
		return 0, err
	}
	return s.store.DeleteSource(source)
}

type Submission struct {
	TaskID string    `json:"task_id"`
	Status TaskState `json:"status"`
}

func submission(task Task) Submission { return Submission{TaskID: task.ID, Status: task.State} }
func requireConfirmation(confirm bool) error {
	if !confirm {
		return fmt.Errorf("confirm must be true")
	}
	return nil
}
func (s *Service) requireStore() error {
	if s.store == nil {
		return fmt.Errorf("store is unavailable")
	}
	return nil
}

type UpdateSourceRequest struct {
	Source  string `json:"source"`
	Content string `json:"content"`
	Confirm bool   `json:"confirm"`
}

func (s *Service) UpdateSource(ctx context.Context, request UpdateSourceRequest) (Submission, error) {
	if err := requireConfirmation(request.Confirm); err != nil {
		return Submission{}, err
	}
	if strings.TrimSpace(request.Source) == "" {
		return Submission{}, fmt.Errorf("source is required")
	}
	if strings.TrimSpace(request.Content) == "" {
		return Submission{}, fmt.Errorf("content is required")
	}
	if err := s.requireStore(); err != nil {
		return Submission{}, err
	}
	if s.chunker == nil || s.embedder == nil || s.config == nil {
		return Submission{}, fmt.Errorf("source update is unavailable: chunker or embedder is not configured")
	}
	task, err := s.tasks.Submit("source_update", func(report *TaskReporter) (map[string]any, error) {
		report.Progress(.05, "removing existing source")
		if _, err := s.store.DeleteSource(request.Source); err != nil {
			return nil, err
		}
		chunks, err := s.chunker.Chunk(request.Content, request.Source)
		if err != nil {
			return nil, fmt.Errorf("chunk content: %w", err)
		}
		if len(chunks) == 0 {
			return map[string]any{"chunks_added": 0}, nil
		}
		texts := make([]string, len(chunks))
		for i, item := range chunks {
			texts[i] = s.config.Embedding.DocPrefix + item.Text
		}
		vectors, err := s.embedder.Embed(ctx, texts)
		if err != nil {
			return nil, fmt.Errorf("embed content: %w", err)
		}
		if len(vectors) != len(chunks) {
			return nil, fmt.Errorf("embedder returned %d vectors for %d chunks", len(vectors), len(chunks))
		}
		added := 0
		for i, item := range chunks {
			id, err := s.store.InsertChunk(item.Text, item.Source, item.MD5, item.ParentText, item.ParentID, vectors[i])
			if err != nil {
				return nil, err
			}
			if id != 0 {
				added++
			}
			report.Progress(.1+.9*float64(i+1)/float64(len(chunks)), "ingesting replacement source")
		}
		return map[string]any{"source": request.Source, "chunks_added": added}, nil
	})
	if err != nil {
		return Submission{}, err
	}
	return submission(task), nil
}

type ResetRequest struct {
	Confirm bool `json:"confirm"`
}

func (s *Service) Reset(request ResetRequest) (Submission, error) {
	if err := requireConfirmation(request.Confirm); err != nil {
		return Submission{}, err
	}
	if err := s.requireStore(); err != nil {
		return Submission{}, err
	}
	task, err := s.tasks.Submit("reset", func(report *TaskReporter) (map[string]any, error) {
		report.Progress(.1, "clearing knowledge base")
		if err := s.store.Reset(); err != nil {
			return nil, err
		}
		return map[string]any{"status": "ok"}, nil
	})
	if err != nil {
		return Submission{}, err
	}
	return submission(task), nil
}

type ArchiveRequest struct {
	Path string `json:"path"`
}
type Artifact struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256,omitempty"`
}

func (s *Service) Export(request ArchiveRequest) (Artifact, error) {
	if err := s.requireStore(); err != nil {
		return Artifact{}, err
	}
	if s.config == nil {
		return Artifact{}, fmt.Errorf("configuration is unavailable")
	}
	path := request.Path
	if path == "" {
		path = filepath.Join(filepath.Dir(s.config.Storage.DBPath), fmt.Sprintf("rag-export-%s.zip", time.Now().UTC().Format("20060102T150405Z")))
	}
	if !isLocalPath(path) {
		return Artifact{}, fmt.Errorf("export path must be a local filesystem path")
	}
	if filepath.Ext(path) != ".zip" {
		return Artifact{}, fmt.Errorf("export path must end in .zip")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Artifact{}, fmt.Errorf("create export directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".rag-export-*.db")
	if err != nil {
		return Artifact{}, err
	}
	snapshot := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(snapshot)
		return Artifact{}, err
	}
	_ = os.Remove(snapshot)
	defer os.Remove(snapshot)
	if err := s.store.Snapshot(snapshot); err != nil {
		return Artifact{}, fmt.Errorf("snapshot database: %w", err)
	}
	chunkCount, err := s.store.ChunkCount()
	if err != nil {
		return Artifact{}, fmt.Errorf("count chunks: %w", err)
	}
	data, err := createArchive(snapshot, s.config, chunkCount)
	if err != nil {
		return Artifact{}, err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return Artifact{}, fmt.Errorf("write export: %w", err)
	}
	sum := sha256.Sum256(data)
	return Artifact{Path: path, SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}, nil
}

type ImportRequest struct {
	Path    string `json:"path"`
	Confirm bool   `json:"confirm"`
}

func (s *Service) Import(request ImportRequest) (Submission, error) {
	return s.restore("import", request.Path, request.Confirm)
}
func (s *Service) BackupRestore(request ImportRequest) (Submission, error) {
	return s.restore("backup_restore", request.Path, request.Confirm)
}
func (s *Service) restore(operation, path string, confirm bool) (Submission, error) {
	if err := requireConfirmation(confirm); err != nil {
		return Submission{}, err
	}
	if err := s.requireStore(); err != nil {
		return Submission{}, err
	}
	if !isLocalPath(path) {
		return Submission{}, fmt.Errorf("archive path must be a local filesystem path")
	}
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return Submission{}, fmt.Errorf("archive file not found or invalid")
	}
	task, err := s.tasks.Submit(operation, func(report *TaskReporter) (map[string]any, error) {
		report.Progress(.05, "validating archive")
		chunks, vectors, err := readArchive(path)
		if err != nil {
			return nil, err
		}
		report.Progress(.3, "replacing knowledge base")
		if err := s.replaceContents(chunks, vectors); err != nil {
			return nil, err
		}
		report.Progress(.9, "checking integrity")
		integrity, err := s.store.IntegrityCheck()
		if err != nil {
			return nil, err
		}
		if integrity != "ok" {
			return nil, fmt.Errorf("integrity check returned %q", integrity)
		}
		return map[string]any{"status": "ok", "chunks": len(chunks)}, nil
	})
	if err != nil {
		return Submission{}, err
	}
	return submission(task), nil
}

type BackupInfo struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	SizeBytes  int64     `json:"size_bytes"`
	ModifiedAt time.Time `json:"modified_at"`
}

func (s *Service) BackupRun() (Submission, error) {
	if err := s.requireStore(); err != nil {
		return Submission{}, err
	}
	if s.config == nil {
		return Submission{}, fmt.Errorf("configuration is unavailable")
	}
	task, err := s.tasks.Submit("backup_run", func(report *TaskReporter) (map[string]any, error) {
		report.Progress(.1, "creating backup")
		dir := filepath.Join(filepath.Dir(s.config.Storage.DBPath), "..", "backups", time.Now().Format("2006-01-02"))
		path := filepath.Join(dir, fmt.Sprintf("rag-%s.zip", time.Now().Format("150405")))
		artifact, err := s.Export(ArchiveRequest{Path: path})
		if err != nil {
			return nil, err
		}
		return map[string]any{"path": artifact.Path, "size_bytes": artifact.SizeBytes}, nil
	})
	if err != nil {
		return Submission{}, err
	}
	return submission(task), nil
}
func (s *Service) BackupList() ([]BackupInfo, error) {
	if s.config == nil {
		return nil, fmt.Errorf("configuration is unavailable")
	}
	root := filepath.Join(filepath.Dir(s.config.Storage.DBPath), "..", "backups")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return []BackupInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	var backups []BackupInfo
	for _, day := range entries {
		if !day.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, day.Name()))
		if err != nil {
			continue
		}
		for _, file := range files {
			if file.IsDir() || filepath.Ext(file.Name()) != ".zip" {
				continue
			}
			info, err := file.Info()
			if err == nil {
				backups = append(backups, BackupInfo{Name: file.Name(), Path: filepath.Join(root, day.Name(), file.Name()), SizeBytes: info.Size(), ModifiedAt: info.ModTime().UTC()})
			}
		}
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].ModifiedAt.After(backups[j].ModifiedAt) })
	return backups, nil
}
func (s *Service) IntegrityCheck() (string, error) {
	if err := s.requireStore(); err != nil {
		return "", err
	}
	return s.store.IntegrityCheck()
}

func (s *Service) IndexRebuild() (Submission, error) {
	if err := s.requireStore(); err != nil {
		return Submission{}, err
	}
	if s.embedder == nil {
		return Submission{}, fmt.Errorf("index rebuild is unavailable: embedding provider is not configured")
	}
	if s.lifecycle != nil {
		if err := s.lifecycle.BeginRebuild(); err != nil {
			return Submission{}, err
		}
	}
	task, err := s.tasks.Submit("index_rebuild", func(report *TaskReporter) (map[string]any, error) {
		if s.lifecycle != nil {
			defer s.lifecycle.EndRebuild()
		}
		snapshot, err := s.withStore(func(st *store.Store) ([]store.ChunkSnapshot, error) { return st.SnapshotChunks() })
		if err != nil {
			return nil, err
		}
		report.ProgressCounts(0, 0, len(snapshot), "preparing vector index rebuild")
		id := fmt.Sprintf("vec_chunks_rebuild_%d", time.Now().UnixNano())
		retired := fmt.Sprintf("vec_chunks_retired_%d", time.Now().UnixNano())
		if err := s.withStoreErr(func(st *store.Store) error { return st.CreateShadowIndex(id) }); err != nil {
			return nil, err
		}
		cleanup := true
		defer func() {
			if cleanup {
				_ = s.withStoreErr(func(st *store.Store) error { return st.DropIndex(id) })
			}
		}()
		for start := 0; start < len(snapshot); start += 32 {
			end := start + 32
			if end > len(snapshot) {
				end = len(snapshot)
			}
			batch := snapshot[start:end]
			texts := make([]string, len(batch))
			for i := range batch {
				texts[i] = batch[i].Text
			}
			vectors, err := s.embedder.Embed(context.Background(), texts)
			if err != nil || len(vectors) != len(batch) {
				if err == nil {
					err = fmt.Errorf("embedding: embedder returned incorrect vector count")
				} else {
					err = fmt.Errorf("embedding: %w", err)
				}
				return nil, err
			}
			if err := s.withStoreErr(func(st *store.Store) error { return st.InsertShadowVectors(id, batch, vectors) }); err != nil {
				return nil, err
			}
			report.ProgressCounts(float64(end)/float64(max(1, len(snapshot))), end, len(snapshot), "rebuilding vector index")
		}
		if err := s.withStoreErr(func(st *store.Store) error { return st.ValidateShadowIndex(id, snapshot) }); err != nil {
			return nil, err
		}
		if err := s.withExclusiveStore(func(st *store.Store) error { return st.ActivateShadowIndex(id, retired) }); err != nil {
			return nil, err
		}
		cleanup = false
		report.ProgressCounts(1, len(snapshot), len(snapshot), "index rebuild complete")
		return map[string]any{"chunks": len(snapshot)}, nil
	})
	if err != nil {
		if s.lifecycle != nil {
			s.lifecycle.EndRebuild()
		}
		return Submission{}, err
	}
	s.indexMu.Lock()
	s.indexTaskID = task.ID
	s.indexMu.Unlock()
	return submission(task), nil
}
func (s *Service) IndexStatus() map[string]any {
	s.indexMu.RLock()
	id := s.indexTaskID
	s.indexMu.RUnlock()
	if id == "" {
		return map[string]any{"state": "available", "task_records_are_process_local": true}
	}
	task, found := s.tasks.Get(id)
	if !found {
		return map[string]any{"state": "unavailable", "task_records_are_process_local": true}
	}
	return map[string]any{"state": task.State, "task_id": task.ID, "progress": task.Progress, "processed": task.Processed, "total": task.Total, "error": task.Error, "task_records_are_process_local": true}
}

func (s *Service) withStore(fn func(*store.Store) ([]store.ChunkSnapshot, error)) ([]store.ChunkSnapshot, error) {
	if s.lifecycle == nil {
		return fn(s.store)
	}
	var result []store.ChunkSnapshot
	err := s.lifecycle.WithStore(func(st *store.Store) error { var inner error; result, inner = fn(st); return inner })
	return result, err
}
func (s *Service) withStoreErr(fn func(*store.Store) error) error {
	if s.lifecycle != nil {
		return s.lifecycle.WithStore(fn)
	}
	return fn(s.store)
}
func (s *Service) withExclusiveStore(fn func(*store.Store) error) error {
	if s.lifecycle != nil {
		return s.lifecycle.WithExclusiveStore(fn)
	}
	return fn(s.store)
}

type RetrievalConfig struct {
	RerankEnabled        bool   `json:"rerank_enabled"`
	VerboseEnabled       bool   `json:"verbose_enabled"`
	DynamicTopKEnabled   bool   `json:"dynamic_top_k_enabled"`
	QueryRewriteEnabled  bool   `json:"query_rewrite_enabled"`
	QueryRewriteStrategy string `json:"query_rewrite_strategy"`
	ChunkStrategy        string `json:"chunk_strategy"`
	Scope                string `json:"scope"`
}
type RetrievalPatch struct {
	RerankEnabled        *bool   `json:"rerank_enabled,omitempty"`
	VerboseEnabled       *bool   `json:"verbose_enabled,omitempty"`
	DynamicTopKEnabled   *bool   `json:"dynamic_top_k_enabled,omitempty"`
	QueryRewriteEnabled  *bool   `json:"query_rewrite_enabled,omitempty"`
	QueryRewriteStrategy *string `json:"query_rewrite_strategy,omitempty"`
	ChunkStrategy        *string `json:"chunk_strategy,omitempty"`
}
type runtimeConfig struct {
	mu                                                                     sync.RWMutex
	RerankEnabled, VerboseEnabled, DynamicTopKEnabled, QueryRewriteEnabled bool
	QueryRewriteStrategy, ChunkStrategy                                    string
}

func (s *Service) RetrievalConfig() RetrievalConfig {
	s.runtime.mu.RLock()
	defer s.runtime.mu.RUnlock()
	return RetrievalConfig{RerankEnabled: s.runtime.RerankEnabled, VerboseEnabled: s.runtime.VerboseEnabled, DynamicTopKEnabled: s.runtime.DynamicTopKEnabled, QueryRewriteEnabled: s.runtime.QueryRewriteEnabled, QueryRewriteStrategy: s.runtime.QueryRewriteStrategy, ChunkStrategy: s.runtime.ChunkStrategy, Scope: "runtime-only"}
}
func (s *Service) SetRetrievalConfig(patch RetrievalPatch) (RetrievalConfig, error) {
	s.runtime.mu.Lock()
	defer s.runtime.mu.Unlock()
	next := RetrievalConfig{
		RerankEnabled:        s.runtime.RerankEnabled,
		VerboseEnabled:       s.runtime.VerboseEnabled,
		DynamicTopKEnabled:   s.runtime.DynamicTopKEnabled,
		QueryRewriteEnabled:  s.runtime.QueryRewriteEnabled,
		QueryRewriteStrategy: s.runtime.QueryRewriteStrategy,
		ChunkStrategy:        s.runtime.ChunkStrategy,
	}
	if patch.QueryRewriteStrategy != nil && !validRewriteStrategy(*patch.QueryRewriteStrategy) {
		return RetrievalConfig{}, fmt.Errorf("unknown query rewrite strategy: %s", *patch.QueryRewriteStrategy)
	}
	if patch.ChunkStrategy != nil && !validChunkStrategy(*patch.ChunkStrategy) {
		return RetrievalConfig{}, fmt.Errorf("unknown chunk strategy: %s", *patch.ChunkStrategy)
	}
	if patch.RerankEnabled != nil {
		next.RerankEnabled = *patch.RerankEnabled
	}
	if patch.VerboseEnabled != nil {
		next.VerboseEnabled = *patch.VerboseEnabled
	}
	if patch.DynamicTopKEnabled != nil {
		next.DynamicTopKEnabled = *patch.DynamicTopKEnabled
	}
	if patch.QueryRewriteEnabled != nil {
		next.QueryRewriteEnabled = *patch.QueryRewriteEnabled
	}
	if patch.QueryRewriteStrategy != nil {
		next.QueryRewriteStrategy = *patch.QueryRewriteStrategy
	}
	if patch.ChunkStrategy != nil {
		next.ChunkStrategy = *patch.ChunkStrategy
	}
	s.runtime.RerankEnabled = next.RerankEnabled
	s.runtime.VerboseEnabled = next.VerboseEnabled
	s.runtime.DynamicTopKEnabled = next.DynamicTopKEnabled
	s.runtime.QueryRewriteEnabled = next.QueryRewriteEnabled
	s.runtime.QueryRewriteStrategy = next.QueryRewriteStrategy
	s.runtime.ChunkStrategy = next.ChunkStrategy
	next.Scope = "runtime-only"
	return next, nil
}
func validChunkStrategy(value string) bool {
	switch value {
	case "fixed", "structure", "semantic", "agentic", "hierarchical":
		return true
	}
	return false
}
func validRewriteStrategy(value string) bool {
	switch value {
	case "expansion", "none":
		return true
	}
	return false
}
func isLocalPath(path string) bool {
	return path != "" && !strings.Contains(path, "://") && filepath.IsAbs(path)
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

type archivedChunk struct {
	ID                                      int64
	Text, Source, MD5, ParentText, ParentID sql.NullString
	Title, URI, Location                    sql.NullString
	CreatedAt                               string
}
type archivedVector struct {
	ID        int64
	Embedding []byte
}

func createArchive(dbPath string, cfg *config.Config, chunkCount int) ([]byte, error) {
	data, err := os.ReadFile(dbPath)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	manifest, err := json.Marshal(map[string]any{
		"format_version": 1,
		"schema_version": 1,
		"created_at":     time.Now().UTC().Format(time.RFC3339),
		"chunk_count":    chunkCount,
		"embedding":      map[string]any{"provider": cfg.Embedding.Provider, "model": cfg.Embedding.Model, "dimensions": cfg.Embedding.Dims},
		"files":          []map[string]any{{"path": "rag.db", "size_bytes": len(data), "sha256": hex.EncodeToString(sum[:])}},
	})
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, entry := range []struct {
		name string
		data []byte
	}{{"rag.db", data}, {"manifest.json", manifest}} {
		f, err := writer.Create(entry.name)
		if err != nil {
			return nil, err
		}
		if _, err = f.Write(entry.data); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
func readArchive(path string) ([]archivedChunk, []archivedVector, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open archive: %w", err)
	}
	defer reader.Close()
	var dbFile *zip.File
	for _, file := range reader.File {
		if file.Name == "rag.db" {
			dbFile = file
		}
	}
	if dbFile == nil {
		return nil, nil, fmt.Errorf("archive does not contain rag.db")
	}
	temp, err := os.CreateTemp("", ".rag-import-*.db")
	if err != nil {
		return nil, nil, err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	body, err := dbFile.Open()
	if err != nil {
		temp.Close()
		return nil, nil, err
	}
	_, copyErr := io.Copy(temp, io.LimitReader(body, 512<<20))
	closeErr := body.Close()
	tempCloseErr := temp.Close()
	if copyErr != nil {
		return nil, nil, copyErr
	}
	if closeErr != nil {
		return nil, nil, closeErr
	}
	if tempCloseErr != nil {
		return nil, nil, tempCloseErr
	}
	db, err := sql.Open("sqlite", "file:"+tempPath+"?mode=ro")
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		if err == nil {
			err = fmt.Errorf("archive integrity check returned %q", integrity)
		}
		return nil, nil, err
	}
	rows, err := db.Query("SELECT id,text,source,md5,parent_text,parent_id,document_title,document_uri,location,created_at FROM chunks ORDER BY id")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var chunks []archivedChunk
	for rows.Next() {
		var item archivedChunk
		if err := rows.Scan(&item.ID, &item.Text, &item.Source, &item.MD5, &item.ParentText, &item.ParentID, &item.Title, &item.URI, &item.Location, &item.CreatedAt); err != nil {
			return nil, nil, err
		}
		chunks = append(chunks, item)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	vectorsRows, err := db.Query("SELECT chunk_id,embedding FROM vec_chunks ORDER BY chunk_id")
	if err != nil {
		return nil, nil, err
	}
	defer vectorsRows.Close()
	var vectors []archivedVector
	for vectorsRows.Next() {
		var item archivedVector
		if err := vectorsRows.Scan(&item.ID, &item.Embedding); err != nil {
			return nil, nil, err
		}
		vectors = append(vectors, item)
	}
	return chunks, vectors, vectorsRows.Err()
}
func (s *Service) replaceContents(chunks []archivedChunk, vectors []archivedVector) error {
	tx, err := s.store.DB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM vec_chunks"); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM chunks"); err != nil {
		return err
	}
	for _, item := range chunks {
		if _, err := tx.Exec("INSERT INTO chunks (id,text,source,md5,parent_text,parent_id,document_title,document_uri,location,created_at) VALUES (?,?,?,?,?,?,?,?,?,?)", item.ID, item.Text, item.Source, item.MD5, item.ParentText, item.ParentID, item.Title, item.URI, item.Location, item.CreatedAt); err != nil {
			return err
		}
	}
	for _, item := range vectors {
		if _, err := tx.Exec("INSERT INTO vec_chunks (chunk_id,embedding) VALUES (?,?)", item.ID, item.Embedding); err != nil {
			return err
		}
	}
	return tx.Commit()
}
