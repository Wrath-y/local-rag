package chunk

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/provider"
)

// AgenticChunker uses an LLM to determine semantically coherent chunk boundaries.
type AgenticChunker struct {
	cfg config.ChunkConfig
	llm provider.LLMProvider
}

// NewAgenticChunker creates an AgenticChunker.
func NewAgenticChunker(cfg config.ChunkConfig, llm provider.LLMProvider) *AgenticChunker {
	return &AgenticChunker{cfg: cfg, llm: llm}
}

// llmChunkBoundary describes a chunk boundary returned by the LLM.
type llmChunkBoundary struct {
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Summary   string `json:"summary"`
}

// llmChunkResponse is the expected JSON structure from the LLM.
type llmChunkResponse struct {
	Chunks []llmChunkBoundary `json:"chunks"`
}

// Chunk splits text using the LLM to identify chunk boundaries.
// Falls back to StructureChunker on any error.
func (c *AgenticChunker) Chunk(text, source string) ([]Chunk, error) {
	if text == "" {
		return nil, nil
	}

	if c.llm == nil {
		return NewStructureChunker(c.cfg).Chunk(text, source)
	}

	lines := strings.Split(text, "\n")
	maxInput := c.cfg.Agentic.MaxLLMInputTokens
	if maxInput <= 0 {
		maxInput = 4000
	}

	// Segment the document by maxLLMInputTokens.
	segments := splitByLines(lines, maxInput)

	var chunks []Chunk
	lineOffset := 0
	for _, seg := range segments {
		segChunks, err := c.processSegment(seg, lines, lineOffset, source)
		if err != nil {
			// Fallback for this segment.
			segText := strings.Join(seg, "\n")
			fallback, _ := NewStructureChunker(c.cfg).Chunk(segText, source)
			chunks = append(chunks, fallback...)
		} else {
			chunks = append(chunks, segChunks...)
		}
		lineOffset += len(seg)
	}
	return chunks, nil
}

// processSegment sends one segment to the LLM and returns its chunks.
func (c *AgenticChunker) processSegment(seg []string, allLines []string, offset int, source string) ([]Chunk, error) {
	numberedText := buildNumberedText(seg, offset+1)

	prompt := fmt.Sprintf(
		"You are a document chunking expert. Split the following text into semantically coherent chunks.\n"+
			"Each chunk should be %d-%d tokens. Return JSON:\n"+
			`{"chunks": [{"start_line": 1, "end_line": 15, "summary": "..."}, ...]}`+"\n\n"+
			"Document:\n%s",
		c.cfg.MinTokens, c.cfg.MaxTokens, numberedText,
	)

	messages := []provider.Message{
		{Role: "user", Content: prompt},
	}

	reply, err := c.llm.Complete(context.Background(), messages)
	if err != nil {
		return nil, fmt.Errorf("agentic chunk: llm complete: %w", err)
	}

	boundaries, err := parseLLMResponse(reply)
	if err != nil {
		return nil, fmt.Errorf("agentic chunk: parse response: %w", err)
	}

	return c.buildChunks(boundaries, allLines, offset, source), nil
}

// buildNumberedText prepends 1-based line numbers to each line of seg.
// startLine is the global line number of seg[0].
func buildNumberedText(seg []string, startLine int) string {
	var sb strings.Builder
	for i, l := range seg {
		sb.WriteString(fmt.Sprintf("%d: %s\n", startLine+i, l))
	}
	return sb.String()
}

// parseLLMResponse extracts the JSON chunk boundaries from the LLM reply.
// Handles responses wrapped in markdown code fences.
func parseLLMResponse(reply string) ([]llmChunkBoundary, error) {
	// Strip markdown code fences if present.
	cleaned := stripCodeFences(reply)

	var resp llmChunkResponse
	if err := json.Unmarshal([]byte(cleaned), &resp); err != nil {
		return nil, fmt.Errorf("json unmarshal: %w (raw: %.200s)", err, cleaned)
	}
	if len(resp.Chunks) == 0 {
		return nil, fmt.Errorf("llm returned no chunks")
	}
	return resp.Chunks, nil
}

// stripCodeFences removes leading/trailing markdown code fences from a string.
func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	// Remove opening fence (```json, ```JSON, ``` etc.)
	for _, fence := range []string{"```json", "```JSON", "```"} {
		if strings.HasPrefix(s, fence) {
			s = s[len(fence):]
			break
		}
	}
	// Remove closing fence.
	if idx := strings.LastIndex(s, "```"); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}

// buildChunks maps LLM-returned line ranges to actual text chunks.
// offset is the 0-based index of the segment's first line in allLines.
func (c *AgenticChunker) buildChunks(boundaries []llmChunkBoundary, allLines []string, offset int, source string) []Chunk {
	totalLines := len(allLines)
	var chunks []Chunk

	for _, b := range boundaries {
		// LLM uses 1-based line numbers.
		startIdx := b.StartLine - 1 // convert to 0-based
		endIdx := b.EndLine         // exclusive

		if startIdx < 0 {
			startIdx = 0
		}
		if endIdx > totalLines {
			endIdx = totalLines
		}
		if startIdx >= endIdx {
			continue
		}

		chunkText := strings.Join(allLines[startIdx:endIdx], "\n")
		chunkText = strings.TrimSpace(chunkText)
		if chunkText == "" {
			continue
		}

		if c.cfg.Agentic.GenerateSummary && b.Summary != "" {
			chunkText = "[摘要] " + b.Summary + "\n" + chunkText
		}

		chunks = append(chunks, Chunk{
			Text:   chunkText,
			Source: source,
			MD5:    computeMD5(chunkText),
		})
	}
	return chunks
}

// splitByLines splits lines into segments where each segment's token count
// does not exceed maxTokens.
func splitByLines(lines []string, maxTokens int) [][]string {
	var segments [][]string
	var current []string
	currentTokens := 0

	for _, l := range lines {
		t := EstimateTokens(l)
		if currentTokens+t > maxTokens && len(current) > 0 {
			segments = append(segments, current)
			current = nil
			currentTokens = 0
		}
		current = append(current, l)
		currentTokens += t
	}
	if len(current) > 0 {
		segments = append(segments, current)
	}
	return segments
}
