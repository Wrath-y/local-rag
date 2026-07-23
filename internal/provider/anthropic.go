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

// CompleteWithTools adapts the portable Agent contracts to Anthropic's
// tool_use/tool_result content blocks.
func (p *AnthropicProvider) CompleteWithTools(ctx context.Context, messages []Message, tools []ToolDefinition) (Completion, error) {
	type block struct {
		Type      string          `json:"type"`
		Text      string          `json:"text,omitempty"`
		ID        string          `json:"id,omitempty"`
		Name      string          `json:"name,omitempty"`
		Input     json.RawMessage `json:"input,omitempty"`
		ToolUseID string          `json:"tool_use_id,omitempty"`
		Content   string          `json:"content,omitempty"`
	}
	type message struct {
		Role    string  `json:"role"`
		Content []block `json:"content"`
	}
	type tool struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema"`
	}
	requestMessages := make([]message, 0, len(messages))
	for _, input := range messages {
		if input.Role == "tool" {
			requestMessages = append(requestMessages, message{Role: "user", Content: []block{{Type: "tool_result", ToolUseID: input.ToolCallID, Content: input.Content}}})
			continue
		}
		output := message{Role: input.Role}
		if input.Content != "" {
			output.Content = append(output.Content, block{Type: "text", Text: input.Content})
		}
		for _, call := range input.ToolCalls {
			output.Content = append(output.Content, block{Type: "tool_use", ID: call.ID, Name: call.Name, Input: call.Arguments})
		}
		requestMessages = append(requestMessages, output)
	}
	requestTools := make([]tool, 0, len(tools))
	for _, definition := range tools {
		requestTools = append(requestTools, tool{Name: definition.Name, Description: definition.Description, InputSchema: definition.InputSchema})
	}
	body, err := json.Marshal(struct {
		Model     string    `json:"model"`
		MaxTokens int       `json:"max_tokens"`
		Messages  []message `json:"messages"`
		Tools     []tool    `json:"tools"`
	}{p.model, anthropicMaxTokens, requestMessages, requestTools})
	if err != nil {
		return Completion{}, fmt.Errorf("anthropic tools: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpointURL, bytes.NewReader(body))
	if err != nil {
		return Completion{}, fmt.Errorf("anthropic tools: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	resp, err := p.client.Do(req)
	if err != nil {
		return Completion{}, fmt.Errorf("anthropic tools: http: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return Completion{}, fmt.Errorf("anthropic tools: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Completion{}, fmt.Errorf("anthropic tools: API returned %d: %s", resp.StatusCode, responseBody)
	}
	var response struct {
		Content []block `json:"content"`
	}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return Completion{}, fmt.Errorf("anthropic tools: unmarshal response: %w", err)
	}
	if len(response.Content) == 0 {
		return Completion{}, fmt.Errorf("anthropic tools: no content blocks in response")
	}
	var completion Completion
	for _, item := range response.Content {
		if item.Type == "tool_use" {
			completion.ToolCalls = append(completion.ToolCalls, ToolCall{ID: item.ID, Name: item.Name, Arguments: item.Input})
		} else if item.Type == "text" {
			completion.Content += item.Text
		}
	}
	return completion, nil
}
