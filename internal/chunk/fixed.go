package chunk

import (
	"regexp"
	"strings"
)

// sentenceSplitRe splits on Chinese/English sentence-ending punctuation
// (。！？.!?) followed by optional whitespace, or on newlines.
var sentenceSplitRe = regexp.MustCompile(`[。！？.!?]\s*|\n+`)

// FixedChunker splits text by sentences, then merges sentences into chunks
// respecting minTokens/maxTokens boundaries.
type FixedChunker struct {
	minTokens int
	maxTokens int
}

// NewFixedChunker creates a FixedChunker with the given token boundaries.
func NewFixedChunker(minTokens, maxTokens int) *FixedChunker {
	return &FixedChunker{minTokens: minTokens, maxTokens: maxTokens}
}

// Chunk splits text into fixed-size chunks by sentence boundaries.
func (c *FixedChunker) Chunk(text string, source string) ([]Chunk, error) {
	if text == "" {
		return nil, nil
	}

	// Split into sentences, keeping non-empty segments.
	indices := sentenceSplitRe.FindAllStringIndex(text, -1)
	var sentences []string
	prev := 0
	for _, loc := range indices {
		seg := strings.TrimSpace(text[prev:loc[1]])
		if seg != "" {
			sentences = append(sentences, seg)
		}
		prev = loc[1]
	}
	// Capture any trailing content after the last delimiter.
	if tail := strings.TrimSpace(text[prev:]); tail != "" {
		sentences = append(sentences, tail)
	}

	if len(sentences) == 0 {
		return nil, nil
	}

	var chunks []Chunk
	var buf strings.Builder

	flush := func() {
		t := strings.TrimSpace(buf.String())
		if t == "" {
			return
		}
		chunks = append(chunks, Chunk{
			Text:   t,
			Source: source,
			MD5:    computeMD5(t),
		})
		buf.Reset()
	}

	for _, s := range sentences {
		candidate := buf.String()
		if candidate != "" {
			candidate += " "
		}
		candidate += s

		if EstimateTokens(candidate) > c.maxTokens && buf.Len() > 0 {
			// Flush the current buffer before adding this sentence.
			flush()
		}
		if buf.Len() > 0 {
			buf.WriteString(" ")
		}
		buf.WriteString(s)
	}

	// Flush any remaining content.
	flush()

	// If the last chunk is too small, merge it with the previous one.
	if len(chunks) >= 2 {
		last := chunks[len(chunks)-1]
		if EstimateTokens(last.Text) < c.minTokens {
			prev := chunks[len(chunks)-2]
			merged := prev.Text + " " + last.Text
			chunks[len(chunks)-2] = Chunk{
				Text:   merged,
				Source: source,
				MD5:    computeMD5(merged),
			}
			chunks = chunks[:len(chunks)-1]
		}
	}

	return chunks, nil
}
