package store

import (
	"strings"
	"testing"
)

func newFeedbackStore(t *testing.T) *Store {
	t.Helper()
	st, err := New(t.TempDir()+"/feedback.db", 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestFeedbackRecordLinkageAndCandidates(t *testing.T) {
	st := newFeedbackStore(t)
	event, err := st.RecordRetrieval(RecordRetrievalInput{Query: "  cafe\u0301  redis  ", Channel: "http", Citations: []RetrievalCitation{{ChunkID: 1, Source: "docs"}}})
	if err != nil {
		t.Fatal(err)
	}
	if event.QueryExcerpt != "" || len(event.Citations) != 1 {
		t.Fatalf("event=%+v", event)
	}
	badEvent, err := st.RecordRetrieval(RecordRetrievalInput{Query: "other", Channel: "mcp", Citations: []RetrievalCitation{{ChunkID: 2, Source: "other"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateFeedback(FeedbackInput{RetrievalID: event.RetrievalID, Kind: "citation-error", CitationIDs: []string{badEvent.Citations[0].CitationID}}, 100, 30); !IsValidationError(err) {
		t.Fatalf("cross event error = %v", err)
	}
	_, err = st.CreateFeedback(FeedbackInput{RetrievalID: event.RetrievalID, Kind: "citation-error", CitationIDs: []string{event.Citations[0].CitationID}, Note: "wrong source"}, 100, 30)
	if err != nil {
		t.Fatal(err)
	}
	page, err := st.ListFeedback(FeedbackListFilter{}, 30)
	if err != nil || len(page.Records) != 1 || page.Records[0].Note != "" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	created, skipped, err := st.ConvertCandidates(FeedbackListFilter{}, false, false, 10, 30)
	if err != nil || len(created) != 1 || skipped != 0 {
		t.Fatalf("created=%+v skipped=%d err=%v", created, skipped, err)
	}
	candidateID := created[0].CandidateID
	created, skipped, err = st.ConvertCandidates(FeedbackListFilter{}, false, false, 10, 30)
	if err != nil || len(created) != 0 || skipped != 1 {
		t.Fatalf("repeat created=%+v skipped=%d err=%v", created, skipped, err)
	}
	item, err := st.ReviewCandidate(candidateID, "approved", "verified", 100)
	if err != nil || item.Status != "approved" {
		t.Fatalf("review=%+v err=%v", item, err)
	}
}

func TestRedactQueryExcerptAndFingerprintNormalization(t *testing.T) {
	st := newFeedbackStore(t)
	a, err := st.QueryFingerprint(" cafe\u0301   redis ")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.QueryFingerprint("café redis")
	if err != nil || a != b {
		t.Fatalf("fingerprints %q %q err=%v", a, b, err)
	}
	excerpt := RedactQueryExcerpt("https://example.test/path?token=secret#fragment password=hunter2", 200)
	if strings.Contains(excerpt, "secret") || strings.Contains(excerpt, "hunter2") || strings.Contains(excerpt, "fragment") {
		t.Fatalf("excerpt leaked: %q", excerpt)
	}
}
