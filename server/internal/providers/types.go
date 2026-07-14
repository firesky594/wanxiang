package providers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	TypeOpenAI   = "openai"
	TypeDeepSeek = "deepseek"
)

type Config struct {
	APIKey  string
	BaseURL string
	Model   string
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Result struct {
	Content      string
	InputTokens  int64
	OutputTokens int64
}

type Provider interface {
	Chat(ctx context.Context, cfg Config, messages []Message, maxTokens int) (Result, error)
}

type Registry struct {
	providers map[string]Provider
}

func NewRegistry(client *http.Client) *Registry {
	if client == nil {
		client = http.DefaultClient
	}
	return &Registry{providers: map[string]Provider{
		TypeOpenAI:   NewOpenAI(client),
		TypeDeepSeek: NewDeepSeek(client),
	}}
}

func (r *Registry) Get(providerType string) (Provider, error) {
	provider, ok := r.providers[strings.ToLower(strings.TrimSpace(providerType))]
	if !ok {
		return nil, fmt.Errorf("unsupported provider type %q", providerType)
	}
	return provider, nil
}

func DefaultBaseURL(providerType string) string {
	switch strings.ToLower(strings.TrimSpace(providerType)) {
	case TypeOpenAI:
		return "https://api.openai.com/v1"
	case TypeDeepSeek:
		return "https://api.deepseek.com"
	default:
		return ""
	}
}

func validateConfig(cfg Config) error {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return errors.New("api key is required")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return errors.New("model is required")
	}
	return nil
}
