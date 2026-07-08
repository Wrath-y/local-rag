package provider

// Message represents a single turn in a conversation with an LLM.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// RerankResult holds the re-ranked position and relevance score for a document.
type RerankResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}
