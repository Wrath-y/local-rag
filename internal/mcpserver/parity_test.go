package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Wrath-y/local-rag/internal/chunk"
	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/document"
	"github.com/Wrath-y/local-rag/internal/handler"
	"github.com/Wrath-y/local-rag/internal/management"
	"github.com/Wrath-y/local-rag/internal/observe"
	"github.com/Wrath-y/local-rag/internal/store"
)

type parityEmbedder struct{}

func (parityEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	vectors := make([][]float32, len(texts))
	for index := range vectors {
		vectors[index] = []float32{1, 0, 0, 0}
	}
	return vectors, nil
}
func (parityEmbedder) Dims() int { return 4 }

type ingestParityChunker struct{}

func (ingestParityChunker) Chunk(text, source string) ([]chunk.Chunk, error) {
	return []chunk.Chunk{{Text: text, Source: source, MD5: "mcp-" + source}}, nil
}

func TestHTTPAndMCPShareIntegrityConfigurationAndIndexServices(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := t.TempDir() + "/parity.db"
	st, err := store.New(dbPath, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.InsertChunk("parity", "parity", "parity", "", "", []float32{1, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Storage: config.StorageConfig{DBPath: dbPath}, Embedding: config.EmbeddingConfig{Dims: 4}, Chunk: config.ChunkConfig{Strategy: "fixed"}}
	embedder := parityEmbedder{}
	service := management.New(management.Deps{Config: cfg, Store: st, Embedder: embedder})
	h := handler.New(handler.Deps{Config: cfg, Store: st, Embedder: embedder, Management: service})
	mcpAdapter := &server{deps: Deps{Management: service}}

	httpResult := httptest.NewRecorder()
	httpContext, _ := gin.CreateTestContext(httpResult)
	httpContext.Request = httptest.NewRequest(http.MethodGet, "/storage/integrity-check", nil)
	h.IntegrityCheck(httpContext)
	if httpResult.Code != http.StatusOK {
		t.Fatalf("HTTP integrity = %d: %s", httpResult.Code, httpResult.Body.String())
	}
	mcpResult, integrity, err := mcpAdapter.handleIntegrityCheck(context.Background(), nil, IntegrityInput{})
	if err != nil || mcpResult.IsError || integrity.Detail != "ok" {
		t.Fatalf("MCP integrity = %#v, %#v, %v", mcpResult, integrity, err)
	}

	h.RerankToggle(httpContext)
	_, retrieval, err := mcpAdapter.handleRetrievalConfigGet(context.Background(), nil, RetrievalConfigGetInput{})
	if err != nil || !retrieval.RerankEnabled {
		t.Fatalf("MCP retrieval config = %#v, %v", retrieval, err)
	}

	indexResult := httptest.NewRecorder()
	indexContext, _ := gin.CreateTestContext(indexResult)
	indexContext.Request = httptest.NewRequest(http.MethodPost, "/index/rebuild", nil)
	h.IndexRebuild(indexContext)
	if indexResult.Code != http.StatusAccepted {
		t.Fatalf("HTTP index start = %d: %s", indexResult.Code, indexResult.Body.String())
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_, status, err := mcpAdapter.handleIndexStatus(context.Background(), nil, IndexStatusInput{})
		if err == nil && status["state"] == management.TaskSucceeded {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("MCP did not observe HTTP-started index rebuild completion")
}

func TestMCPIngestUsesCommonLoaderServiceAndSafeErrors(t *testing.T) {
	observe.InitMetrics()
	st, err := store.New(t.TempDir()+"/mcp.db", 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	deps := Deps{
		Config:         &config.Config{Embedding: config.EmbeddingConfig{}},
		Store:          st,
		Embedder:       parityEmbedder{},
		Chunker:        ingestParityChunker{},
		LoaderRegistry: document.BuiltinRegistry(nil),
	}
	adapter := &server{deps: deps}
	adapter.ingestService = document.Service{Registry: deps.LoaderRegistry, Pipeline: document.PipelineFunc(adapter.ingestDocument)}
	result, output, err := adapter.handleIngest(context.Background(), nil, IngestInput{Text: "from mcp"})
	if err != nil || result.IsError || output.Status != "ok" || output.ChunksAdded != 1 {
		t.Fatalf("ingest = %#v %#v %v", result, output, err)
	}
	sources, err := st.ListSources()
	if err != nil || len(sources) != 1 || sources[0].Source != "manual" {
		t.Fatalf("sources = %#v, %v", sources, err)
	}
	result, _, err = adapter.handleIngest(context.Background(), nil, IngestInput{URL: "ftp://example.com/secret"})
	if err != nil || !result.IsError || len(result.Content) != 1 {
		t.Fatalf("unsupported result = %#v, %v", result, err)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	if text != "[unsupported_input] Provide direct text, a local file path, or a supported Feishu document link." {
		t.Fatalf("unexpected safe message %q", text)
	}
}
