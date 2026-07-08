package chunk

import (
	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/provider"
)

// SemanticChunker is a stub — full implementation in Task 10.
type SemanticChunker struct{}

// NewSemanticChunker creates a SemanticChunker stub.
func NewSemanticChunker(cfg config.ChunkConfig, embedder provider.EmbedProvider) *SemanticChunker {
	return &SemanticChunker{}
}

// Chunk delegates to FixedChunker until the real implementation is ready.
func (c *SemanticChunker) Chunk(text, source string) ([]Chunk, error) {
	return NewFixedChunker(200, 400).Chunk(text, source)
}
