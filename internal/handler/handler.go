package handler

import (
	"github.com/Wrath-y/local-rag/internal/chunk"
	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/provider"
	"github.com/Wrath-y/local-rag/internal/store"
)

// Deps holds all external dependencies for the handler layer.
type Deps struct {
	Config   *config.Config
	Store    *store.Store
	Embedder provider.EmbedProvider
	Reranker provider.RerankProvider // may be nil if disabled
	LLM      provider.LLMProvider   // may be nil
	Chunker  chunk.Chunker
}

// Handler is the HTTP handler collection.
type Handler struct {
	deps Deps

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
	strategy := ""
	if deps.Config != nil {
		strategy = deps.Config.Chunk.Strategy
		if strategy == "" {
			strategy = "fixed"
		}
	}
	return &Handler{
		deps:                 deps,
		queryRewriteStrategy: "expansion",
		chunkStrategy:        strategy,
	}
}
