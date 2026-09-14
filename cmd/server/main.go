package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Wrath-y/local-rag/internal/chunk"
	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/graphsnapshot"
	"github.com/Wrath-y/local-rag/internal/handler"
	"github.com/Wrath-y/local-rag/internal/lifecycle"
	"github.com/Wrath-y/local-rag/internal/management"
	"github.com/Wrath-y/local-rag/internal/mcpserver"
	"github.com/Wrath-y/local-rag/internal/observe"
	"github.com/Wrath-y/local-rag/internal/operability"
	"github.com/Wrath-y/local-rag/internal/provider"
	"github.com/Wrath-y/local-rag/internal/sidecar"
	"github.com/Wrath-y/local-rag/internal/store"
)

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Accept, X-Request-ID, Idempotency-Key")

		if c.Request.Method == http.MethodOptions {
			c.Status(http.StatusNoContent)
			c.Abort()
			return
		}

		c.Next()
	}
}

func optionalProviderProbe(value any) operability.ProviderProbe {
	probe, _ := value.(operability.ProviderProbe)
	return probe
}

type observedEmbedder struct {
	provider.EmbedProvider
	health *operability.ProviderStateCache
}

func (e observedEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	result, err := e.EmbedProvider.Embed(ctx, texts)
	e.health.Observe(err)
	return result, err
}

type observedReranker struct {
	provider.RerankProvider
	health *operability.ProviderStateCache
}

type shutdownHTTPServer interface{ Shutdown(context.Context) error }
type shutdownCloser interface{ Close() }
type shutdownSidecar interface{ Stop() }
type shutdownStoreLifecycle interface{ Close() error }

// shutdownOrdered prevents store closure while HTTP or background workers can
// still admit or execute graph work. It is kept separate from signal handling
// so its process shutdown contract is directly testable.
func shutdownOrdered(ctx context.Context, server shutdownHTTPServer, graph, other shutdownCloser, sc shutdownSidecar, stores shutdownStoreLifecycle) error {
	httpErr := server.Shutdown(ctx)
	graph.Close()
	other.Close()
	sc.Stop()
	return errors.Join(httpErr, stores.Close())
}

func (r observedReranker) Rerank(ctx context.Context, query string, documents []string, topN int) ([]provider.RerankResult, error) {
	result, err := r.RerankProvider.Rerank(ctx, query, documents, topN)
	r.health.Observe(err)
	return result, err
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "启动或运行失败：%v\n", err)
		os.Exit(1)
	}
}

// run 将退出码留给最外层入口，保证失败路径同样执行 sidecar 和存储清理。
func run() error {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "lifecycle" {
		return lifecycle.Run(args[1:], os.Stdout)
	}
	mcpMode := len(args) > 0 && args[0] == "mcp"
	if len(args) > 0 && (args[0] == "serve" || args[0] == "mcp") {
		args = args[1:]
	}
	flags := flag.NewFlagSet("rag-server", flag.ContinueOnError)
	cfgPath := os.Getenv("RAG_CONFIG")
	if cfgPath == "" {
		cfgPath = "config.yaml"
	}
	flags.StringVar(&cfgPath, "config", cfgPath, "配置文件路径")
	listen := flags.String("listen", "", "监听地址，例如 127.0.0.1:8765")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("存在未知参数：%v", flags.Args())
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("配置读取失败 %q：%w", cfgPath, err)
	}
	if *listen == "" {
		*listen = serverAddress(cfg.Server.Port)
	}
	if _, _, err := net.SplitHostPort(*listen); err != nil {
		return fmt.Errorf("监听地址无效：%w", err)
	}
	cleanupJob, err := lifecycle.EnterJob()
	if err != nil {
		return fmt.Errorf("进程归属初始化失败：%w", err)
	}
	defer cleanupJob()
	if mcpMode {
		return runMCP(cfg)
	}
	shutdownSignal, stopSignals, err := lifecycle.ShutdownContext()
	if err != nil {
		return fmt.Errorf("停止通知初始化失败：%w", err)
	}
	defer stopSignals()

	observe.InitLogger(cfg.Log.Level, cfg.Log.Format)
	observe.InitMetrics()

	// 嵌入或重排任一使用本地模型时，先等待 sidecar 及配置模型就绪。
	sc := sidecar.New(sidecarConfig(cfg))
	if err := sc.Start(); err != nil {
		slog.Error("sidecar start failed", "err", err)
		return err
	}
	defer sc.Stop()

	// Init providers.
	embedder, err := provider.NewEmbedProvider(cfg.Embedding, sc.URL())
	if err != nil {
		slog.Error("embed provider init failed", "err", err)
		return err
	}

	reranker, err := provider.NewRerankProvider(cfg.Rerank, sc.URL())
	if err != nil {
		slog.Error("rerank provider init failed", "err", err)
		return err
	}
	vectorHealth := &operability.ProviderStateCache{Name: "vector", Provider: cfg.Embedding.Provider, Model: cfg.Embedding.Model, Probe: optionalProviderProbe(embedder)}
	embedder = observedEmbedder{EmbedProvider: embedder, health: vectorHealth}
	var rerankHealth *operability.ProviderStateCache
	if reranker != nil {
		rerankHealth = &operability.ProviderStateCache{Name: "rerank", Provider: cfg.Rerank.Provider, Model: cfg.Rerank.Model, Probe: optionalProviderProbe(reranker)}
		reranker = observedReranker{RerankProvider: reranker, health: rerankHealth}
	}

	// LLM provider is optional — log warning but continue if not configured.
	llm, err := provider.NewLLMProvider(cfg.LLM)
	if err != nil {
		slog.Warn("LLM provider not available (agent and rewrite features disabled)", "err", err)
		llm = nil
	}

	// Init store.
	st, err := store.New(cfg.Storage.DBPath, cfg.Embedding.Dims)
	if err != nil {
		slog.Error("store init failed", "err", err)
		return err
	}
	stores := handler.NewStoreLifecycle(st)
	defer stores.Close()
	graphService := graphsnapshot.NewService(st, nil)
	graphEmbedder := graphsnapshot.WithEmbeddingIdentity(embedder, graphsnapshot.EmbeddingIdentity{
		Algorithm:  graphsnapshot.SearchDocumentFormatV1 + "/embedding",
		Provider:   cfg.Embedding.Provider,
		Model:      cfg.Embedding.Model,
		Dimensions: cfg.Embedding.Dims,
	})
	graphAvailable := true
	if graphErr := st.GraphUnavailable(); graphErr != nil {
		graphAvailable = false
		slog.Error("graph lifecycle unavailable after initialization", "err", graphErr)
	} else if err := graphService.Start(context.Background(), func(ctx context.Context, task graphsnapshot.Task) error {
		return graphService.Dispatch(ctx, task, graphEmbedder)
	}); err != nil {
		graphAvailable = false
		slog.Error("graph lifecycle worker start failed", "err", err)
	}
	defer graphService.Close()

	// Init chunker.
	chunker := chunk.NewChunker(cfg.Chunk, embedder, llm)

	// Build handler. Graph dependencies are supplied only after recovery starts;
	// a graph migration/startup failure leaves every legacy route operational.
	handlerDeps := handler.Deps{
		Config:       cfg,
		Stores:       stores,
		Embedder:     embedder,
		Reranker:     reranker,
		LLM:          llm,
		Chunker:      chunker,
		VectorHealth: vectorHealth,
		RerankHealth: rerankHealth,
	}
	if graphAvailable {
		handlerDeps.GraphService = graphService
		handlerDeps.GraphSnapshotReader = st
		handlerDeps.GraphTaskReader = st
		handlerDeps.GraphLifecycle = st
		handlerDeps.GraphRebuild = operability.RebuildService{Repository: st, Waker: graphService}
	}
	h := handler.New(handlerDeps)

	// Gin router.
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery(), corsMiddleware())
	v1 := r.Group("/v1")
	v1.Use(handler.GraphRequestID())
	h.RegisterNamespaceLifecycle(v1)
	v1.PUT("/graphs/:namespace/snapshots/:version", h.PutGraphSnapshot)
	v1.GET("/graphs/:namespace/snapshots/:version", h.GetGraphSnapshot)
	v1.POST("/graphs/:namespace/snapshots/:version/activate", h.ActivateGraphSnapshot)
	v1.POST("/graphs/:namespace/active/query", h.QueryActiveGraphSnapshot)
	v1.DELETE("/graphs/:namespace/snapshots/:version", h.DeleteGraphSnapshot)
	v1.POST("/graphs/:namespace/snapshots/:version/rebuild", h.RebuildGraphSnapshot)
	v1.POST("/graphs/:namespace/traverse", h.TraverseGraph)
	v1.POST("/graphs/:namespace/paths", h.PathsGraph)
	v1.POST("/graphs/:namespace/retrieve", h.RetrieveGraph)
	v1.GET("/tasks/:task_id", h.GetGraphTask)

	// Core routes.
	r.POST("/ingest", h.Ingest)
	r.POST("/sources/:source/syncs", h.SyncSubmit)
	r.GET("/sources/:source/syncs/:task", h.SyncStatus)
	r.GET("/sources/:source/syncs/:task/report", h.SyncReport)
	r.POST("/sources/:source/syncs/:task/retry", h.SyncRetry)
	r.GET("/sources/:source/sync-baseline", h.SyncBaseline)
	r.POST("/retrieve", h.Retrieve)
	r.POST("/citations/validate", h.ValidateCitations)
	r.POST("/hook", h.Hook)
	r.POST("/hook/outcome", h.HookOutcomeReport)
	r.POST("/feedback", h.CreateFeedback)
	r.GET("/feedback", h.ListFeedback)
	r.GET("/feedback/aggregate", h.AggregateFeedback)
	r.GET("/feedback/export", h.ExportFeedback)
	r.POST("/feedback/candidates/convert", h.ConvertCandidates)
	r.GET("/feedback/candidates", h.ListCandidates)
	r.GET("/feedback/candidates/export", h.ExportCandidates)
	r.POST("/feedback/candidates/:id/review", h.ReviewCandidate)

	// Toggle routes.
	r.POST("/rerank/toggle", h.RerankToggle)
	r.POST("/retrieve/verbose", h.VerboseToggle)
	r.POST("/retrieve/dynamic-top-k", h.DynamicTopKToggle)
	r.POST("/retrieve/query-rewrite", h.QueryRewriteToggle)

	// Config routes.
	r.GET("/config/chunk-strategy", h.GetChunkStrategy)
	r.PUT("/config/chunk-strategy", h.SetChunkStrategy)

	// Management routes.
	r.GET("/sources", h.ListSources)
	r.DELETE("/source", h.DeleteSource)
	r.DELETE("/reset", h.Reset)
	r.GET("/stats", h.Stats)
	r.GET("/export", h.Export)
	r.POST("/import", h.Import)

	// Observability routes.
	r.GET("/health", func(c *gin.Context) {
		// 随机实例标识供本轮启动验证，避免端口争用时误认外部健康响应。
		if id := os.Getenv("RAG_INSTANCE_ID"); id != "" {
			c.Header("X-Local-RAG-Instance", id)
		}
		h.Health(c)
	})
	r.GET("/metrics", h.Metrics)
	r.GET("/storage/integrity-check", h.IntegrityCheck)

	// Backup routes.
	r.POST("/backup/run", h.BackupRun)
	r.GET("/backup/list", h.BackupList)
	r.POST("/backup/restore", h.BackupRestore)

	// Index routes.
	r.POST("/index/rebuild", h.IndexRebuild)
	r.GET("/index/status", h.IndexStatus)

	// Agent routes.
	r.POST("/agent/chat", h.AgentChat)
	r.POST("/agent/permission/:token", h.AgentApprovePermission)
	r.POST("/agent/session", h.AgentCreateSession)
	r.GET("/agent/sessions", h.AgentListSessions)
	r.DELETE("/agent/session/:id", h.AgentDeleteSession)

	server := &http.Server{Addr: *listen, Handler: r}

	// 先安装信号处理；等待 HTTP 及 worker、存储清理完成后才能返回 main。
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.ListenAndServe() }()
	slog.Info("服务启动", "addr", server.Addr)
	select {
	case err := <-serveDone:
		if err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	case <-shutdownSignal.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := shutdownOrdered(shutdownCtx, server, graphService, h, sc, stores)
		if err != nil {
			_ = server.Close()
		}
		<-serveDone
		return err
	}
}

func sidecarConfig(cfg *config.Config) sidecar.Config {
	out := sidecar.Config{Provider: "disabled", Port: cfg.Sidecar.Port, HealthInterval: time.Duration(cfg.Sidecar.HealthInterval) * time.Second, HealthRetries: cfg.Sidecar.HealthRetries, StartupTimeout: time.Duration(cfg.Sidecar.StartupTimeout) * time.Second}
	if cfg.Embedding.Provider == "local" {
		out.Provider = "local"
		out.EmbedModel = cfg.Embedding.Model
	}
	if cfg.Rerank.Provider == "local" {
		out.Provider = "local"
		out.RerankModel = cfg.Rerank.Model
	}
	// 独立启动和编排均优先使用已经准备好的虚拟环境；不会在运行中安装依赖。
	python := filepath.Join("sidecar", ".venv", "bin", "python3")
	if runtime.GOOS == "windows" {
		python = filepath.Join("sidecar", ".venv", "Scripts", "python.exe")
	}
	if _, err := os.Stat(python); err == nil {
		if absolute, err := filepath.Abs(python); err == nil {
			out.PythonExecutable = absolute
		}
	}
	return out
}

func serverAddress(port int) string { return fmt.Sprintf("127.0.0.1:%d", port) }

// runMCP starts the server in MCP (Model Context Protocol) mode.
// It communicates over stdin/stdout using JSON-RPC, NOT HTTP.
// Other agents (Claude Code, Cursor, etc.) connect to this directly.
func runMCP(cfg *config.Config) error {
	// In MCP mode, only log errors to stderr (stdout is the protocol channel).
	observe.InitLogger("error", "text")

	// Start sidecar if needed.
	sc := sidecar.New(sidecarConfig(cfg))
	if err := sc.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "sidecar start failed: %v\n", err)
		return err
	}
	defer sc.Stop()

	// Init providers.
	embedder, err := provider.NewEmbedProvider(cfg.Embedding, sc.URL())
	if err != nil {
		fmt.Fprintf(os.Stderr, "embed provider: %v\n", err)
		return err
	}
	reranker, _ := provider.NewRerankProvider(cfg.Rerank, sc.URL())
	llm, _ := provider.NewLLMProvider(cfg.LLM)

	// Init store.
	st, err := store.New(cfg.Storage.DBPath, cfg.Embedding.Dims)
	if err != nil {
		fmt.Fprintf(os.Stderr, "store init: %v\n", err)
		return err
	}
	defer st.Close()

	// Init chunker.
	chunker := chunk.NewChunker(cfg.Chunk, embedder, llm)

	// Run MCP server (blocks until client disconnects).
	managementService := management.New(management.Deps{
		Config:   cfg,
		Store:    st,
		Embedder: embedder,
		Chunker:  chunker,
	})
	deps := mcpserver.Deps{
		Config:     cfg,
		Store:      st,
		Embedder:   embedder,
		Reranker:   reranker,
		LLM:        llm,
		Chunker:    chunker,
		Management: managementService,
	}
	if err := mcpserver.Run(context.Background(), deps); err != nil {
		fmt.Fprintf(os.Stderr, "MCP server error: %v\n", err)
		return err
	}
	return nil
}
