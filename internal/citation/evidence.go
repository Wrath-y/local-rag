// Package citation creates and validates request-scoped citation evidence.
package citation

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Wrath-y/local-rag/internal/store"
)

// Evidence is a client-facing citation record. ID is an ordinal assigned only
// within one retrieval request; it is never a persistent document identifier.
type Evidence struct {
	// CitationID is a durable, retrieval-scoped identifier when feedback
	// capture is enabled. ID remains the request-scoped display label.
	CitationID  string `json:"citation_id,omitempty"`
	ID          int    `json:"id"`
	Label       string `json:"label"`
	ChunkID     int64  `json:"chunk_id"`
	Source      string `json:"source"`
	Title       string `json:"title,omitempty"`
	URI         string `json:"uri,omitempty"`
	Location    string `json:"location,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
	Excerpt     string `json:"excerpt"`
}

// Manifest associates a short-lived opaque token with ordered evidence.
type Manifest struct {
	Token     string     `json:"evidence_token"`
	Citations []Evidence `json:"citations"`
	ExpiresAt time.Time  `json:"expires_at"`
}

// Validation describes citation labels found in one completed answer.
type Validation struct {
	EvidenceToken    string           `json:"evidence_token"`
	Labels           []int            `json:"labels"`
	ValidLabels      []int            `json:"valid_labels"`
	InvalidLabels    []int            `json:"invalid_labels"`
	CitationMap      map[int]Evidence `json:"citation_map"`
	MissingCitations bool             `json:"missing_citations"`
}

// EvidenceFromResults deterministically assigns IDs in the already-ranked
// order received from retrieval.
func EvidenceFromResults(results []store.RetrieveResult) []Evidence {
	evidence := make([]Evidence, 0, len(results))
	for i, result := range results {
		excerpt := result.Text
		if result.ParentText != "" {
			excerpt = result.ParentText
		}
		uri := result.DocumentURI
		if uri == "" {
			uri = result.Source
		}
		evidence = append(evidence, Evidence{
			ID:          i + 1,
			Label:       fmt.Sprintf("[%d]", i+1),
			ChunkID:     result.ID,
			Source:      result.Source,
			Title:       result.DocumentTitle,
			URI:         uri,
			Location:    result.Location,
			ContentHash: result.ContentHash,
			Excerpt:     strings.TrimSpace(excerpt),
		})
	}
	return evidence
}

// RenderChunks retains the existing plain-text response format while making
// its supplied citation label visible to clients that do not use JSON fields.
func RenderChunks(evidence []Evidence) []string {
	chunks := make([]string, 0, len(evidence))
	for _, item := range evidence {
		chunks = append(chunks, fmt.Sprintf("%s [来源: %s]\n%s", item.Label, item.Source, item.Excerpt))
	}
	return chunks
}

// RenderAnswerInstructions is the single grounded-answer rule shared by Hook
// and Agent prompts.
func RenderAnswerInstructions(evidence []Evidence) string {
	var b strings.Builder
	b.WriteString("[RAG evidence]\nUse only the supplied citation labels for factual claims. Attach a label such as [1] to each supported claim. Do not invent, change, or reuse labels from another request. If the evidence does not establish a claim, say that the available evidence is insufficient.\n")
	if len(evidence) == 0 {
		b.WriteString("No retrieval evidence is available for this request. State uncertainty rather than citing a source.\n")
		return b.String()
	}
	for _, item := range evidence {
		fmt.Fprintf(&b, "%s source=%s", item.Label, item.Source)
		if item.Title != "" {
			fmt.Fprintf(&b, " title=%s", item.Title)
		}
		if item.URI != "" {
			fmt.Fprintf(&b, " uri=%s", item.URI)
		}
		if item.Location != "" {
			fmt.Fprintf(&b, " location=%s", item.Location)
		}
		fmt.Fprintf(&b, "\n%s\n", item.Excerpt)
	}
	return b.String()
}

// Manager stores bounded, in-memory manifests. Tokens are independent across
// requests even though citation ordinals intentionally restart at one.
type Manager struct {
	mu        sync.Mutex
	manifests map[string]Manifest
	ttl       time.Duration
}

func NewManager(ttl time.Duration) *Manager {
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &Manager{manifests: make(map[string]Manifest), ttl: ttl}
}

func (m *Manager) Create(evidence []Evidence) Manifest {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for token, manifest := range m.manifests {
		if !manifest.ExpiresAt.After(now) {
			delete(m.manifests, token)
		}
	}
	manifest := Manifest{Token: newToken(), Citations: append([]Evidence(nil), evidence...), ExpiresAt: now.Add(m.ttl)}
	m.manifests[manifest.Token] = manifest
	return manifest
}

func (m *Manager) Validate(token, answer string) (Validation, bool) {
	m.mu.Lock()
	manifest, ok := m.manifests[token]
	if ok && !manifest.ExpiresAt.After(time.Now()) {
		delete(m.manifests, token)
		ok = false
	}
	m.mu.Unlock()
	if !ok {
		return Validation{}, false
	}
	byID := make(map[int]Evidence, len(manifest.Citations))
	for _, item := range manifest.Citations {
		byID[item.ID] = item
	}
	labels := ParseLabels(answer)
	validation := Validation{
		EvidenceToken:    token,
		Labels:           labels,
		CitationMap:      make(map[int]Evidence),
		MissingCitations: len(manifest.Citations) > 0 && len(labels) == 0,
	}
	for _, label := range labels {
		if item, exists := byID[label]; exists {
			validation.ValidLabels = append(validation.ValidLabels, label)
			validation.CitationMap[label] = item
		} else {
			validation.InvalidLabels = append(validation.InvalidLabels, label)
		}
	}
	return validation, true
}

var labelPattern = regexp.MustCompile(`\[(\d+)\]`)

// ParseLabels returns distinct labels in first-appearance order.
func ParseLabels(answer string) []int {
	matches := labelPattern.FindAllStringSubmatch(answer, -1)
	seen := make(map[int]bool, len(matches))
	labels := make([]int, 0, len(matches))
	for _, match := range matches {
		var label int
		if _, err := fmt.Sscanf(match[1], "%d", &label); err == nil && !seen[label] {
			seen[label] = true
			labels = append(labels, label)
		}
	}
	return labels
}

func newToken() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		// A timestamp is sufficient for a process-local fallback and avoids
		// turning an otherwise successful retrieval into an unavailable one.
		return fmt.Sprintf("evidence-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(bytes)
}
