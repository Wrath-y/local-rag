package chunk

import (
	"crypto/md5"
	"fmt"

	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/provider"
)

// Chunk is a single unit of indexed text.
type Chunk struct {
	Text       string
	Source     string
	MD5        string
	ParentText string
	ParentID   string
}

// Chunker splits a document into Chunks.
type Chunker interface {
	Chunk(text string, source string) ([]Chunk, error)
}

// NewChunker creates a Chunker based on the configured strategy.
func NewChunker(cfg config.ChunkConfig, embedder provider.EmbedProvider, llm provider.LLMProvider) Chunker {
	switch cfg.Strategy {
	case "structure":
		return NewStructureChunker(cfg)
	case "semantic":
		return NewSemanticChunker(cfg, embedder)
	case "agentic":
		return NewAgenticChunker(cfg, llm)
	default:
		return NewFixedChunker(cfg.MinTokens, cfg.MaxTokens)
	}
}

func computeMD5(text string) string {
	return fmt.Sprintf("%x", md5.Sum([]byte(text)))
}
