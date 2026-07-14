package providers

import (
	"encoding/json"
	"fmt"
	"strings"
)

func decodeChatResult(providerType string, body []byte) (Result, error) {
	var response struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return Result{}, fmt.Errorf("%s returned invalid JSON", providerType)
	}
	if len(response.Choices) == 0 {
		return Result{}, fmt.Errorf("%s returned no choices", providerType)
	}
	return Result{Content: response.Choices[0].Message.Content, InputTokens: response.Usage.PromptTokens, OutputTokens: response.Usage.CompletionTokens}, nil
}

func providerError(body []byte) string {
	var response struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &response) == nil && strings.TrimSpace(response.Error.Message) != "" {
		return response.Error.Message
	}
	return "provider request failed"
}
