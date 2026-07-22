package citation

import (
	"testing"
	"time"

	"github.com/Wrath-y/local-rag/internal/store"
)

func TestEvidenceFromResultsAndValidationAreRequestScoped(t *testing.T) {
	first := EvidenceFromResults([]store.RetrieveResult{
		{ID: 11, Text: "first", Source: "notes.md", DocumentTitle: "Notes", DocumentURI: "/docs/notes.md", Location: "line:8", ContentHash: "abc"},
		{ID: 12, Text: "second", Source: "guide.md", DocumentURI: "/docs/guide.md", ContentHash: "def"},
	})
	if first[0].ID != 1 || first[1].ID != 2 || first[0].Label != "[1]" || first[0].Excerpt != "first" {
		t.Fatalf("unexpected deterministic evidence: %#v", first)
	}
	manager := NewManager(time.Hour)
	firstManifest := manager.Create(first)
	valid, ok := manager.Validate(firstManifest.Token, "Supported statement [2]. Fabricated [99].")
	if !ok || len(valid.ValidLabels) != 1 || valid.ValidLabels[0] != 2 || len(valid.InvalidLabels) != 1 || valid.InvalidLabels[0] != 99 {
		t.Fatalf("unexpected validation: %#v", valid)
	}
	if valid.CitationMap[2].Source != "guide.md" {
		t.Fatalf("citation mapping missing evidence: %#v", valid.CitationMap)
	}

	secondManifest := manager.Create(first[:1])
	isolation, ok := manager.Validate(secondManifest.Token, "Prior-only label [2].")
	if !ok || len(isolation.InvalidLabels) != 1 || isolation.InvalidLabels[0] != 2 {
		t.Fatalf("citation from another request must be invalid: %#v", isolation)
	}
	missing, ok := manager.Validate(firstManifest.Token, "No citation supplied")
	if !ok || !missing.MissingCitations {
		t.Fatalf("expected missing-citation diagnostic: %#v", missing)
	}
}

func TestEvidenceFromLegacyResultPreservesAvailableProvenance(t *testing.T) {
	evidence := EvidenceFromResults([]store.RetrieveResult{{ID: 7, Text: "legacy text", Source: "legacy.txt", ContentHash: "old-md5"}})
	if len(evidence) != 1 || evidence[0].URI != "legacy.txt" || evidence[0].Location != "" || evidence[0].ContentHash != "old-md5" {
		t.Fatalf("legacy provenance was not represented safely: %#v", evidence)
	}
}
