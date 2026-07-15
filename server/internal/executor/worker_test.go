package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"wanxiang-agent/server/internal/leases"
	"wanxiang-agent/server/internal/providers"
)

type fakeWorkerRunner struct {
	result WorkerResult
	err    error
	wait   bool
}

func (f *fakeWorkerRunner) Run(ctx context.Context, _ WorkerInput) (WorkerResult, error) {
	if f.wait {
		<-ctx.Done()
		return WorkerResult{Status: RunInterrupted}, ctx.Err()
	}
	return f.result, f.err
}

type fakeHeartbeater struct {
	calls atomic.Int32
	err   error
}

func (f *fakeHeartbeater) Heartbeat(context.Context, leases.LeaseRef) (leases.Lease, error) {
	f.calls.Add(1)
	return leases.Lease{}, f.err
}

type fakeShutdownCheckpoint struct{ calls atomic.Int32 }

func (f *fakeShutdownCheckpoint) CreateGitCheckpoint(context.Context, leases.LeaseRef, WorkerSummary) (leases.Checkpoint, error) {
	f.calls.Add(1)
	return leases.Checkpoint{}, nil
}

func TestWorkerReadsStructuredInputAndWritesResult(t *testing.T) {
	input := WorkerInput{TaskID: 1, StepID: 2, AgentName: "agent-a", LeaseID: "lease", LeaseVersion: 1}
	encoded, _ := json.Marshal(input)
	var out bytes.Buffer
	runner := &fakeWorkerRunner{result: WorkerResult{Status: RunCompleted, Summary: "完成"}}
	err := RunWorker(t.Context(), bytes.NewReader(encoded), &out, runner, &fakeHeartbeater{}, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"status":"completed"`) {
		t.Fatalf("out=%q", out.String())
	}
}

func TestWorkerHeartbeatConflictStopsAndSignalCheckpoints(t *testing.T) {
	input := `{"task_id":1,"step_id":2,"agent_name":"agent-a","lease_id":"lease","lease_version":1,"server_url":"http://127.0.0.1","agent_token":"token"}`
	hb := &fakeHeartbeater{err: leases.ErrConflict}
	checkpoint := &fakeShutdownCheckpoint{}
	ctx, cancel := context.WithCancel(t.Context())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	err := RunWorker(ctx, strings.NewReader(input), &bytes.Buffer{}, &fakeWorkerRunner{wait: true}, hb, checkpoint, time.Millisecond)
	if err == nil {
		t.Fatal("expected interrupted worker")
	}
	if hb.calls.Load() == 0 {
		t.Fatal("heartbeat not called")
	}
	if checkpoint.calls.Load() != 1 {
		t.Fatalf("checkpoint calls=%d", checkpoint.calls.Load())
	}
}

func TestWorkerCommandUsesOnlyInternalModeAndTargetEnv(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	cmd := NewWorkerCommand("/opt/wanxiang-agent", file, map[string]string{"AGENT_API_KEY": "private", "AGENT_MODEL": "model", "AGENT_PROVIDER_TYPE": "openai"})
	joined := strings.Join(cmd.Args, " ")
	if strings.Contains(joined, "private") || strings.Contains(joined, "codex") || strings.Contains(joined, "opencode") || strings.Contains(joined, "sh -c") {
		t.Fatalf("args=%q", joined)
	}
	if joined != "/opt/wanxiang-agent agent-worker --input-fd 3" {
		t.Fatalf("args=%q", joined)
	}
	if len(cmd.ExtraFiles) != 1 {
		t.Fatalf("extra files=%d", len(cmd.ExtraFiles))
	}
}

func TestEnvChatterRequiresOwnAgentEnv(t *testing.T) {
	chatter := NewEnvChatter(providers.NewRegistry(nil), map[string]string{"AGENT_PROVIDER_TYPE": "openai", "AGENT_MODEL": "m"})
	_, err := chatter.ChatAgent(t.Context(), "agent-a", nil, 1)
	if err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("err=%v", err)
	}
	if !errors.Is(err, ErrMissingWorkerConfig) {
		t.Fatalf("err=%v", err)
	}
}
