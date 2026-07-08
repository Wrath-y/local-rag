package chunk

import (
	"strings"
	"testing"

	"github.com/Wrath-y/local-rag/internal/config"
)

func newStructureCfg(min, max int, prefixEnabled bool) config.ChunkConfig {
	return config.ChunkConfig{
		MinTokens: min,
		MaxTokens: max,
		ContextPrefix: config.ContextPrefixConfig{
			Enabled:  prefixEnabled,
			MaxDepth: 3,
		},
	}
}

// TestStructureChunker_CodeFenceNotSplit verifies that a code fence is kept
// as a single atomic block and not split across chunks.
func TestStructureChunker_CodeFenceNotSplit(t *testing.T) {
	// Build a document where a large code fence is the primary content.
	fence := "```go\n"
	for i := 0; i < 50; i++ {
		fence += "// line of code that adds tokens to this fence block\n"
	}
	fence += "```"

	doc := "# Section\n\n" + fence

	c := NewStructureChunker(newStructureCfg(10, 50, false))
	chunks, err := c.Chunk(doc, "src")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Every chunk that contains the opening ``` must also contain the closing ```.
	for _, ch := range chunks {
		opens := strings.Count(ch.Text, "```")
		if opens%2 != 0 {
			t.Errorf("code fence split across chunks: chunk text = %.80s…", ch.Text)
		}
	}
}

// TestStructureChunker_HeadingSplit verifies that long content under different
// headings is split at heading boundaries.
func TestStructureChunker_HeadingSplit(t *testing.T) {
	// Build two sections, each with enough prose to exceed maxTokens.
	para := strings.Repeat("word ", 200) // ~200 words → lots of tokens

	doc := "# Section One\n\n" + para + "\n\n# Section Two\n\n" + para

	c := NewStructureChunker(newStructureCfg(10, 100, false))
	chunks, err := c.Chunk(doc, "src")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks from two large sections, got %d", len(chunks))
	}
}

// TestStructureChunker_Breadcrumb verifies that breadcrumbs are prepended when
// ContextPrefix is enabled.
func TestStructureChunker_Breadcrumb(t *testing.T) {
	doc := "# Guide\n\n## Setup\n\nInstall the tool by running the installer script.\n"

	c := NewStructureChunker(newStructureCfg(1, 500, true))
	chunks, err := c.Chunk(doc, "src")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}

	// At least one chunk should contain a breadcrumb.
	found := false
	for _, ch := range chunks {
		if strings.Contains(ch.Text, "[") && strings.Contains(ch.Text, ">") {
			found = true
			break
		}
		if strings.Contains(ch.Text, "[Guide") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no breadcrumb found in chunks; texts: %v", chunksTexts(chunks))
	}
}

// TestStructureChunker_PlainTextFallback verifies that plain text (no Markdown
// headings or fences) is handled without error.
func TestStructureChunker_PlainTextFallback(t *testing.T) {
	plain := "This is a plain text document without any Markdown structure. " +
		"It has multiple sentences. Each sentence adds some content."

	c := NewStructureChunker(newStructureCfg(1, 100, false))
	chunks, err := c.Chunk(plain, "src")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk for non-empty plain text")
	}
}

func chunksTexts(chunks []Chunk) []string {
	out := make([]string, len(chunks))
	for i, ch := range chunks {
		out[i] = ch.Text
	}
	return out
}
