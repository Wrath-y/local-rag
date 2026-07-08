// Package main implements an MCP (Model Context Protocol) server that exposes
// the Local RAG service as tools. Other agents (Claude Code, Cursor, etc.) can
// connect to this server via stdio transport to ingest, retrieve, and manage
// the local knowledge base.
//
// Usage:
//
//	go build -o rag-mcp-server ./examples/mcp/
//	# Then configure in your agent's MCP settings (see README.md)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// baseURL is the RAG service endpoint. Override with RAG_BASE_URL env var.
var baseURL = "http://127.0.0.1:8765"

func init() {
	if u := os.Getenv("RAG_BASE_URL"); u != "" {
		baseURL = u
	}
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

// --- Tool Input/Output Types ---

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
	TopK  int    `json:"top_k,omitempty" jsonschema:"description=Number of results to return (default: 3)"`
}

type RetrieveOutput struct {
	Chunks []string `json:"chunks"`
}

type ListSourcesInput struct{}

type ListSourcesOutput struct {
	Sources []SourceInfo `json:"sources"`
}

type SourceInfo struct {
	Source string `json:"source"`
	Count  int    `json:"count"`
}

type DeleteSourceInput struct {
	Source string `json:"source" jsonschema:"description=Source identifier to delete all chunks from"`
}

type DeleteSourceOutput struct {
	Message string `json:"message"`
	Deleted int    `json:"deleted"`
}

type StatusInput struct{}

type StatusOutput struct {
	Status      string `json:"status"`
	TotalChunks int    `json:"total_chunks"`
}

// --- Tool Handlers ---

func handleIngest(ctx context.Context, req *mcp.CallToolRequest, input IngestInput) (*mcp.CallToolResult, IngestOutput, error) {
	if input.Text == "" {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "text is required"}}}, IngestOutput{}, nil
	}
	if input.Source == "" {
		input.Source = "manual"
	}

	body, _ := json.Marshal(map[string]string{"text": input.Text, "source": input.Source})
	resp, err := httpClient.Post(baseURL+"/ingest", "application/json", bytes.NewReader(body))
	if err != nil {
		return errorResult(fmt.Sprintf("RAG service unreachable: %v", err)), IngestOutput{}, nil
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	chunksAdded := 0
	if v, ok := result["chunks_added"].(float64); ok {
		chunksAdded = int(v)
	}
	status := "ok"
	if s, ok := result["status"].(string); ok {
		status = s
	}

	out := IngestOutput{Status: status, ChunksAdded: chunksAdded}
	text := fmt.Sprintf("Ingested %d chunks from source %q", chunksAdded, input.Source)
	if status == "skip" {
		text = "Content already exists (duplicate), skipped"
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, out, nil
}

func handleRetrieve(ctx context.Context, req *mcp.CallToolRequest, input RetrieveInput) (*mcp.CallToolResult, RetrieveOutput, error) {
	if input.Query == "" {
		return errorResult("query is required"), RetrieveOutput{}, nil
	}

	payload := map[string]interface{}{"text": input.Query}
	if input.TopK > 0 {
		payload["top_k"] = input.TopK
	}
	body, _ := json.Marshal(payload)

	resp, err := httpClient.Post(baseURL+"/retrieve", "application/json", bytes.NewReader(body))
	if err != nil {
		return errorResult(fmt.Sprintf("RAG service unreachable: %v", err)), RetrieveOutput{}, nil
	}
	defer resp.Body.Close()

	var result struct {
		Chunks []string `json:"chunks"`
	}
	json.NewDecoder(resp.Body).Decode(&result)

	if len(result.Chunks) == 0 {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "No relevant results found."}}}, RetrieveOutput{Chunks: []string{}}, nil
	}

	text := strings.Join(result.Chunks, "\n---\n")
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, RetrieveOutput{Chunks: result.Chunks}, nil
}

func handleListSources(ctx context.Context, req *mcp.CallToolRequest, input ListSourcesInput) (*mcp.CallToolResult, ListSourcesOutput, error) {
	resp, err := httpClient.Get(baseURL + "/sources")
	if err != nil {
		return errorResult(fmt.Sprintf("RAG service unreachable: %v", err)), ListSourcesOutput{}, nil
	}
	defer resp.Body.Close()

	var result struct {
		Sources []SourceInfo `json:"sources"`
	}
	json.NewDecoder(resp.Body).Decode(&result)

	if len(result.Sources) == 0 {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "No sources ingested yet."}}}, ListSourcesOutput{}, nil
	}

	var lines []string
	for _, s := range result.Sources {
		lines = append(lines, fmt.Sprintf("- %s: %d chunks", s.Source, s.Count))
	}
	text := strings.Join(lines, "\n")
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, ListSourcesOutput{Sources: result.Sources}, nil
}

func handleDeleteSource(ctx context.Context, req *mcp.CallToolRequest, input DeleteSourceInput) (*mcp.CallToolResult, DeleteSourceOutput, error) {
	if input.Source == "" {
		return errorResult("source is required"), DeleteSourceOutput{}, nil
	}

	reqHTTP, _ := http.NewRequestWithContext(ctx, "DELETE", baseURL+"/source?source="+input.Source, nil)
	resp, err := httpClient.Do(reqHTTP)
	if err != nil {
		return errorResult(fmt.Sprintf("RAG service unreachable: %v", err)), DeleteSourceOutput{}, nil
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	deleted := 0
	if v, ok := result["deleted"].(float64); ok {
		deleted = int(v)
	}
	msg := fmt.Sprintf("Deleted %d chunks from source %q", deleted, input.Source)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: msg}}}, DeleteSourceOutput{Message: msg, Deleted: deleted}, nil
}

func handleStatus(ctx context.Context, req *mcp.CallToolRequest, input StatusInput) (*mcp.CallToolResult, StatusOutput, error) {
	// Get health
	healthResp, err := httpClient.Get(baseURL + "/health")
	if err != nil {
		return errorResult(fmt.Sprintf("RAG service unreachable: %v", err)), StatusOutput{}, nil
	}
	defer healthResp.Body.Close()
	var health map[string]interface{}
	json.NewDecoder(healthResp.Body).Decode(&health)

	// Get stats
	statsResp, err := httpClient.Get(baseURL + "/stats")
	if err != nil {
		return errorResult("Failed to get stats"), StatusOutput{}, nil
	}
	defer statsResp.Body.Close()
	var stats map[string]interface{}
	json.NewDecoder(statsResp.Body).Decode(&stats)

	status := "unknown"
	if s, ok := health["status"].(string); ok {
		status = s
	}
	totalChunks := 0
	if v, ok := stats["total_chunks"].(float64); ok {
		totalChunks = int(v)
	}

	text := fmt.Sprintf("Status: %s | Total chunks: %d", status, totalChunks)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, StatusOutput{Status: status, TotalChunks: totalChunks}, nil
}

// --- Helpers ---

func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}

// --- Main ---

func main() {
	// Suppress any stray output to stdout (MCP uses stdio for JSON-RPC)
	log.SetOutput(io.Discard)

	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    "local-rag",
			Version: "1.0.0",
		},
		nil,
	)

	// Register tools
	mcp.AddTool(server, &mcp.Tool{
		Name:        "rag_ingest",
		Description: "Ingest text content into the local RAG knowledge base for future semantic retrieval. Supports plain text, and the source parameter helps organize content by origin.",
	}, handleIngest)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "rag_retrieve",
		Description: "Retrieve relevant document chunks from the knowledge base using hybrid vector + keyword search. Returns the most semantically relevant passages for the given query.",
	}, handleRetrieve)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "rag_list_sources",
		Description: "List all ingested sources in the knowledge base with their chunk counts.",
	}, handleListSources)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "rag_delete_source",
		Description: "Delete all chunks associated with a specific source from the knowledge base.",
	}, handleDeleteSource)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "rag_status",
		Description: "Get the health status of the RAG service and total number of chunks in the knowledge base.",
	}, handleStatus)

	// Run over stdio (standard MCP transport for Claude Code)
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintf(os.Stderr, "MCP server error: %v\n", err)
		os.Exit(1)
	}
}
