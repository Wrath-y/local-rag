package agent

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/Wrath-y/local-rag/internal/provider"
)

// agentPreview limits log exposure while still making request flow diagnosable.
// It is deliberately character-based so an invalid or partial UTF-8 sequence
// cannot make a log line unexpectedly large.
func agentPreview(value string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	count := 0
	for i := range value {
		if count == limit {
			return value[:i] + "…"
		}
		count++
	}
	return value
}

func agentTextAttrs(key, value string) []any {
	return []any{key + "_chars", utf8.RuneCountInString(value), key + "_preview", agentPreview(value, 240)}
}

// toolCallLogAttrs describes a tool request without serialising raw arguments.
// In particular, rag_ingest text and permission tokens must never enter logs.
func toolCallLogAttrs(call provider.ToolCall) []any {
	attrs := []any{"tool", call.Name, "call_id", call.ID, "arguments_bytes", len(call.Arguments)}
	var arguments map[string]any
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
		return append(attrs, "arguments_valid_json", false)
	}
	attrs = append(attrs, "arguments_valid_json", true)

	switch call.Name {
	case RAGRetrieveToolName:
		if query, ok := arguments["query"].(string); ok {
			attrs = append(attrs, agentTextAttrs("query", query)...)
		}
		if topK, ok := arguments["top_k"]; ok {
			attrs = append(attrs, "top_k", fmt.Sprint(topK))
		}
	case "rag_ingest":
		if source, ok := arguments["source"].(string); ok {
			attrs = append(attrs, "source", agentPreview(source, 120))
		}
		if text, ok := arguments["text"].(string); ok {
			attrs = append(attrs, "text_chars", utf8.RuneCountInString(text))
		}
	case "rag_delete_source":
		if source, ok := arguments["source"].(string); ok {
			attrs = append(attrs, "source", agentPreview(source, 120))
		}
	}
	return attrs
}
