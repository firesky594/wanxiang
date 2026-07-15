package executor

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"wanxiang-agent/server/internal/providers"
)

func TestLiveProviderLowVolumeRunner(t *testing.T) {
	if os.Getenv("WANXIANG_LIVE_SMOKE") != "1" {
		t.Skip("set WANXIANG_LIVE_SMOKE=1 for explicit low-volume Provider validation")
	}
	envPath := os.Getenv("WANXIANG_LIVE_AGENT_ENV")
	if envPath == "" {
		t.Fatal("WANXIANG_LIVE_AGENT_ENV is required")
	}
	values, err := loadWorkerEnv(envPath)
	if err != nil {
		t.Fatalf("load target agent env: %v", err)
	}
	files, ref, _ := fileToolsFixture(t)
	_, _ = files.db.Exec(`update tasks set title='M06 低量协议验证',description='不要修改文件。只返回协议版本 1 的 JSON：status 为 completed，summary 为低量验证完成，actions 为空数组，next_action 为等待审核。不要使用 Markdown。' where id=?`, ref.TaskID)
	registry := providers.NewRegistry(&http.Client{Timeout: 30 * time.Second})
	chatter := &shapeRecordingChatter{delegate: NewEnvChatter(registry, values)}
	runner := NewRunner(files.db, chatter, files, NewCheckRunner(files.db, files.leases), nil)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	result, err := runner.Run(ctx, WorkerInput{TaskID: ref.TaskID, StepID: ref.StepID, AgentName: ref.AgentName, LeaseID: ref.LeaseID, LeaseVersion: ref.LeaseVersion})
	if err != nil {
		t.Fatalf("live runner: %s response_shape=%s", Redact(err.Error()), chatter.shape())
	}
	if result.Status != RunCompleted || result.RequestCount > maxProviderRequests || result.InputTokens < 1 || result.OutputTokens < 1 {
		t.Fatalf("unsafe result status=%s requests=%d input_tokens=%d output_tokens=%d", result.Status, result.RequestCount, result.InputTokens, result.OutputTokens)
	}
	secret := values["AGENT_API_KEY"]
	for _, query := range []string{`select coalesce(group_concat(error_summary,' '),'') from executor_runs`, `select coalesce(group_concat(result_summary,' '),'') from executor_actions`, `select coalesce(group_concat(payload_json,' '),'') from runtime_events`} {
		var stored string
		if err := files.db.QueryRow(query).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if secret != "" && strings.Contains(stored, secret) {
			t.Fatal("provider secret persisted")
		}
	}
	t.Logf("live smoke passed status=%s requests=%d input_tokens=%d output_tokens=%d", result.Status, result.RequestCount, result.InputTokens, result.OutputTokens)
}

type shapeRecordingChatter struct {
	delegate AgentChatter
	contents []string
}

func (s *shapeRecordingChatter) ChatAgent(ctx context.Context, name string, messages []providers.Message, maxTokens int) (providers.Result, error) {
	result, err := s.delegate.ChatAgent(ctx, name, messages, maxTokens)
	if err == nil {
		s.contents = append(s.contents, result.Content)
	}
	return result, err
}

func (s *shapeRecordingChatter) shape() string {
	if len(s.contents) == 0 {
		return "none"
	}
	value := strings.TrimSpace(s.contents[len(s.contents)-1])
	first := "empty"
	if value != "" {
		first = string(value[0])
	}
	return fmt.Sprintf("bytes=%d first=%q fenced=%t object_start=%d object_end=%d", len(value), first, strings.HasPrefix(value, "```"), strings.Index(value, "{"), strings.LastIndex(value, "}"))
}
