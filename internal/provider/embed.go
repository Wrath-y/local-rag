package provider

import "context"

// EmbedProvider generates dense vector embeddings for a batch of texts.
type EmbedProvider interface {
	// Embed returns one embedding vector per input text, in the same order.
	Embed(ctx context.Context, texts []string) ([][]float32, error)

	// Dims returns the dimensionality of the embedding vectors.
	Dims() int
}
