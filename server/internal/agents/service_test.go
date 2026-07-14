package agents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/testutil"
)

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
	if len(status.MissingEnv) != 1 || status.MissingEnv[0] != "MANAGER_API_KEY" {
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

func TestSaveManagerSecretUnblocksManager(t *testing.T) {
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
	if status.Status != "online" {
		t.Fatalf("status=%q", status.Status)
	}
}
