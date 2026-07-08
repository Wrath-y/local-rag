package provider

import (
	"fmt"
	"os"

	"github.com/Wrath-y/local-rag/internal/config"
)

// NewEmbedProvider constructs an EmbedProvider based on cfg.
// sidecarURL is the base URL for the local Python sidecar (e.g. "http://127.0.0.1:8766").
func NewEmbedProvider(cfg config.EmbeddingConfig, sidecarURL string) (EmbedProvider, error) {
	switch cfg.Provider {
	case "local", "":
		return NewLocalEmbedProvider(sidecarURL, cfg.Model, cfg.Dims), nil
	case "openai", "custom":
		apiKey := os.Getenv(cfg.APIKeyEnv)
		if apiKey == "" {
			return nil, fmt.Errorf("embed provider %q: env var %q is empty", cfg.Provider, cfg.APIKeyEnv)
		}
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		return NewOpenAIEmbedProvider(baseURL, apiKey, cfg.Model, cfg.Dims), nil
	default:
		return nil, fmt.Errorf("unknown embed provider %q", cfg.Provider)
	}
}

// NewRerankProvider constructs a RerankProvider based on cfg.
// Returns (nil, nil) when the provider is "disabled".
func NewRerankProvider(cfg config.RerankConfig, sidecarURL string) (RerankProvider, error) {
	switch cfg.Provider {
	case "disabled", "":
		return nil, nil
	case "local":
		return NewLocalRerankProvider(sidecarURL, cfg.Model), nil
	case "cohere", "jina", "custom":
		apiKey := os.Getenv(cfg.APIKeyEnv)
		if apiKey == "" {
			return nil, fmt.Errorf("rerank provider %q: env var %q is empty", cfg.Provider, cfg.APIKeyEnv)
		}
		baseURL := cfg.BaseURL
		if baseURL == "" {
			return nil, fmt.Errorf("rerank provider %q: base_url must be set", cfg.Provider)
		}
		return NewOpenAIRerankProvider(baseURL, apiKey, cfg.Model), nil
	default:
		return nil, fmt.Errorf("unknown rerank provider %q", cfg.Provider)
	}
}

// NewLLMProvider constructs an LLMProvider based on cfg.
func NewLLMProvider(cfg config.LLMConfig) (LLMProvider, error) {
	apiKey := os.Getenv(cfg.APIKeyEnv)
	if apiKey == "" {
		return nil, fmt.Errorf("llm provider %q: env var %q is empty", cfg.Provider, cfg.APIKeyEnv)
	}

	switch cfg.Provider {
	case "openai":
		return NewOpenAILLMProvider("https://api.openai.com/v1", apiKey, cfg.Model, cfg.Timeout), nil
	case "anthropic":
		return NewAnthropicProvider(apiKey, cfg.Model, cfg.Timeout), nil
	default:
		return nil, fmt.Errorf("unknown llm provider %q", cfg.Provider)
	}
}
