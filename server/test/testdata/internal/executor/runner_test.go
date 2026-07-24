package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wanxiang-agent/server/internal/providers"
)

type fakeChatter struct {
	responses []providers.Result
	err       error
	names     []string
	messages  [][]providers.Message
	calls     int
}

func (f *fakeChatter) ChatAgent(_ context.Context, name string, messages []providers.Message, _ int) (providers.Result, error) {
	f.names = append(f.names, name)
	f.messages = append(f.messages, messages)
	f.calls++
	if f.err != nil {
		return providers.Result{}, f.err
	}
	if len(f.responses) == 0 {
		return providers.Result{}, errors.New("no response")
	}
	result := f.responses[0]
	f.responses = f.responses[1:]
	return result, nil
}

func TestRunnerExecutesOrderedActionsAndAccountsTokens(t *testing.T) {
	files, ref, root := fileToolsFixture(t)
	chat := &fakeChatter{responses: []providers.Result{
		{Content: `{"version":1,"status":"continue","summary":"写入并读取文件","actions":[{"type":"write_file","path":"src/main.go","content":"package src\n"},{"type":"read_file","path":"src/main.go"}],"next_action":"确认结果"}`, InputTokens: 10, OutputTokens: 5},
		{Content: `{"version":1,"status":"completed","summary":"实现完成","actions":[],"next_action":"等待审核"}`, InputTokens: 7, OutputTokens: 3},
	}}
	runner := NewRunner(files.db, chat, files, NewCheckRunner(files.db, files.leases), nil)
	result, err := runner.Run(t.Context(), WorkerInput{TaskID: ref.TaskID, StepID: ref.StepID, AgentName: ref.AgentName, LeaseID: ref.LeaseID, LeaseVersion: ref.LeaseVersion, AgentToken: "must-not-persist"})
	if err != nil || result.Status != RunCompleted || result.RequestCount != 2 || result.InputTokens != 17 || result.OutputTokens != 8 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	content, _ := os.ReadFile(filepath.Join(root, "src", "main.go"))
	if string(content) != "package src\n" {
		t.Fatalf("content=%q", content)
	}
	var stored string
	_ = files.db.QueryRow(`select group_concat(result_summary,' ') from executor_actions`).Scan(&stored)
	if strings.Contains(stored, "package src") || strings.Contains(stored, "must-not-persist") {
		t.Fatalf("stored=%q", stored)
	}
}

func TestRunnerUsesTargetAgentAndStopsAtThreeRequests(t *testing.T) {
	files, ref, _ := fileToolsFixture(t)
	response := providers.Result{Content: `{"version":1,"status":"continue","summary":"继续分析","actions":[],"next_action":"继续"}`}
	chat := &fakeChatter{responses: []providers.Result{response, response, response, response}}
	result, err := NewRunner(files.db, chat, files, NewCheckRunner(files.db, files.leases), nil).Run(t.Context(), WorkerInput{TaskID: ref.TaskID, StepID: ref.StepID, AgentName: "agent-a", LeaseID: ref.LeaseID, LeaseVersion: ref.LeaseVersion})
	if err == nil || result.RequestCount != 3 || chat.calls != 3 {
		t.Fatalf("result=%+v calls=%d err=%v", result, chat.calls, err)
	}
	for _, name := range chat.names {
		if name != "agent-a" {
			t.Fatalf("fallback agent=%q", name)
		}
	}
	if len(chat.messages) == 0 || !strings.Contains(chat.messages[0][0].Content, `{"version":1,"status":"continue|checkpoint|completed|blocked"`) {
		t.Fatal("system prompt lacks exact protocol contract")
	}
}

func TestRunnerMapsConfigAndInvalidJSONErrors(t *testing.T) {
	files, ref, _ := fileToolsFixture(t)
	for _, tc := range []struct {
		name string
		chat *fakeChatter
		want RunStatus
	}{
		{"missing", &fakeChatter{err: errors.New("agent provider_type, api_key, and model are required")}, RunStopped},
		{"json", &fakeChatter{responses: []providers.Result{{Content: "not-json"}}}, RunFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := NewRunner(files.db, tc.chat, files, NewCheckRunner(files.db, files.leases), nil).Run(t.Context(), WorkerInput{TaskID: ref.TaskID, StepID: ref.StepID, AgentName: ref.AgentName, LeaseID: ref.LeaseID, LeaseVersion: ref.LeaseVersion})
			if err == nil || result.Status != tc.want {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestAgentContextRedactsMultilineSecrets(t *testing.T) {
	input := `safe-before
api_key: |
  first-secret-line
  second-secret-line
safe-middle
token:
  yaml-token-value
"token":
  quoted-token-value
"token": [
"json-token-value"
]
TOKEN=first-part\
second-part
-----BEGIN PRIVATE KEY-----
private-key-body
-----END PRIVATE KEY-----
safe-after`
	got := redactAgentContext(input)
	for _, secret := range []string{"first-secret-line", "second-secret-line", "yaml-token-value", "quoted-token-value", "json-token-value", "first-part", "second-part", "private-key-body"} {
		if strings.Contains(got, secret) {
			t.Fatalf("multiline secret leaked: %q in %q", secret, got)
		}
	}
	for _, safe := range []string{"safe-before", "safe-middle", "safe-after"} {
		if !strings.Contains(got, safe) {
			t.Fatalf("safe context lost: %q in %q", safe, got)
		}
	}
}

func TestProviderToolResultIsRedactedBeforeModelReuse(t *testing.T) {
	got := redactProviderToolResult("safe\nAuthorization: Bearer raw-token\napi_key: |\n  multiline-value\n\"auth\":\"registry-secret\"\nhttps://user:password@example.test/path")
	for _, secret := range []string{"raw-token", "multiline-value", "registry-secret", "user", "password"} {
		if strings.Contains(got, secret) {
			t.Fatalf("tool result leaked %q in %q", secret, got)
		}
	}
	if !strings.Contains(got, "safe") {
		t.Fatalf("safe tool output lost: %q", got)
	}
}
