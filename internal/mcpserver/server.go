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
	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/observe"
	"github.com/Wrath-y/local-rag/internal/provider"
	"github.com/Wrath-y/local-rag/internal/store"
)

// Deps mirrors handler.Deps — holds all internal services.
type Deps struct {
	Config   *config.Config
	Store    *store.Store
	Embedder provider.EmbedProvider
	Reranker provider.RerankProvider
	LLM      provider.LLMProvider
	Chunker  chunk.Chunker
}

// Run creates and starts the MCP server over stdio transport.
// Blocks until the client disconnects.
func Run(ctx context.Context, deps Deps) error {
	s := &server{deps: deps}

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

	return mcpServer.Run(ctx, &mcp.StdioTransport{})
}

type server struct {
	deps Deps
}

// --- Input/Output types ---

type IngestInput struct {
	Text   string `json:"text" jsonschema:"description=Text content to ingest into the knowledge base"`
	Source string `json:"source" jsonschema:"description=Source identifier (e.g. filename or URL). Defaults to 'manual'"`
}

type IngestOutput struct {
	Status      string `json:"status"`
	ChunksAdded int    `json:"chunks_added"`
}

type RetrieveInput struct {
	Query string `json:"query" jsonschema:"description=Search query to find relevant documents"`
	TopK  int    `json:"top_k,omitempty" jsonschema:"description=Number of results to return (default: from config)"`
}

type RetrieveOutput struct {
	Chunks []string `json:"chunks"`
}

type ListSourcesInput struct{}

type ListSourcesOutput struct {
	Sources []store.SourceInfo `json:"sources"`
}

type DeleteSourceInput struct {
	Source string `json:"source" jsonschema:"description=Source identifier to delete all chunks from"`
}

type DeleteSourceOutput struct {
	Deleted int `json:"deleted"`
}

type StatusInput struct{}

type StatusOutput struct {
	TotalChunks int `json:"total_chunks"`
}

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
		id, err := s.deps.Store.InsertChunk(ch.Text, ch.Source, ch.MD5, ch.ParentText, ch.ParentID, embeddings[i])
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
		return textResult("No relevant results found."), RetrieveOutput{Chunks: []string{}}, nil
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

	// Format
	var chunks []string
	for _, r := range results {
		displayText := r.Text
		if r.ParentText != "" {
			displayText = r.ParentText
		}
		chunks = append(chunks, fmt.Sprintf("[来源: %s]\n%s", r.Source, strings.TrimSpace(displayText)))
	}

	text := strings.Join(chunks, "\n---\n")
	return textResult(text), RetrieveOutput{Chunks: chunks}, nil
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
