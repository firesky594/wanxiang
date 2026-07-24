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

type DeepSeek struct{ client *http.Client }

// NewDeepSeek 创建 DeepSeek 模型客户端。
func NewDeepSeek(client *http.Client) *DeepSeek { return &DeepSeek{client: client} }

// Chat 调用 DeepSeek 对话接口并解析用量。
func (p *DeepSeek) Chat(ctx context.Context, cfg Config, messages []Message, maxTokens int) (Result, error) {
	if err := validateConfig(cfg); err != nil {
		return Result{}, err
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL(TypeDeepSeek)
	}
	payload := struct {
		Model     string    `json:"model"`
		Messages  []Message `json:"messages"`
		MaxTokens int       `json:"max_tokens,omitempty"`
	}{Model: cfg.Model, Messages: messages, MaxTokens: maxTokens}
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
	res, err := p.client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("deepseek request failed: %w", err)
	}
	defer res.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return Result{}, fmt.Errorf("deepseek response read failed: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Result{}, fmt.Errorf("deepseek returned HTTP %d: %s", res.StatusCode, providerError(responseBody))
	}
	return decodeChatResult("deepseek", responseBody)
}
