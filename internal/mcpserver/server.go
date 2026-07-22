// Package mcpserver provides an MCP (Model Context Protocol) server that
// exposes the Local RAG capabilities as tools. It uses the same internal
// services (store, embedder, chunker) directly — no HTTP round-trip.
//
// Usage: rag-server mcp  (runs over stdio)
package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Wrath-y/local-rag/internal/chunk"
	"github.com/Wrath-y/local-rag/internal/citation"
	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/management"
	"github.com/Wrath-y/local-rag/internal/observe"
	"github.com/Wrath-y/local-rag/internal/provider"
	"github.com/Wrath-y/local-rag/internal/store"
)

// Deps mirrors handler.Deps — holds all internal services.
type Deps struct {
	Config     *config.Config
	Store      *store.Store
	Embedder   provider.EmbedProvider
	Reranker   provider.RerankProvider
	LLM        provider.LLMProvider
	Chunker    chunk.Chunker
	Management *management.Service
}

// Run creates and starts the MCP server over stdio transport.
// Blocks until the client disconnects.
func Run(ctx context.Context, deps Deps) error {
	return newMCPServer(deps).Run(ctx, &mcp.StdioTransport{})
}

// newMCPServer constructs the complete registry separately from the stdio
// transport so the discoverable MCP contract can be tested in memory.
func newMCPServer(deps Deps) *mcp.Server {
	s := &server{deps: deps, citations: citation.NewManager(time.Hour)}

	mcpServer := mcp.NewServer(
		&mcp.Implementation{
			Name:    "local-rag",
			Version: "1.0.0",
		},
		nil,
	)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "rag_ingest",
		Description: "Ingest text content into the local knowledge base. The text will be chunked, embedded, and stored for future semantic retrieval.",
	}, s.handleIngest)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "rag_retrieve",
		Description: "Retrieve relevant document chunks from the knowledge base using hybrid vector + keyword search. Returns the most semantically relevant passages for the given query.",
	}, s.handleRetrieve)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "rag_list_sources",
		Description: "List all ingested sources in the knowledge base with their chunk counts.",
	}, s.handleListSources)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "rag_delete_source",
		Description: "Delete all chunks associated with a specific source from the knowledge base.",
	}, s.handleDeleteSource)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "rag_status",
		Description: "Get the status of the RAG knowledge base including total chunk count.",
	}, s.handleStatus)

	mcp.AddTool(mcpServer, &mcp.Tool{Name: "rag_update_source", Description: "Replace all chunks for a source with supplied content. Requires confirm: true and returns an asynchronous task."}, s.handleUpdateSource)
	mcp.AddTool(mcpServer, &mcp.Tool{Name: "rag_reset", Description: "Remove all knowledge-base content. Requires confirm: true and returns an asynchronous task."}, s.handleReset)
	mcp.AddTool(mcpServer, &mcp.Tool{Name: "rag_export", Description: "Create a local zip export and return local artifact metadata. Archive bytes are never streamed over MCP."}, s.handleExport)
	mcp.AddTool(mcpServer, &mcp.Tool{Name: "rag_import", Description: "Replace the knowledge base from a local export archive. Requires confirm: true and returns an asynchronous task."}, s.handleImport)
	mcp.AddTool(mcpServer, &mcp.Tool{Name: "rag_backup_run", Description: "Create a local backup asynchronously and return a task identifier."}, s.handleBackupRun)
	mcp.AddTool(mcpServer, &mcp.Tool{Name: "rag_backup_list", Description: "List local backup artifacts, newest first."}, s.handleBackupList)
	mcp.AddTool(mcpServer, &mcp.Tool{Name: "rag_backup_restore", Description: "Restore a local backup archive. Requires confirm: true and returns an asynchronous task."}, s.handleBackupRestore)
	mcp.AddTool(mcpServer, &mcp.Tool{Name: "rag_storage_integrity_check", Description: "Run SQLite integrity_check and return its result."}, s.handleIntegrityCheck)
	mcp.AddTool(mcpServer, &mcp.Tool{Name: "rag_index_rebuild", Description: "Rebuild vector embeddings asynchronously and return a task identifier."}, s.handleIndexRebuild)
	mcp.AddTool(mcpServer, &mcp.Tool{Name: "rag_index_status", Description: "Return index-rebuild availability and process-local task semantics."}, s.handleIndexStatus)
	mcp.AddTool(mcpServer, &mcp.Tool{Name: "rag_retrieval_config_get", Description: "Get effective supported runtime retrieval configuration."}, s.handleRetrievalConfigGet)
	mcp.AddTool(mcpServer, &mcp.Tool{Name: "rag_retrieval_config_set", Description: "Atomically update supported runtime retrieval configuration fields."}, s.handleRetrievalConfigSet)
	mcp.AddTool(mcpServer, &mcp.Tool{Name: "rag_task_status", Description: "Inspect a process-local asynchronous management task by its opaque identifier."}, s.handleTaskStatus)

	return mcpServer
}

type server struct {
	deps      Deps
	citations *citation.Manager
}

// --- Input/Output types ---

type IngestInput struct {
	Text     string `json:"text" jsonschema:"Text content to ingest into the knowledge base"`
	Source   string `json:"source" jsonschema:"Source identifier (e.g. filename or URL). Defaults to 'manual'"`
	Title    string `json:"title,omitempty" jsonschema:"Optional document title for citations"`
	URI      string `json:"uri,omitempty" jsonschema:"Optional original document URI or filesystem path for citations"`
	Location string `json:"location,omitempty" jsonschema:"Optional document location (for example page or section)"`
}

type IngestOutput struct {
	Status      string `json:"status"`
	ChunksAdded int    `json:"chunks_added"`
}

type RetrieveInput struct {
	Query string `json:"query" jsonschema:"Search query to find relevant documents"`
	TopK  int    `json:"top_k,omitempty" jsonschema:"Number of results to return (default: from config)"`
}

type RetrieveOutput struct {
	Chunks        []string            `json:"chunks"`
	Citations     []citation.Evidence `json:"citations"`
	EvidenceToken string              `json:"evidence_token"`
}

type ListSourcesInput struct{}

type ListSourcesOutput struct {
	Sources []store.SourceInfo `json:"sources"`
}

type DeleteSourceInput struct {
	Source string `json:"source" jsonschema:"Source identifier to delete all chunks from"`
}

type DeleteSourceOutput struct {
	Deleted int `json:"deleted"`
}

type StatusInput struct{}

type StatusOutput struct {
	TotalChunks int `json:"total_chunks"`
}

type UpdateSourceInput struct {
	Source  string `json:"source" jsonschema:"Source identifier to replace"`
	Content string `json:"content" jsonschema:"Replacement text content"`
	Confirm bool   `json:"confirm" jsonschema:"Must be literal true before replacing a source"`
}
type ResetInput struct {
	Confirm bool `json:"confirm" jsonschema:"Must be literal true before clearing all content"`
}
type ArchiveInput struct {
	Path string `json:"path,omitempty" jsonschema:"Optional absolute local .zip destination path"`
}
type ImportInput struct {
	Path    string `json:"path" jsonschema:"Absolute local archive path"`
	Confirm bool   `json:"confirm" jsonschema:"Must be literal true before replacing persisted content"`
}
type BackupRestoreInput = ImportInput
type BackupRunInput struct{}
type BackupListInput struct{}
type IntegrityInput struct{}
type IndexRebuildInput struct{}
type IndexStatusInput struct{}
type RetrievalConfigGetInput struct{}
type RetrievalConfigSetInput struct {
	RerankEnabled        *bool   `json:"rerank_enabled,omitempty" jsonschema:"Enable reranking"`
	VerboseEnabled       *bool   `json:"verbose_enabled,omitempty" jsonschema:"Enable verbose retrieval logging"`
	DynamicTopKEnabled   *bool   `json:"dynamic_top_k_enabled,omitempty" jsonschema:"Enable dynamic top-k"`
	QueryRewriteEnabled  *bool   `json:"query_rewrite_enabled,omitempty" jsonschema:"Enable query rewriting"`
	QueryRewriteStrategy *string `json:"query_rewrite_strategy,omitempty" jsonschema:"Query rewrite strategy: expansion or none"`
	ChunkStrategy        *string `json:"chunk_strategy,omitempty" jsonschema:"Chunk strategy: fixed, structure, semantic, agentic, or hierarchical"`
}
type TaskStatusInput struct {
	TaskID string `json:"task_id" jsonschema:"Opaque task identifier returned by a management tool"`
}
type SubmissionOutput = management.Submission
type ExportOutput = management.Artifact
type BackupListOutput struct {
	Backups []management.BackupInfo `json:"backups"`
}
type IntegrityOutput struct {
	Status string `json:"status"`
	Detail string `json:"detail"`
}
type IndexStatusOutput map[string]any
type RetrievalConfigOutput = management.RetrievalConfig
type TaskStatusOutput = management.Task

// --- Handlers ---

func (s *server) handleIngest(ctx context.Context, req *mcp.CallToolRequest, input IngestInput) (*mcp.CallToolResult, IngestOutput, error) {
	if input.Text == "" {
		return errResult("text is required"), IngestOutput{}, nil
	}
	if input.Source == "" {
		input.Source = "manual"
	}

	// Chunk
	chunks, err := s.deps.Chunker.Chunk(input.Text, input.Source)
	if err != nil {
		return errResult(fmt.Sprintf("chunking failed: %v", err)), IngestOutput{}, nil
	}
	if len(chunks) == 0 {
		return textResult("Content is empty after chunking, nothing to ingest."), IngestOutput{Status: "skip"}, nil
	}

	// Batch embed
	texts := make([]string, len(chunks))
	for i, ch := range chunks {
		prefix := s.deps.Config.Embedding.DocPrefix
		texts[i] = prefix + ch.Text
	}
	embeddings, err := s.deps.Embedder.Embed(ctx, texts)
	if err != nil {
		return errResult(fmt.Sprintf("embedding failed: %v", err)), IngestOutput{}, nil
	}

	// Store
	added := 0
	for i, ch := range chunks {
		uri := ch.URI
		if input.URI != "" {
			uri = input.URI
		}
		if uri == "" {
			uri = ch.Source
		}
		title := ch.Title
		if input.Title != "" {
			title = input.Title
		}
		location := ch.Location
		if input.Location != "" {
			location = input.Location
		}
		if location == "" {
			location = fmt.Sprintf("chunk:%d", i+1)
		}
		id, err := s.deps.Store.InsertChunkWithProvenance(ch.Text, ch.Source, ch.MD5, ch.ParentText, ch.ParentID, store.Provenance{Title: title, URI: uri, Location: location}, embeddings[i])
		if err != nil {
			return errResult(fmt.Sprintf("store error: %v", err)), IngestOutput{}, nil
		}
		if id != 0 {
			added++
		}
	}

	// Metrics
	if added > 0 {
		observe.IngestTotal.WithLabelValues("ok").Inc()
		observe.ChunkTotal.Add(float64(added))
	} else {
		observe.IngestTotal.WithLabelValues("skip").Inc()
	}

	out := IngestOutput{Status: "ok", ChunksAdded: added}
	if added == 0 {
		out.Status = "skip"
		return textResult("Content already exists (duplicate), skipped."), out, nil
	}
	return textResult(fmt.Sprintf("Ingested %d chunks from source %q.", added, input.Source)), out, nil
}

func (s *server) handleRetrieve(ctx context.Context, req *mcp.CallToolRequest, input RetrieveInput) (*mcp.CallToolResult, RetrieveOutput, error) {
	if input.Query == "" {
		return errResult("query is required"), RetrieveOutput{}, nil
	}

	cfg := s.deps.Config
	topK := cfg.Retrieve.TopK
	if input.TopK > 0 {
		topK = input.TopK
	}

	opts := store.RetrieveOpts{
		TopK:                topK,
		CandidateMultiplier: cfg.Retrieve.CandidateMultiplier,
		VectorWeight:        cfg.Retrieve.ScoreWeights.Vector,
		BM25Weight:          cfg.Retrieve.ScoreWeights.BM25,
	}

	// Embed query
	queryText := cfg.Embedding.QueryPrefix + input.Query
	vecs, err := s.deps.Embedder.Embed(ctx, []string{queryText})
	if err != nil {
		return errResult(fmt.Sprintf("embed query failed: %v", err)), RetrieveOutput{}, nil
	}
	if len(vecs) == 0 {
		return errResult("embedder returned no vectors"), RetrieveOutput{}, nil
	}

	// Retrieve
	start := time.Now()
	results, err := s.deps.Store.Retrieve(vecs[0], input.Query, opts)
	if err != nil {
		return errResult(fmt.Sprintf("retrieve failed: %v", err)), RetrieveOutput{}, nil
	}
	observe.RetrieveLatency.Observe(time.Since(start).Seconds())
	observe.RetrieveTotal.WithLabelValues(boolStr(len(results) > 0)).Inc()

	if len(results) == 0 {
		manifest := s.citations.Create(nil)
		return textResult("No relevant results found."), RetrieveOutput{Chunks: []string{}, Citations: []citation.Evidence{}, EvidenceToken: manifest.Token}, nil
	}

	// Optional rerank
	if s.deps.Reranker != nil && len(results) > 1 {
		docs := make([]string, len(results))
		for i, r := range results {
			docs[i] = r.Text
		}
		topN := cfg.Retrieve.RerankCandidates
		if topN <= 0 || topN > len(docs) {
			topN = len(docs)
		}
		reranked, err := s.deps.Reranker.Rerank(ctx, input.Query, docs, topN)
		if err == nil && len(reranked) > 0 {
			reordered := make([]store.RetrieveResult, 0, len(reranked))
			for _, rr := range reranked {
				if rr.Index >= 0 && rr.Index < len(results) {
					reordered = append(reordered, results[rr.Index])
				}
			}
			if len(reordered) > 0 {
				results = reordered
			}
		}
	}

	manifest := s.citations.Create(citation.EvidenceFromResults(results))
	chunks := citation.RenderChunks(manifest.Citations)
	text := strings.Join(chunks, "\n---\n")
	return textResult(text), RetrieveOutput{Chunks: chunks, Citations: manifest.Citations, EvidenceToken: manifest.Token}, nil
}

func (s *server) handleListSources(ctx context.Context, req *mcp.CallToolRequest, input ListSourcesInput) (*mcp.CallToolResult, ListSourcesOutput, error) {
	sources, err := s.deps.Store.ListSources()
	if err != nil {
		return errResult(fmt.Sprintf("failed: %v", err)), ListSourcesOutput{}, nil
	}
	if len(sources) == 0 {
		return textResult("No sources ingested yet."), ListSourcesOutput{}, nil
	}

	var lines []string
	for _, src := range sources {
		lines = append(lines, fmt.Sprintf("- %s: %d chunks", src.Source, src.Count))
	}
	return textResult(strings.Join(lines, "\n")), ListSourcesOutput{Sources: sources}, nil
}

func (s *server) handleDeleteSource(ctx context.Context, req *mcp.CallToolRequest, input DeleteSourceInput) (*mcp.CallToolResult, DeleteSourceOutput, error) {
	if input.Source == "" {
		return errResult("source is required"), DeleteSourceOutput{}, nil
	}

	deleted, err := s.deps.Store.DeleteSource(input.Source)
	if err != nil {
		return errResult(fmt.Sprintf("delete failed: %v", err)), DeleteSourceOutput{}, nil
	}

	msg := fmt.Sprintf("Deleted %d chunks from source %q.", deleted, input.Source)
	return textResult(msg), DeleteSourceOutput{Deleted: deleted}, nil
}

func (s *server) handleStatus(ctx context.Context, req *mcp.CallToolRequest, input StatusInput) (*mcp.CallToolResult, StatusOutput, error) {
	count, err := s.deps.Store.ChunkCount()
	if err != nil {
		return errResult(fmt.Sprintf("failed: %v", err)), StatusOutput{}, nil
	}

	text := fmt.Sprintf("RAG Status: OK | Total chunks: %d", count)
	return textResult(text), StatusOutput{TotalChunks: count}, nil
}

func (s *server) management() (*management.Service, error) {
	if s.deps.Management == nil {
		return nil, fmt.Errorf("management service is unavailable")
	}
	return s.deps.Management, nil
}

func (s *server) handleUpdateSource(ctx context.Context, req *mcp.CallToolRequest, input UpdateSourceInput) (*mcp.CallToolResult, SubmissionOutput, error) {
	service, err := s.management()
	if err != nil {
		return errResult(err.Error()), SubmissionOutput{}, nil
	}
	result, err := service.UpdateSource(ctx, management.UpdateSourceRequest{Source: input.Source, Content: input.Content, Confirm: input.Confirm})
	if err != nil {
		return errResult(err.Error()), SubmissionOutput{}, nil
	}
	return textResult("Source update queued. Poll rag_task_status with the returned task_id."), result, nil
}
func (s *server) handleReset(ctx context.Context, req *mcp.CallToolRequest, input ResetInput) (*mcp.CallToolResult, SubmissionOutput, error) {
	service, err := s.management()
	if err != nil {
		return errResult(err.Error()), SubmissionOutput{}, nil
	}
	result, err := service.Reset(management.ResetRequest{Confirm: input.Confirm})
	if err != nil {
		return errResult(err.Error()), SubmissionOutput{}, nil
	}
	return textResult("Reset queued. Poll rag_task_status with the returned task_id."), result, nil
}
func (s *server) handleExport(ctx context.Context, req *mcp.CallToolRequest, input ArchiveInput) (*mcp.CallToolResult, ExportOutput, error) {
	service, err := s.management()
	if err != nil {
		return errResult(err.Error()), ExportOutput{}, nil
	}
	result, err := service.Export(management.ArchiveRequest{Path: input.Path})
	if err != nil {
		return errResult(err.Error()), ExportOutput{}, nil
	}
	return textResult("Export created at " + result.Path), result, nil
}
func (s *server) handleImport(ctx context.Context, req *mcp.CallToolRequest, input ImportInput) (*mcp.CallToolResult, SubmissionOutput, error) {
	service, err := s.management()
	if err != nil {
		return errResult(err.Error()), SubmissionOutput{}, nil
	}
	result, err := service.Import(management.ImportRequest{Path: input.Path, Confirm: input.Confirm})
	if err != nil {
		return errResult(err.Error()), SubmissionOutput{}, nil
	}
	return textResult("Import queued. Poll rag_task_status with the returned task_id."), result, nil
}
func (s *server) handleBackupRun(ctx context.Context, req *mcp.CallToolRequest, input BackupRunInput) (*mcp.CallToolResult, SubmissionOutput, error) {
	service, err := s.management()
	if err != nil {
		return errResult(err.Error()), SubmissionOutput{}, nil
	}
	result, err := service.BackupRun()
	if err != nil {
		return errResult(err.Error()), SubmissionOutput{}, nil
	}
	return textResult("Backup queued. Poll rag_task_status with the returned task_id."), result, nil
}
func (s *server) handleBackupList(ctx context.Context, req *mcp.CallToolRequest, input BackupListInput) (*mcp.CallToolResult, BackupListOutput, error) {
	service, err := s.management()
	if err != nil {
		return errResult(err.Error()), BackupListOutput{}, nil
	}
	backups, err := service.BackupList()
	if err != nil {
		return errResult(err.Error()), BackupListOutput{}, nil
	}
	return textResult(fmt.Sprintf("Found %d backup(s).", len(backups))), BackupListOutput{Backups: backups}, nil
}
func (s *server) handleBackupRestore(ctx context.Context, req *mcp.CallToolRequest, input BackupRestoreInput) (*mcp.CallToolResult, SubmissionOutput, error) {
	service, err := s.management()
	if err != nil {
		return errResult(err.Error()), SubmissionOutput{}, nil
	}
	result, err := service.BackupRestore(management.ImportRequest{Path: input.Path, Confirm: input.Confirm})
	if err != nil {
		return errResult(err.Error()), SubmissionOutput{}, nil
	}
	return textResult("Backup restore queued. Poll rag_task_status with the returned task_id."), result, nil
}
func (s *server) handleIntegrityCheck(ctx context.Context, req *mcp.CallToolRequest, input IntegrityInput) (*mcp.CallToolResult, IntegrityOutput, error) {
	service, err := s.management()
	if err != nil {
		return errResult(err.Error()), IntegrityOutput{}, nil
	}
	detail, err := service.IntegrityCheck()
	if err != nil {
		return errResult(err.Error()), IntegrityOutput{}, nil
	}
	status := "ok"
	if detail != "ok" {
		status = "error"
	}
	return textResult("Integrity check: " + detail), IntegrityOutput{Status: status, Detail: detail}, nil
}
func (s *server) handleIndexRebuild(ctx context.Context, req *mcp.CallToolRequest, input IndexRebuildInput) (*mcp.CallToolResult, SubmissionOutput, error) {
	service, err := s.management()
	if err != nil {
		return errResult(err.Error()), SubmissionOutput{}, nil
	}
	result, err := service.IndexRebuild()
	if err != nil {
		return errResult(err.Error()), SubmissionOutput{}, nil
	}
	return textResult("Index rebuild queued. Poll rag_task_status with the returned task_id."), result, nil
}
func (s *server) handleIndexStatus(ctx context.Context, req *mcp.CallToolRequest, input IndexStatusInput) (*mcp.CallToolResult, IndexStatusOutput, error) {
	service, err := s.management()
	if err != nil {
		return errResult(err.Error()), IndexStatusOutput{}, nil
	}
	status := IndexStatusOutput(service.IndexStatus())
	return textResult(fmt.Sprintf("Index status: %v", status["state"])), status, nil
}
func (s *server) handleRetrievalConfigGet(ctx context.Context, req *mcp.CallToolRequest, input RetrievalConfigGetInput) (*mcp.CallToolResult, RetrievalConfigOutput, error) {
	service, err := s.management()
	if err != nil {
		return errResult(err.Error()), RetrievalConfigOutput{}, nil
	}
	result := service.RetrievalConfig()
	return textResult("Returned effective runtime retrieval configuration."), result, nil
}
func (s *server) handleRetrievalConfigSet(ctx context.Context, req *mcp.CallToolRequest, input RetrievalConfigSetInput) (*mcp.CallToolResult, RetrievalConfigOutput, error) {
	service, err := s.management()
	if err != nil {
		return errResult(err.Error()), RetrievalConfigOutput{}, nil
	}
	result, err := service.SetRetrievalConfig(management.RetrievalPatch{RerankEnabled: input.RerankEnabled, VerboseEnabled: input.VerboseEnabled, DynamicTopKEnabled: input.DynamicTopKEnabled, QueryRewriteEnabled: input.QueryRewriteEnabled, QueryRewriteStrategy: input.QueryRewriteStrategy, ChunkStrategy: input.ChunkStrategy})
	if err != nil {
		return errResult(err.Error()), RetrievalConfigOutput{}, nil
	}
	return textResult("Updated effective runtime retrieval configuration."), result, nil
}
func (s *server) handleTaskStatus(ctx context.Context, req *mcp.CallToolRequest, input TaskStatusInput) (*mcp.CallToolResult, TaskStatusOutput, error) {
	if input.TaskID == "" {
		return errResult("task_id is required"), TaskStatusOutput{}, nil
	}
	service, err := s.management()
	if err != nil {
		return errResult(err.Error()), TaskStatusOutput{}, nil
	}
	task, found := service.Tasks().Get(input.TaskID)
	if !found {
		return errResult("task is unavailable"), TaskStatusOutput{}, nil
	}
	return textResult(fmt.Sprintf("Task %s is %s.", task.ID, task.State)), task, nil
}

// --- Helpers ---

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}

func errResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
