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

const anthropicMessagesEndpoint = "https://api.anthropic.com/v1/messages"
const anthropicVersion = "2023-06-01"
const anthropicMaxTokens = 4096

// ---------------------------------------------------------------------------
// AnthropicProvider — Anthropic Messages API.
// ---------------------------------------------------------------------------

// AnthropicProvider implements LLMProvider using the Anthropic Messages API.
type AnthropicProvider struct {
	endpointURL string
	apiKey      string
	model       string
	client      *http.Client
}

// NewAnthropicProvider creates an AnthropicProvider that calls api.anthropic.com.
// timeout is in seconds; ≤0 means no explicit timeout (defaults to 60s).
func NewAnthropicProvider(apiKey, model string, timeout int) *AnthropicProvider {
	return newAnthropicProviderWithURL(anthropicMessagesEndpoint, apiKey, model, timeout)
}

// newAnthropicProviderWithURL is the internal constructor that accepts a custom
// endpoint URL, enabling test servers to be injected.
func newAnthropicProviderWithURL(endpointURL, apiKey, model string, timeout int) *AnthropicProvider {
	d := time.Duration(timeout) * time.Second
	if d <= 0 {
		d = 60 * time.Second
	}
	return &AnthropicProvider{
		endpointURL: endpointURL,
		apiKey:      apiKey,
		model:       model,
		client:      &http.Client{Timeout: d},
	}
}

// Complete sends messages to the Anthropic Messages API and returns the reply.
func (p *AnthropicProvider) Complete(ctx context.Context, messages []Message) (string, error) {
	reqBody := struct {
		Model     string    `json:"model"`
		MaxTokens int       `json:"max_tokens"`
		Messages  []Message `json:"messages"`
	}{Model: p.model, MaxTokens: anthropicMaxTokens, Messages: messages}

	b, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("anthropic: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpointURL, bytes.NewReader(b))
	if err != nil {
		return "", fmt.Errorf("anthropic: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("anthropic: http: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("anthropic: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("anthropic: API returned %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("anthropic: unmarshal response: %w", err)
	}
	if len(result.Content) == 0 {
		return "", fmt.Errorf("anthropic: no content blocks in response")
	}
	return result.Content[0].Text, nil
}
