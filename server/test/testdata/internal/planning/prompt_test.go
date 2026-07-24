package planning

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wanxiang-agent/server/internal/tasks"
)

func TestBuildMessagesIncludesRulesTaskAndSchema(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "system_prompt.md"), []byte("manager rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	messages, err := BuildMessages(dir, tasks.Task{ID: 7, Title: "Build auth", Description: "No secrets", Status: "created"})
	if err != nil {
		t.Fatal(err)
	}
	joined := messages[0].Content + messages[1].Content
	for _, want := range []string{"manager rules", "Build auth", "acceptance_criteria", "depends_on"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("messages=%+v missing=%q", messages, want)
		}
	}
	if messages[0].Role != "system" || messages[1].Role != "user" {
		t.Fatalf("messages=%+v", messages)
	}
}

func TestManagerMemoryWhitelistRedactionAndConditionFingerprint(t *testing.T) {
	root := t.TempDir()
	managerDir := filepath.Join(root, "manager")
	for _, relative := range []string{"memory/summaries", "memory/decisions", "memory/task-notes"} {
		if err := os.MkdirAll(filepath.Join(managerDir, relative), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(managerDir, "system_prompt.md"), []byte("manager rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(managerDir, "memory", "summaries", "governance.md"), []byte(
		"api_key:\n  value: split-secret-value\nstatus: healthy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runtimePath := filepath.Join(managerDir, "memory", "summaries", "runtime-status.md")
	if err := os.WriteFile(runtimePath, []byte("RUNTIME_STATUS_MUST_NOT_ENTER_PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}
	decisionPath := filepath.Join(managerDir, "memory", "decisions", "decision.md")
	if err := os.WriteFile(decisionPath, []byte(
		"credential sk-abcdefgh12345678\njwt abcdefghij.abcdefghij.abcdefghij"), 0o644); err != nil {
		t.Fatal(err)
	}
	taskNotePath := filepath.Join(managerDir, "memory", "task-notes", "private.md")
	if err := os.WriteFile(taskNotePath, []byte("TASK_NOTE_MUST_NOT_ENTER_PROMPT"), 0o644); err != nil {
		t.Fatal(err)
	}

	messages, err := BuildMessages(managerDir, tasks.Task{ID: 7, Title: "task", Description: "description", Status: "created"})
	if err != nil {
		t.Fatal(err)
	}
	system := messages[0].Content
	for _, forbidden := range []string{
		"split-secret-value",
		"sk-abcdefgh12345678",
		"abcdefghij.abcdefghij.abcdefghij",
		"RUNTIME_STATUS_MUST_NOT_ENTER_PROMPT",
		"TASK_NOTE_MUST_NOT_ENTER_PROMPT",
	} {
		if strings.Contains(system, forbidden) {
			t.Fatalf("manager prompt leaked %q", forbidden)
		}
	}
	if !strings.Contains(system, "status: healthy") {
		t.Fatal("normal field after a redacted YAML block was removed")
	}

	envPath := filepath.Join(managerDir, "env")
	firstEnv := "AGENT_PROVIDER_TYPE=openai\nAGENT_API_KEY=secret-one-123\nAGENT_MODEL=model\n"
	secondEnv := "AGENT_PROVIDER_TYPE=openai\nAGENT_API_KEY=secret-two-456\nAGENT_MODEL=model\n"
	if len(firstEnv) != len(secondEnv) {
		t.Fatal("test secrets must have equal length")
	}
	fixedTime := time.Unix(1_700_000_000, 0)
	if err := os.WriteFile(envPath, []byte(firstEnv), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(envPath, fixedTime, fixedTime); err != nil {
		t.Fatal(err)
	}
	initial := planningConditionFingerprint(managerDir)

	if err := os.WriteFile(runtimePath, []byte("runtime changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(taskNotePath, []byte("task note changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := planningConditionFingerprint(managerDir); got != initial {
		t.Fatal("excluded runtime/task-note memory changed the planning condition")
	}
	if err := os.WriteFile(envPath, []byte(secondEnv), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(envPath, fixedTime, fixedTime); err != nil {
		t.Fatal(err)
	}
	if got := planningConditionFingerprint(managerDir); got != initial {
		t.Fatal("raw API key bytes changed the planning condition")
	}
	if err := os.WriteFile(decisionPath, []byte("retry policy changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := planningConditionFingerprint(managerDir); got == initial {
		t.Fatal("whitelisted governance decision did not change the planning condition")
	}
}

func TestRedactMemoryStructuredMultilineSecrets(t *testing.T) {
	input := `"api_key":
  yaml-secret-value-123456
status: healthy
cookie:
- indentationless-secret-value-123456
yaml_status: healthy
client_secret:
  prefixed-secret-value-123456
prefixed_status: healthy
"token": [
  "json-secret-value-123456",
]
json_status: healthy
password = """
toml-secret-value-123456
"""
toml_status = healthy
authorization=first-secret-part\
second-secret-part
shell_status=healthy
private_key = {
  "value": "unclosed-secret-value-123456"`
	got := redactMemory(input)
	for _, forbidden := range []string{
		"yaml-secret-value-123456",
		"indentationless-secret-value-123456",
		"prefixed-secret-value-123456",
		"json-secret-value-123456",
		"toml-secret-value-123456",
		"first-secret-part",
		"second-secret-part",
		"unclosed-secret-value-123456",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("memory secret leaked: %s", got)
		}
	}
	for _, visible := range []string{"status: healthy", "yaml_status: healthy", "prefixed_status: healthy", "json_status: healthy", "toml_status = healthy", "shell_status=healthy"} {
		if !strings.Contains(got, visible) {
			t.Fatalf("normal content %q was removed: %s", visible, got)
		}
	}
}
