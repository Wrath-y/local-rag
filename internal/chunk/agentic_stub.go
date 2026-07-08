package chunk

import (
	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/provider"
)

// AgenticChunker is a stub — full implementation in Task 10.
type AgenticChunker struct{}

// NewAgenticChunker creates an AgenticChunker stub.
func NewAgenticChunker(cfg config.ChunkConfig, llm provider.LLMProvider) *AgenticChunker {
	return &AgenticChunker{}
}

// Chunk delegates to FixedChunker until the real implementation is ready.
func (c *AgenticChunker) Chunk(text, source string) ([]Chunk, error) {
	return NewFixedChunker(200, 400).Chunk(text, source)
}
