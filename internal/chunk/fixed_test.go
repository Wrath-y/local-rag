package chunk

import (
	"strings"
	"testing"
)

func TestEstimateTokens(t *testing.T) {
	// 300 Chinese chars ≈ 100 tokens
	text := strings.Repeat("你", 300)
	tokens := EstimateTokens(text)
	if tokens != 100 {
		t.Errorf("expected 100, got %d", tokens)
	}
}

func TestFixedChunker_BasicSplit(t *testing.T) {
	// Create text with multiple sentences that should split into 2+ chunks
	// with min=50, max=100 tokens
	sentences := make([]string, 10)
	for i := range sentences {
		sentences[i] = strings.Repeat("测", 90) + "。" // ~30 tokens each
	}
	text := strings.Join(sentences, "")

	c := NewFixedChunker(50, 100)
	chunks, err := c.Chunk(text, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 2 {
		t.Errorf("expected multiple chunks, got %d", len(chunks))
	}
	for _, ch := range chunks {
		if ch.MD5 == "" {
			t.Error("chunk has empty MD5")
		}
		if ch.Source != "test" {
			t.Errorf("expected source 'test', got %q", ch.Source)
		}
	}
}

func TestFixedChunker_EmptyText(t *testing.T) {
	c := NewFixedChunker(100, 200)
	chunks, err := c.Chunk("", "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks, got %d", len(chunks))
	}
}

func TestFixedChunker_SmallTextSingleChunk(t *testing.T) {
	c := NewFixedChunker(100, 200)
	chunks, err := c.Chunk("短文本。", "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk, got %d", len(chunks))
	}
}
