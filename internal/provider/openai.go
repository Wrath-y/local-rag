package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ---------------------------------------------------------------------------
// OpenAIEmbedProvider — OpenAI-compatible /embeddings endpoint.
// ---------------------------------------------------------------------------

// OpenAIEmbedProvider implements EmbedProvider using an OpenAI-compatible API.
type OpenAIEmbedProvider struct {
	baseURL string
	apiKey  string
	model   string
	dims    int
	client  *http.Client
}

// NewOpenAIEmbedProvider creates an OpenAIEmbedProvider.
func NewOpenAIEmbedProvider(baseURL, apiKey, model string, dims int) *OpenAIEmbedProvider {
	return &OpenAIEmbedProvider{
		baseURL: baseURL,
		apiKey:  apiKey,
		model:   model,
		dims:    dims,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Embed sends texts to the OpenAI-compatible embeddings endpoint.
func (p *OpenAIEmbedProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	reqBody := struct {
		Input []string `json:"input"`
		Model string   `json:"model"`
	}{Input: texts, Model: p.model}

	b, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("openai embed: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/embeddings", bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("openai embed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai embed: http: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai embed: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai embed: API returned %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("openai embed: unmarshal response: %w", err)
	}

	vecs := make([][]float32, len(texts))
	for _, item := range result.Data {
		if item.Index < 0 || item.Index >= len(vecs) {
			return nil, fmt.Errorf("openai embed: response index %d out of range", item.Index)
		}
		vecs[item.Index] = item.Embedding
	}
	return vecs, nil
}

// Dims returns the configured embedding dimensionality.
func (p *OpenAIEmbedProvider) Dims() int { return p.dims }

// ---------------------------------------------------------------------------
// OpenAIRerankProvider — Cohere/Jina-style /rerank endpoint.
// ---------------------------------------------------------------------------

// OpenAIRerankProvider implements RerankProvider using a Cohere/Jina-compatible API.
type OpenAIRerankProvider struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

// NewOpenAIRerankProvider creates an OpenAIRerankProvider.
func NewOpenAIRerankProvider(baseURL, apiKey, model string) *OpenAIRerankProvider {
	return &OpenAIRerankProvider{
		baseURL: baseURL,
		apiKey:  apiKey,
		model:   model,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// Rerank sends documents to the rerank endpoint and returns sorted results.
func (p *OpenAIRerankProvider) Rerank(ctx context.Context, query string, documents []string, topN int) ([]RerankResult, error) {
	reqBody := struct {
		Query     string   `json:"query"`
		Documents []string `json:"documents"`
		Model     string   `json:"model"`
		TopN      int      `json:"top_n"`
	}{Query: query, Documents: documents, Model: p.model, TopN: topN}

	b, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("openai rerank: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/rerank", bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("openai rerank: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai rerank: http: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai rerank: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai rerank: API returned %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Results []struct {
			Index          int     `json:"index"`
			RelevanceScore float64 `json:"relevance_score"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("openai rerank: unmarshal response: %w", err)
	}

	out := make([]RerankResult, len(result.Results))
	for i, r := range result.Results {
		out[i] = RerankResult{Index: r.Index, RelevanceScore: r.RelevanceScore}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// OpenAILLMProvider — OpenAI-compatible /chat/completions endpoint.
// ---------------------------------------------------------------------------

// OpenAILLMProvider implements LLMProvider using an OpenAI-compatible API.
type OpenAILLMProvider struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

// NewOpenAILLMProvider creates an OpenAILLMProvider.
// timeout is in seconds; ≤0 means no explicit timeout.
func NewOpenAILLMProvider(baseURL, apiKey, model string, timeout int) *OpenAILLMProvider {
	d := time.Duration(timeout) * time.Second
	if d <= 0 {
		d = 60 * time.Second
	}
	return &OpenAILLMProvider{
		baseURL: baseURL,
		apiKey:  apiKey,
		model:   model,
		client:  &http.Client{Timeout: d},
	}
}

// Complete sends messages to the chat completions endpoint and returns the reply.
func (p *OpenAILLMProvider) Complete(ctx context.Context, messages []Message) (string, error) {
	reqBody := struct {
		Model    string    `json:"model"`
		Messages []Message `json:"messages"`
	}{Model: p.model, Messages: messages}

	b, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("openai llm: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return "", fmt.Errorf("openai llm: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("openai llm: http: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("openai llm: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("openai llm: API returned %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("openai llm: unmarshal response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("openai llm: no choices in response")
	}
	return result.Choices[0].Message.Content, nil
}
