package agents

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/providers"
	"wanxiang-agent/server/internal/testutil"
)

func TestChatAgentUsesPrivateRuntimeConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-key" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"planned"}}],"usage":{"prompt_tokens":4,"completion_tokens":2}}`))
	}))
	defer server.Close()
	cfg, _ := config.Load(t.TempDir())
	conn := testutil.OpenDB(t)
	svc := NewService(cfg, conn)
	if _, err := svc.SaveAgentConfig(t.Context(), AgentConfigInput{Name: "manager", ProviderType: "openai", BaseURL: server.URL, Model: "planner", APIKey: "private-key"}); err != nil {
		t.Fatal(err)
	}
	result, err := svc.ChatAgent(t.Context(), "manager", []providers.Message{{Role: "user", Content: "plan"}}, 500)
	if err != nil || result.Content != "planned" || result.InputTokens != 4 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestSaveAgentConfigStoresSecretPrivatelyAndPreservesIt(t *testing.T) {
	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	svc := NewService(cfg, conn)

	input := AgentConfigInput{Name: "worker-1", ProviderType: "deepseek", Model: "deepseek-test", APIKey: "secret-one"}
	if _, err := svc.SaveAgentConfig(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(cfg.AgentDir, "worker-1", "env")
	info, err := os.Stat(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}

	input.APIKey = ""
	input.Model = "deepseek-updated"
	view, err := svc.SaveAgentConfig(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !view.SecretConfigured || view.BaseURL != "https://api.deepseek.com" || view.Model != "deepseek-updated" {
		t.Fatalf("view=%+v", view)
	}
	body, _ := os.ReadFile(envPath)
	if !strings.Contains(string(body), "AGENT_API_KEY=secret-one") {
		t.Fatalf("secret was not preserved: %s", body)
	}
	if strings.Contains(string(body), "secret-one\nAGENT_API_KEY") {
		t.Fatal("secret was duplicated")
	}
}

func TestGetAgentConfigRepairsStaleMissingConfigWithoutLosingEnv(t *testing.T) {
	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	svc := NewService(cfg, conn)
	if _, err := svc.SaveAgentConfig(t.Context(), AgentConfigInput{Name: "worker-1", ProviderType: "openai", Model: "gpt-5.2", APIKey: "persisted-secret"}); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`update agent_registry set status='blocked: missing_config' where name='worker-1'`); err != nil {
		t.Fatal(err)
	}
	view, err := svc.GetAgentConfig(t.Context(), "worker-1")
	if err != nil || view.Status != "configured" || !view.SecretConfigured {
		t.Fatalf("view=%+v err=%v", view, err)
	}
	var status string
	_ = conn.QueryRow(`select status from agent_registry where name='worker-1'`).Scan(&status)
	body, _ := os.ReadFile(filepath.Join(cfg.AgentDir, "worker-1", "env"))
	if status != "configured" || !strings.Contains(string(body), "AGENT_API_KEY=persisted-secret") {
		t.Fatalf("status=%q env not preserved", status)
	}
}

func TestGetAgentConfigDoesNotOverwriteRuntimeStatuses(t *testing.T) {
	for _, status := range []string{"online", "blocked: provider_error"} {
		t.Run(status, func(t *testing.T) {
			root := t.TempDir()
			cfg, _ := config.Load(root)
			conn := testutil.OpenDB(t)
			svc := NewService(cfg, conn)
			if _, err := svc.SaveAgentConfig(t.Context(), AgentConfigInput{Name: "worker-1", ProviderType: "openai", Model: "gpt-5.2", APIKey: "test-key"}); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Exec(`update agent_registry set status=? where name='worker-1'`, status); err != nil {
				t.Fatal(err)
			}
			view, err := svc.GetAgentConfig(t.Context(), "worker-1")
			if err != nil || view.Status != status {
				t.Fatalf("status=%q view=%+v err=%v", status, view, err)
			}
		})
	}
}

func TestProbeAgentSelectsConfiguredProviderAndPersistsStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"OK"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer server.Close()

	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	svc := NewService(cfg, conn)
	_, err := svc.SaveAgentConfig(context.Background(), AgentConfigInput{Name: "worker-1", ProviderType: "deepseek", BaseURL: server.URL, Model: "deepseek-test", APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	view, err := svc.ProbeAgent(context.Background(), "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != "online" {
		t.Fatalf("view=%+v", view)
	}
	var status, model string
	if err := conn.QueryRow(`select status,current_model from agent_registry where name='worker-1'`).Scan(&status, &model); err != nil {
		t.Fatal(err)
	}
	if status != "online" || model != "deepseek-test" {
		t.Fatalf("status=%q model=%q", status, model)
	}
}

func TestAgentConfigRejectsUnsupportedProviderAndDoesNotExposeSecret(t *testing.T) {
	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	svc := NewService(cfg, conn)
	if _, err := svc.SaveAgentConfig(context.Background(), AgentConfigInput{Name: "worker-1", ProviderType: "other", Model: "model", APIKey: "secret"}); err == nil {
		t.Fatal("unsupported provider should fail")
	}
	if _, err := svc.SaveAgentConfig(context.Background(), AgentConfigInput{Name: "worker-1", ProviderType: "openai", Model: "model", APIKey: "secret"}); err != nil {
		t.Fatal(err)
	}
	views, err := svc.ListAgentConfigs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || !views[0].SecretConfigured {
		t.Fatalf("views=%+v", views)
	}
	if strings.Contains(fmt.Sprintf("%+v", views), "secret") {
		t.Fatalf("view leaked secret: %+v", views)
	}
}

func TestEnsureManagerCreatesTemplateAndBlocksWhenSecretMissing(t *testing.T) {
	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	svc := NewService(cfg, conn)

	status, err := svc.EnsureManager(context.Background())
	if err != nil {
		t.Fatalf("EnsureManager: %v", err)
	}
	if status.Status != "blocked: missing_secret" {
		t.Fatalf("status=%q", status.Status)
	}
	if strings.Join(status.MissingEnv, ",") != "AGENT_PROVIDER_TYPE,AGENT_API_KEY,AGENT_MODEL" {
		t.Fatalf("MissingEnv=%v", status.MissingEnv)
	}
	if _, err := os.Stat(filepath.Join(cfg.AgentDir, "manager", "agent.yaml")); err != nil {
		t.Fatalf("agent.yaml not created: %v", err)
	}
	gitignore, err := os.ReadFile(filepath.Join(cfg.AgentDir, "manager", ".gitignore"))
	if err != nil {
		t.Fatalf(".gitignore missing: %v", err)
	}
	if !strings.Contains(string(gitignore), "env") {
		t.Fatalf(".gitignore must ignore env")
	}
}

func TestHeartbeatAndTokenUsagePublishPersistedEvents(t *testing.T) {
	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	svc := NewService(cfg, conn)

	if err := svc.WriteMemory(context.Background(), "worker-1", "notes/private.md", "secret-content"); err != nil {
		t.Fatalf("WriteMemory: %v", err)
	}
	if err := svc.WriteLog(context.Background(), "worker-1", "runtime/private.log", "secret-content"); err != nil {
		t.Fatalf("WriteLog: %v", err)
	}
	if err := svc.Heartbeat(context.Background(), HeartbeatInput{Name: "worker-1", Role: "worker", Status: "online", CurrentModel: "local-model"}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if err := svc.RecordTokenUsage(context.Background(), TokenUsageInput{AgentName: "worker-1", Model: "local-model", InputTokens: 12, OutputTokens: 7}); err != nil {
		t.Fatalf("RecordTokenUsage: %v", err)
	}

	rows, err := conn.Query(`select event_type,actor,payload_json from runtime_events order by id`)
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var eventType, actor, payload string
		if err := rows.Scan(&eventType, &actor, &payload); err != nil {
			t.Fatalf("scan event: %v", err)
		}
		if actor != "worker-1" {
			t.Fatalf("actor=%q", actor)
		}
		if strings.Contains(payload, "secret-content") {
			t.Fatalf("event contains secret content: %s", payload)
		}
		got = append(got, eventType)
	}
	if strings.Join(got, ",") != "agent.heartbeat,token.usage" {
		t.Fatalf("events=%v", got)
	}
}

func TestLegacyManagerSecretStillRequiresProviderAndModel(t *testing.T) {
	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	svc := NewService(cfg, conn)
	if _, err := svc.EnsureManager(context.Background()); err != nil {
		t.Fatalf("EnsureManager: %v", err)
	}
	if err := svc.SaveManagerSecret(context.Background(), "MANAGER_API_KEY", "abc123"); err != nil {
		t.Fatalf("SaveManagerSecret: %v", err)
	}
	status, err := svc.EnsureManager(context.Background())
	if err != nil {
		t.Fatalf("EnsureManager second: %v", err)
	}
	if status.Status != "blocked: missing_secret" {
		t.Fatalf("status=%q", status.Status)
	}
	if strings.Join(status.MissingEnv, ",") != "AGENT_PROVIDER_TYPE,AGENT_MODEL" {
		t.Fatalf("MissingEnv=%v", status.MissingEnv)
	}
}
