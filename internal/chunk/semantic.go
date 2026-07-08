package chunk

import (
	"context"
	"math"
	"sort"
	"strings"

	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/provider"
)

// SemanticChunker groups sentences into chunks based on embedding similarity.
type SemanticChunker struct {
	cfg      config.ChunkConfig
	embedder provider.EmbedProvider
}

// NewSemanticChunker creates a SemanticChunker.
func NewSemanticChunker(cfg config.ChunkConfig, embedder provider.EmbedProvider) *SemanticChunker {
	return &SemanticChunker{cfg: cfg, embedder: embedder}
}

// Chunk splits text into semantically coherent chunks.
// Falls back to FixedChunker when embedder is nil or embedding fails.
func (c *SemanticChunker) Chunk(text, source string) ([]Chunk, error) {
	if text == "" {
		return nil, nil
	}

	if c.embedder == nil {
		return NewFixedChunker(c.cfg.MinTokens, c.cfg.MaxTokens).Chunk(text, source)
	}

	sentences := splitSentences(text)
	if len(sentences) == 0 {
		return nil, nil
	}
	if len(sentences) == 1 {
		t := sentences[0]
		return []Chunk{{Text: t, Source: source, MD5: computeMD5(t)}}, nil
	}

	// Embed all sentences.
	embeddings, err := c.embedder.Embed(context.Background(), sentences)
	if err != nil || len(embeddings) != len(sentences) {
		// Fallback
		return NewFixedChunker(c.cfg.MinTokens, c.cfg.MaxTokens).Chunk(text, source)
	}

	// Compute cosine similarities between adjacent sentences.
	sims := make([]float64, len(sentences)-1)
	for i := 0; i < len(sentences)-1; i++ {
		sims[i] = cosineSimilarity(embeddings[i], embeddings[i+1])
	}

	// Find breakpoints: positions where similarity is below the percentile threshold.
	// threshold_percentile=90 means we cut at the bottom 10% of similarities.
	cutoff := percentileValue(sims, 100-c.cfg.Semantic.ThresholdPercentile)
	var breakpoints []int
	for i, s := range sims {
		if s < cutoff {
			breakpoints = append(breakpoints, i+1) // break before sentence i+1
		}
	}

	// Group sentences into raw groups.
	groups := groupByBreakpoints(sentences, breakpoints)

	// Post-process: merge too-small groups, split too-large groups.
	minSize := c.cfg.Semantic.MinChunkSize
	maxSize := c.cfg.Semantic.MaxChunkSize
	if minSize <= 0 {
		minSize = 2
	}
	if maxSize <= 0 {
		maxSize = 20
	}
	groups = mergeSmallGroups(groups, minSize)
	groups = splitLargeGroups(groups, maxSize)

	// Convert groups to Chunks, respecting token limits.
	var chunks []Chunk
	maxTok := c.cfg.MaxTokens
	for _, g := range groups {
		groupText := strings.Join(g, " ")
		if EstimateTokens(groupText) <= maxTok {
			chunks = append(chunks, Chunk{
				Text:   groupText,
				Source: source,
				MD5:    computeMD5(groupText),
			})
			continue
		}
		// Split oversized group by sentences respecting token limits.
		subChunks := splitGroupByTokens(g, source, maxTok)
		chunks = append(chunks, subChunks...)
	}

	return chunks, nil
}

// splitSentences splits text into sentences using the same regex as FixedChunker.
func splitSentences(text string) []string {
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
	if tail := strings.TrimSpace(text[prev:]); tail != "" {
		sentences = append(sentences, tail)
	}
	return sentences
}

// cosineSimilarity returns the cosine similarity between two vectors.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		fa, fb := float64(a[i]), float64(b[i])
		dot += fa * fb
		normA += fa * fa
		normB += fb * fb
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}

// percentileValue returns the p-th percentile value from a slice of float64.
// p=10 returns the value at the 10th percentile.
func percentileValue(sims []float64, p int) float64 {
	if len(sims) == 0 {
		return 0
	}
	sorted := make([]float64, len(sims))
	copy(sorted, sims)
	sort.Float64s(sorted)

	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	idx := float64(p) / 100.0 * float64(len(sorted)-1)
	lo := int(idx)
	hi := lo + 1
	if hi >= len(sorted) {
		return sorted[lo]
	}
	frac := idx - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

// groupByBreakpoints splits sentences into groups at the given break positions.
func groupByBreakpoints(sentences []string, breakpoints []int) [][]string {
	if len(breakpoints) == 0 {
		return [][]string{sentences}
	}
	var groups [][]string
	prev := 0
	for _, bp := range breakpoints {
		if bp > prev {
			groups = append(groups, sentences[prev:bp])
		}
		prev = bp
	}
	if prev < len(sentences) {
		groups = append(groups, sentences[prev:])
	}
	return groups
}

// mergeSmallGroups merges consecutive groups that are smaller than minSize sentences.
func mergeSmallGroups(groups [][]string, minSize int) [][]string {
	if len(groups) == 0 {
		return groups
	}
	result := [][]string{groups[0]}
	for i := 1; i < len(groups); i++ {
		last := result[len(result)-1]
		if len(last) < minSize {
			result[len(result)-1] = append(last, groups[i]...)
		} else {
			result = append(result, groups[i])
		}
	}
	// If the last group is still too small, merge it into the previous.
	if len(result) >= 2 && len(result[len(result)-1]) < minSize {
		n := len(result)
		result[n-2] = append(result[n-2], result[n-1]...)
		result = result[:n-1]
	}
	return result
}

// splitLargeGroups splits groups that exceed maxSize sentences.
func splitLargeGroups(groups [][]string, maxSize int) [][]string {
	var result [][]string
	for _, g := range groups {
		for len(g) > maxSize {
			result = append(result, g[:maxSize])
			g = g[maxSize:]
		}
		if len(g) > 0 {
			result = append(result, g)
		}
	}
	return result
}

// splitGroupByTokens breaks an oversized sentence group into token-limited chunks.
func splitGroupByTokens(sentences []string, source string, maxTokens int) []Chunk {
	var chunks []Chunk
	var buf strings.Builder

	flush := func() {
		t := strings.TrimSpace(buf.String())
		if t == "" {
			return
		}
		chunks = append(chunks, Chunk{Text: t, Source: source, MD5: computeMD5(t)})
		buf.Reset()
	}

	for _, s := range sentences {
		candidate := buf.String()
		if candidate != "" {
			candidate += " "
		}
		candidate += s
		if EstimateTokens(candidate) > maxTokens && buf.Len() > 0 {
			flush()
		}
		if buf.Len() > 0 {
			buf.WriteString(" ")
		}
		buf.WriteString(s)
	}
	flush()
	return chunks
}
