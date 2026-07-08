package chunk

import "github.com/Wrath-y/local-rag/internal/config"

// StructureChunker is a stub — full implementation in Task 10.
type StructureChunker struct{}

// NewStructureChunker creates a StructureChunker stub.
func NewStructureChunker(cfg config.ChunkConfig) *StructureChunker {
	return &StructureChunker{}
}

// Chunk delegates to FixedChunker until the real implementation is ready.
func (c *StructureChunker) Chunk(text, source string) ([]Chunk, error) {
	return NewFixedChunker(200, 400).Chunk(text, source)
}
