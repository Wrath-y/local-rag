package handler

import (
	"time"

	"github.com/Wrath-y/local-rag/internal/chunk"
	"github.com/Wrath-y/local-rag/internal/citation"
	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/observe"
	"github.com/Wrath-y/local-rag/internal/provider"
	"github.com/Wrath-y/local-rag/internal/store"
)

// Deps holds all external dependencies for the handler layer.
type Deps struct {
	Config   *config.Config
	Store    *store.Store // Deprecated: New wraps this in Stores when needed.
	Stores   *StoreLifecycle
	Restore  *RestoreService
	Embedder provider.EmbedProvider
	Reranker provider.RerankProvider // may be nil if disabled
	LLM      provider.LLMProvider    // may be nil
	Chunker  chunk.Chunker
}

// Handler is the HTTP handler collection.
type Handler struct {
	deps             Deps
	indexRebuild     *indexRebuildCoordinator
	hookObservations *observe.HookObservations
	citations        *citation.Manager

	// runtime toggleable state
	rerankEnabled        bool
	verboseEnabled       bool
	dynamicTopKEnabled   bool
	queryRewriteEnabled  bool
	queryRewriteStrategy string
	chunkStrategy        string
}

// New creates a Handler with the given dependencies.
func New(deps Deps) *Handler {
	if deps.Stores == nil && deps.Store != nil {
		deps.Stores = NewStoreLifecycle(deps.Store)
	}
	if deps.Restore == nil && deps.Stores != nil && deps.Config != nil {
		deps.Restore = NewRestoreService(deps.Stores, deps.Config.Storage.DBPath, deps.Config.Embedding.Dims)
	}
	strategy := ""
	if deps.Config != nil {
		strategy = deps.Config.Chunk.Strategy
		if strategy == "" {
			strategy = "fixed"
		}
	}
	h := &Handler{
		deps:                 deps,
		queryRewriteStrategy: "expansion",
		chunkStrategy:        strategy,
	}
	dims := 0
	if deps.Config != nil {
		dims = deps.Config.Embedding.Dims
	}
	h.indexRebuild = newIndexRebuildCoordinator(deps.Stores, deps.Embedder, dims)
	h.hookObservations = observe.NewHookObservations()
	h.citations = citation.NewManager(time.Hour)
	return h
}
