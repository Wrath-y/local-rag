package chunk

import (
	"strings"

	"github.com/Wrath-y/local-rag/internal/config"
)

// StructureChunker splits Markdown text at heading/atomic-block boundaries.
// For plain text it falls back to FixedChunker behaviour.
type StructureChunker struct {
	minTokens      int
	maxTokens      int
	prefixEnabled  bool
	prefixMaxDepth int
}

// NewStructureChunker creates a StructureChunker from cfg.
func NewStructureChunker(cfg config.ChunkConfig) *StructureChunker {
	return &StructureChunker{
		minTokens:      cfg.MinTokens,
		maxTokens:      cfg.MaxTokens,
		prefixEnabled:  cfg.ContextPrefix.Enabled,
		prefixMaxDepth: cfg.ContextPrefix.MaxDepth,
	}
}

// mdBlock is a classified line group inside a Markdown document.
type mdBlock struct {
	text    string // raw text (may span multiple lines)
	heading string // non-empty if this block opens a new section (heading text)
	level   int    // heading level (1-6), 0 for non-heading blocks
	atomic  bool   // true for code fences, tables, and list groups
}

// Chunk splits text into structurally-aware chunks.
func (c *StructureChunker) Chunk(text, source string) ([]Chunk, error) {
	if text == "" {
		return nil, nil
	}

	lines := strings.Split(text, "\n")

	// If the document has no Markdown headings or fences, fall back to fixed.
	if !hasMarkdown(lines) {
		return NewFixedChunker(c.minTokens, c.maxTokens).Chunk(text, source)
	}

	blocks := parseBlocks(lines)
	return c.assembleChunks(blocks, source), nil
}

// hasMarkdown returns true when the text looks like Markdown.
func hasMarkdown(lines []string) bool {
	for _, l := range lines {
		if isHeadingLine(l) {
			return true
		}
		if strings.HasPrefix(l, "```") || strings.HasPrefix(l, "~~~") {
			return true
		}
	}
	return false
}

// isHeadingLine returns true for ATX headings (# … ######).
func isHeadingLine(l string) bool {
	trimmed := strings.TrimLeft(l, "#")
	if len(trimmed) == len(l) {
		return false // no leading #
	}
	hashes := len(l) - len(trimmed)
	if hashes > 6 {
		return false
	}
	return len(trimmed) == 0 || trimmed[0] == ' '
}

// headingLevel returns the ATX heading level (1-6) or 0.
func headingLevel(l string) (int, string) {
	trimmed := strings.TrimLeft(l, "#")
	hashes := len(l) - len(trimmed)
	if hashes == 0 || hashes > 6 {
		return 0, ""
	}
	if len(trimmed) > 0 && trimmed[0] != ' ' {
		return 0, ""
	}
	return hashes, strings.TrimSpace(trimmed)
}

// parseBlocks classifies lines into mdBlock entries.
func parseBlocks(lines []string) []mdBlock {
	var blocks []mdBlock
	i := 0
	for i < len(lines) {
		l := lines[i]

		// --- Code fence ---
		if strings.HasPrefix(l, "```") || strings.HasPrefix(l, "~~~") {
			fence := l[:3]
			end := i + 1
			for end < len(lines) && !strings.HasPrefix(lines[end], fence) {
				end++
			}
			if end < len(lines) {
				end++ // include closing fence
			}
			raw := strings.Join(lines[i:end], "\n")
			blocks = append(blocks, mdBlock{text: raw, atomic: true})
			i = end
			continue
		}

		// --- Heading ---
		if lvl, title := headingLevel(l); lvl > 0 {
			blocks = append(blocks, mdBlock{
				text:    l,
				heading: title,
				level:   lvl,
			})
			i++
			continue
		}

		// --- Table ---
		if strings.Contains(l, "|") {
			end := i
			for end < len(lines) && strings.Contains(lines[end], "|") {
				end++
			}
			raw := strings.Join(lines[i:end], "\n")
			blocks = append(blocks, mdBlock{text: raw, atomic: true})
			i = end
			continue
		}

		// --- List group ---
		if isListLine(l) {
			end := i
			for end < len(lines) && (isListLine(lines[end]) || isContinuationLine(lines[end])) {
				end++
			}
			raw := strings.Join(lines[i:end], "\n")
			blocks = append(blocks, mdBlock{text: raw, atomic: true})
			i = end
			continue
		}

		// --- Ordinary paragraph line ---
		end := i + 1
		for end < len(lines) && lines[end] != "" &&
			!isHeadingLine(lines[end]) &&
			!strings.HasPrefix(lines[end], "```") &&
			!strings.HasPrefix(lines[end], "~~~") &&
			!strings.Contains(lines[end], "|") &&
			!isListLine(lines[end]) {
			end++
		}
		raw := strings.Join(lines[i:end], "\n")
		blocks = append(blocks, mdBlock{text: raw})
		i = end

		// Skip blank lines between blocks
		for i < len(lines) && lines[i] == "" {
			i++
		}
	}
	return blocks
}

func isListLine(l string) bool {
	t := strings.TrimLeft(l, " \t")
	if len(t) == 0 {
		return false
	}
	if t[0] == '-' || t[0] == '*' || t[0] == '+' {
		return len(t) > 1 && t[1] == ' '
	}
	// ordered list: "1. " etc.
	for i, ch := range t {
		if ch >= '0' && ch <= '9' {
			continue
		}
		if ch == '.' && i > 0 && i+1 < len(t) && t[i+1] == ' ' {
			return true
		}
		break
	}
	return false
}

func isContinuationLine(l string) bool {
	if l == "" {
		return false
	}
	return l[0] == ' ' || l[0] == '\t'
}

// assembleChunks walks blocks, accumulating text, flushing at heading boundaries
// when the buffer exceeds maxTokens.
func (c *StructureChunker) assembleChunks(blocks []mdBlock, source string) []Chunk {
	var chunks []Chunk
	var buf strings.Builder
	var headingStack []string // tracks current heading breadcrumb

	flush := func() {
		t := strings.TrimSpace(buf.String())
		if t == "" {
			return
		}
		if c.prefixEnabled {
			t = ApplyBreadcrumb(t, headingStack, c.prefixMaxDepth)
		}
		chunks = append(chunks, Chunk{
			Text:   t,
			Source: source,
			MD5:    computeMD5(t),
		})
		buf.Reset()
	}

	for _, b := range blocks {
		// Heading block: maybe flush, then update breadcrumb stack.
		if b.level > 0 {
			// Flush if we've accumulated enough.
			if EstimateTokens(buf.String()) >= c.maxTokens {
				flush()
			}
			// Update heading stack: pop levels >= current, push new.
			for len(headingStack) >= b.level {
				headingStack = headingStack[:len(headingStack)-1]
			}
			headingStack = append(headingStack, b.heading)

			// Write heading text into buffer.
			if buf.Len() > 0 {
				buf.WriteString("\n")
			}
			buf.WriteString(b.text)
			continue
		}

		// Atomic block: must not be split.
		atomicTokens := EstimateTokens(b.text)

		// If adding this atomic block would overflow and buffer is non-empty, flush first.
		if buf.Len() > 0 && EstimateTokens(buf.String())+atomicTokens > c.maxTokens {
			flush()
		}

		// If the atomic block is itself larger than maxTokens, emit it alone.
		if atomicTokens > c.maxTokens {
			flush() // flush anything already buffered
			t := b.text
			if c.prefixEnabled {
				t = ApplyBreadcrumb(t, headingStack, c.prefixMaxDepth)
			}
			chunks = append(chunks, Chunk{
				Text:   t,
				Source: source,
				MD5:    computeMD5(t),
			})
			continue
		}

		if buf.Len() > 0 {
			buf.WriteString("\n")
		}
		buf.WriteString(b.text)

		// Regular paragraph: flush when we hit maxTokens.
		if EstimateTokens(buf.String()) >= c.maxTokens {
			flush()
		}
	}

	flush()

	// Merge tiny trailing chunk into previous.
	if len(chunks) >= 2 {
		last := chunks[len(chunks)-1]
		if EstimateTokens(last.Text) < c.minTokens {
			prev := chunks[len(chunks)-2]
			merged := prev.Text + "\n" + last.Text
			chunks[len(chunks)-2] = Chunk{
				Text:   merged,
				Source: source,
				MD5:    computeMD5(merged),
			}
			chunks = chunks[:len(chunks)-1]
		}
	}

	return chunks
}
