package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type OpenAI struct{ client *http.Client }

func NewOpenAI(client *http.Client) *OpenAI { return &OpenAI{client: client} }

func (p *OpenAI) Chat(ctx context.Context, cfg Config, messages []Message, maxTokens int) (Result, error) {
	if err := validateConfig(cfg); err != nil {
		return Result{}, err
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL(TypeOpenAI)
	}
	payload := struct {
		Model               string    `json:"model"`
		Messages            []Message `json:"messages"`
		MaxCompletionTokens int       `json:"max_completion_tokens,omitempty"`
	}{Model: cfg.Model, Messages: messages, MaxCompletionTokens: maxTokens}
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	return p.do(req)
}

func (p *OpenAI) do(req *http.Request) (Result, error) {
	res, err := p.client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("openai request failed: %w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return Result{}, fmt.Errorf("openai response read failed: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Result{}, fmt.Errorf("openai returned HTTP %d: %s", res.StatusCode, providerError(body))
	}
	return decodeChatResult("openai", body)
}
