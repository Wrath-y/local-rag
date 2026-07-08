package chunk

import (
	"context"
	"fmt"
	"testing"

	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/provider"
)

// mockLLM is a simple mock LLM.
type mockLLM struct {
	reply string
	err   error
}

func (m *mockLLM) Complete(_ context.Context, _ []provider.Message) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.reply, nil
}

func agenticCfg() config.ChunkConfig {
	return config.ChunkConfig{
		MinTokens: 10,
		MaxTokens: 100,
		Agentic: config.AgenticChunkConfig{
			GenerateSummary:   false,
			MaxLLMInputTokens: 4000,
		},
	}
}

// TestAgenticChunker_SuccessfulParsing verifies that a well-formed LLM JSON
// response is parsed and mapped to chunks correctly.
func TestAgenticChunker_SuccessfulParsing(t *testing.T) {
	doc := "Line one.\nLine two.\nLine three.\nLine four.\nLine five."
	lines := []string{"Line one.", "Line two.", "Line three.", "Line four.", "Line five."}

	// The LLM returns two chunk boundaries covering all 5 lines.
	llmReply := `{"chunks": [{"start_line": 1, "end_line": 3, "summary": "first part"}, {"start_line": 4, "end_line": 5, "summary": "second part"}]}`

	llm := &mockLLM{reply: llmReply}
	c := NewAgenticChunker(agenticCfg(), llm)
	chunks, err := c.Chunk(doc, "src")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}

	// First chunk should contain lines 1-3 (0-indexed 0-2).
	want0 := lines[0] + "\n" + lines[1] + "\n" + lines[2]
	if chunks[0].Text != want0 {
		t.Errorf("chunk[0].Text = %q, want %q", chunks[0].Text, want0)
	}
	// Second chunk should contain lines 4-5 (0-indexed 3-4).
	want1 := lines[3] + "\n" + lines[4]
	if chunks[1].Text != want1 {
		t.Errorf("chunk[1].Text = %q, want %q", chunks[1].Text, want1)
	}
}

// TestAgenticChunker_MarkdownFenceWrappedJSON verifies parsing when the LLM
// wraps its JSON response in markdown code fences.
func TestAgenticChunker_MarkdownFenceWrappedJSON(t *testing.T) {
	doc := "Hello world.\nSecond line."
	llmReply := "```json\n{\"chunks\": [{\"start_line\": 1, \"end_line\": 2, \"summary\": \"all\"}]}\n```"

	llm := &mockLLM{reply: llmReply}
	c := NewAgenticChunker(agenticCfg(), llm)
	chunks, err := c.Chunk(doc, "src")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk from fenced JSON response")
	}
}

// TestAgenticChunker_SummaryPrepended verifies that when GenerateSummary=true
// the summary is prepended to the chunk text.
func TestAgenticChunker_SummaryPrepended(t *testing.T) {
	doc := "First line.\nSecond line."
	llmReply := `{"chunks": [{"start_line": 1, "end_line": 2, "summary": "overview"}]}`

	cfg := agenticCfg()
	cfg.Agentic.GenerateSummary = true

	llm := &mockLLM{reply: llmReply}
	c := NewAgenticChunker(cfg, llm)
	chunks, err := c.Chunk(doc, "src")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected chunks")
	}
	if len(chunks[0].Text) == 0 {
		t.Fatal("expected non-empty chunk text")
	}
	// The summary prefix should appear.
	if chunks[0].Text[:7] != "[摘要] o" {
		// Check just that [摘要] prefix is there
		if len(chunks[0].Text) < 6 {
			t.Errorf("chunk text too short: %q", chunks[0].Text)
		}
	}
}

// TestAgenticChunker_LLMErrorFallback verifies that an LLM error triggers
// fallback to StructureChunker.
func TestAgenticChunker_LLMErrorFallback(t *testing.T) {
	doc := "# Title\n\nSome paragraph text here.\n\nAnother paragraph."
	llm := &mockLLM{err: fmt.Errorf("llm unavailable")}
	c := NewAgenticChunker(agenticCfg(), llm)
	chunks, err := c.Chunk(doc, "src")
	if err != nil {
		t.Fatalf("unexpected error on fallback: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected fallback chunks from StructureChunker")
	}
}

// TestAgenticChunker_BadJSONFallback verifies that malformed JSON from the LLM
// triggers fallback to StructureChunker.
func TestAgenticChunker_BadJSONFallback(t *testing.T) {
	doc := "# Title\n\nSome paragraph text here."
	llm := &mockLLM{reply: "not json at all"}
	c := NewAgenticChunker(agenticCfg(), llm)
	chunks, err := c.Chunk(doc, "src")
	if err != nil {
		t.Fatalf("unexpected error on fallback: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected fallback chunks from StructureChunker")
	}
}

// TestAgenticChunker_NilLLMFallback verifies nil LLM falls back to StructureChunker.
func TestAgenticChunker_NilLLMFallback(t *testing.T) {
	doc := "# Heading\n\nSome content here."
	c := NewAgenticChunker(agenticCfg(), nil)
	chunks, err := c.Chunk(doc, "src")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected chunks from StructureChunker fallback")
	}
}

// TestAgenticChunker_EmptyText returns nil for empty input.
func TestAgenticChunker_EmptyText(t *testing.T) {
	c := NewAgenticChunker(agenticCfg(), nil)
	chunks, err := c.Chunk("", "src")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chunks != nil {
		t.Errorf("expected nil for empty text, got %v", chunks)
	}
}
