package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Wrath-y/local-rag/internal/citation"
	"github.com/Wrath-y/local-rag/internal/store"
)

func TestRetrieveReturnsStructuredCitationsAndValidationDiagnostics(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.InsertChunkWithProvenance("redis evidence", "redis.md", "redis-citation", "", "", store.Provenance{Title: "Redis guide", URI: "/docs/redis.md", Location: "section:cache"}, []float32{0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	h := New(testDeps(t, st))
	retrieveBody := bytes.NewBufferString(`{"text":"redis evidence"}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/retrieve", retrieveBody)
	c.Request.Header.Set("Content-Type", "application/json")
	h.Retrieve(c)
	if w.Code != http.StatusOK {
		t.Fatalf("retrieve: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Chunks        []string            `json:"chunks"`
		Citations     []citation.Evidence `json:"citations"`
		EvidenceToken string              `json:"evidence_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Chunks) != 1 || !strings.Contains(response.Chunks[0], "[1]") || len(response.Citations) != 1 || response.EvidenceToken == "" {
		t.Fatalf("missing compatible chunk or citation response: %#v", response)
	}
	if got := response.Citations[0]; got.Title != "Redis guide" || got.URI != "/docs/redis.md" || got.Location != "section:cache" || got.ContentHash != "redis-citation" {
		t.Fatalf("unexpected citation: %#v", got)
	}

	validationBody, _ := json.Marshal(map[string]string{"evidence_token": response.EvidenceToken, "answer": "Supported [1], fabricated [9]."})
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/citations/validate", bytes.NewReader(validationBody))
	c.Request.Header.Set("Content-Type", "application/json")
	h.ValidateCitations(c)
	var validation citation.Validation
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &validation) != nil || len(validation.ValidLabels) != 1 || validation.ValidLabels[0] != 1 || len(validation.InvalidLabels) != 1 || validation.InvalidLabels[0] != 9 {
		t.Fatalf("unexpected validation: %d %s", w.Code, w.Body.String())
	}
}
