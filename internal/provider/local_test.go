package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalEmbed_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embed" || r.Method != http.MethodPost {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var req struct {
			Input []string `json:"input"`
			Model string   `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		// Build response: one embedding per input text, each with dims=4
		type embeddingItem struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		type embedResponse struct {
			Data []embeddingItem `json:"data"`
		}
		resp := embedResponse{}
		for i := range req.Input {
			resp.Data = append(resp.Data, embeddingItem{
				Index:     i,
				Embedding: []float32{0.1, 0.2, 0.3, 0.4},
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := NewLocalEmbedProvider(srv.URL, "text-embedding-3-small", 4)

	texts := []string{"hello world", "foo bar"}
	vecs, err := p.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed returned error: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("expected 2 vectors, got %d", len(vecs))
	}
	for i, v := range vecs {
		if len(v) != 4 {
			t.Errorf("vector[%d]: expected dim 4, got %d", i, len(v))
		}
	}
	if p.Dims() != 4 {
		t.Errorf("Dims() = %d, want 4", p.Dims())
	}
}

func TestLocalEmbed_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := NewLocalEmbedProvider(srv.URL, "model", 4)
	_, err := p.Embed(context.Background(), []string{"test"})
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
}

func TestLocalRerank_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rerank" || r.Method != http.MethodPost {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var req struct {
			Query     string   `json:"query"`
			Documents []string `json:"documents"`
			Model     string   `json:"model"`
			TopN      int      `json:"top_n"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		type rerankItem struct {
			Index          int     `json:"index"`
			RelevanceScore float64 `json:"relevance_score"`
		}
		type rerankResponse struct {
			Results []rerankItem `json:"results"`
		}
		resp := rerankResponse{
			Results: []rerankItem{
				{Index: 1, RelevanceScore: 0.95},
				{Index: 0, RelevanceScore: 0.72},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := NewLocalRerankProvider(srv.URL, "bge-reranker-base")

	docs := []string{"less relevant doc", "highly relevant doc"}
	results, err := p.Rerank(context.Background(), "test query", docs, 2)
	if err != nil {
		t.Fatalf("Rerank returned error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Index != 1 {
		t.Errorf("expected first result index=1, got %d", results[0].Index)
	}
	if results[0].RelevanceScore != 0.95 {
		t.Errorf("expected first result score=0.95, got %f", results[0].RelevanceScore)
	}
	if results[1].Index != 0 {
		t.Errorf("expected second result index=0, got %d", results[1].Index)
	}
}

func TestLocalRerank_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := NewLocalRerankProvider(srv.URL, "model")
	_, err := p.Rerank(context.Background(), "query", []string{"doc"}, 1)
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
}
