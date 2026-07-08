package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------------------------------------------------------------------------
// OpenAIEmbedProvider tests
// ---------------------------------------------------------------------------

func TestOpenAIEmbed_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" || r.Method != http.MethodPost {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		// Verify Authorization header.
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
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
				Embedding: []float32{0.1, 0.2, 0.3},
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := NewOpenAIEmbedProvider(srv.URL, "test-key", "text-embedding-3-small", 3)

	texts := []string{"hello", "world"}
	vecs, err := p.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed returned error: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("expected 2 vectors, got %d", len(vecs))
	}
	for i, v := range vecs {
		if len(v) != 3 {
			t.Errorf("vector[%d]: expected dim 3, got %d", i, len(v))
		}
	}
	if p.Dims() != 3 {
		t.Errorf("Dims() = %d, want 3", p.Dims())
	}
}

func TestOpenAIEmbed_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	p := NewOpenAIEmbedProvider(srv.URL, "bad-key", "model", 3)
	_, err := p.Embed(context.Background(), []string{"test"})
	if err == nil {
		t.Fatal("expected error for 401 response, got nil")
	}
}

// ---------------------------------------------------------------------------
// OpenAILLMProvider tests
// ---------------------------------------------------------------------------

func TestOpenAILLM_Complete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Method != http.MethodPost {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		// Verify Authorization header.
		auth := r.Header.Get("Authorization")
		if auth != "Bearer llm-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var req struct {
			Model    string    `json:"model"`
			Messages []Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		resp := struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{
				{Message: struct {
					Content string `json:"content"`
				}{Content: "Hello from LLM"}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := NewOpenAILLMProvider(srv.URL, "llm-key", "gpt-4o", 30)

	msgs := []Message{
		{Role: "user", Content: "Hi"},
	}
	reply, err := p.Complete(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	if reply != "Hello from LLM" {
		t.Errorf("expected 'Hello from LLM', got %q", reply)
	}
}

func TestOpenAILLM_EmptyChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := struct {
			Choices []struct{} `json:"choices"`
		}{}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := NewOpenAILLMProvider(srv.URL, "key", "model", 30)
	_, err := p.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected error for empty choices, got nil")
	}
}

// ---------------------------------------------------------------------------
// AnthropicProvider test (lives here since it is tested via httptest too)
// ---------------------------------------------------------------------------

func TestAnthropicLLM_Complete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Method != http.MethodPost {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		// Verify x-api-key header.
		apiKey := r.Header.Get("x-api-key")
		if apiKey != "ant-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Verify anthropic-version header.
		ver := r.Header.Get("anthropic-version")
		if ver != "2023-06-01" {
			http.Error(w, "bad version", http.StatusBadRequest)
			return
		}

		var req struct {
			Model     string    `json:"model"`
			MaxTokens int       `json:"max_tokens"`
			Messages  []Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		resp := struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}{
			Content: []struct {
				Text string `json:"text"`
			}{
				{Text: "Hello from Anthropic"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	// Override the endpoint URL for testing — pass the full path as the endpoint URL.
	p := newAnthropicProviderWithURL(srv.URL+"/v1/messages", "ant-key", "claude-3-5-sonnet-20241022", 30)

	msgs := []Message{
		{Role: "user", Content: "Hello"},
	}
	reply, err := p.Complete(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	if reply != "Hello from Anthropic" {
		t.Errorf("expected 'Hello from Anthropic', got %q", reply)
	}
}

func TestAnthropicLLM_EmptyContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := struct {
			Content []struct{} `json:"content"`
		}{}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := newAnthropicProviderWithURL(srv.URL+"/v1/messages", "key", "model", 30)
	_, err := p.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected error for empty content, got nil")
	}
}
