package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIChatUsesConfiguredEndpointAndParsesUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer openai-key" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "gpt-test" || body["max_completion_tokens"] != float64(1) {
			t.Fatalf("body=%v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"OK"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`))
	}))
	defer server.Close()

	result, err := NewOpenAI(server.Client()).Chat(context.Background(), Config{APIKey: "openai-key", BaseURL: server.URL + "/v1", Model: "gpt-test"}, []Message{{Role: "user", Content: "ping"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "OK" || result.InputTokens != 3 || result.OutputTokens != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestDeepSeekChatUsesItsProtocolPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "deepseek-test" || body["max_tokens"] != float64(1) {
			t.Fatalf("body=%v", body)
		}
		if _, exists := body["max_completion_tokens"]; exists {
			t.Fatalf("deepseek payload contains OpenAI token field: %v", body)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"OK"}}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`))
	}))
	defer server.Close()

	result, err := NewDeepSeek(server.Client()).Chat(context.Background(), Config{APIKey: "deepseek-key", BaseURL: server.URL, Model: "deepseek-test"}, []Message{{Role: "user", Content: "ping"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "OK" {
		t.Fatalf("result=%+v", result)
	}
}

func TestProviderErrorsAreUsefulAndDoNotLeakAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid authentication"}}`))
	}))
	defer server.Close()

	_, err := NewOpenAI(server.Client()).Chat(context.Background(), Config{APIKey: "do-not-leak", BaseURL: server.URL, Model: "gpt-test"}, []Message{{Role: "user", Content: "ping"}}, 1)
	if err == nil || !strings.Contains(err.Error(), "invalid authentication") {
		t.Fatalf("err=%v", err)
	}
	if strings.Contains(err.Error(), "do-not-leak") {
		t.Fatalf("error leaked key: %v", err)
	}
}

func TestRegistryKeepsProvidersSeparateAndDefinesDefaults(t *testing.T) {
	registry := NewRegistry(http.DefaultClient)
	if _, err := registry.Get("openai"); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Get("deepseek"); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Get("unknown"); err == nil {
		t.Fatal("unknown provider should fail")
	}
	if DefaultBaseURL("openai") != "https://api.openai.com/v1" {
		t.Fatalf("openai default=%q", DefaultBaseURL("openai"))
	}
	if DefaultBaseURL("deepseek") != "https://api.deepseek.com" {
		t.Fatalf("deepseek default=%q", DefaultBaseURL("deepseek"))
	}
}
