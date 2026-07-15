package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"wanxiang-agent/server/internal/leases"
	"wanxiang-agent/server/internal/providers"
)

var ErrMissingWorkerConfig = errors.New("missing worker config")

type WorkerRunner interface {
	Run(context.Context, WorkerInput) (WorkerResult, error)
}
type LeaseHeartbeater interface {
	Heartbeat(context.Context, leases.LeaseRef) (leases.Lease, error)
}
type ShutdownCheckpointer interface {
	CreateGitCheckpoint(context.Context, leases.LeaseRef, WorkerSummary) (leases.Checkpoint, error)
}

func RunWorker(parent context.Context, input io.Reader, output io.Writer, runner WorkerRunner, heartbeater LeaseHeartbeater, checkpoint ShutdownCheckpointer, heartbeatInterval time.Duration) error {
	decoder := json.NewDecoder(io.LimitReader(input, 64*1024))
	decoder.DisallowUnknownFields()
	var workerInput WorkerInput
	if err := decoder.Decode(&workerInput); err != nil {
		return errors.New("invalid worker input")
	}
	if workerInput.TaskID < 1 || workerInput.StepID < 1 || workerInput.AgentName == "" || workerInput.LeaseID == "" || workerInput.LeaseVersion < 1 {
		return errors.New("incomplete worker input")
	}
	if heartbeatInterval <= 0 {
		heartbeatInterval = leases.HeartbeatInterval
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	type runOutcome struct {
		result WorkerResult
		err    error
	}
	done := make(chan runOutcome, 1)
	go func() { result, err := runner.Run(ctx, workerInput); done <- runOutcome{result, err} }()
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	ref := leases.LeaseRef{TaskID: workerInput.TaskID, StepID: workerInput.StepID, AgentName: workerInput.AgentName, LeaseID: workerInput.LeaseID, LeaseVersion: workerInput.LeaseVersion}
	for {
		select {
		case outcome := <-done:
			_ = json.NewEncoder(output).Encode(outcome.result)
			return outcome.err
		case <-parent.Done():
			cancel()
			outcome := <-done
			if checkpoint != nil {
				checkpointCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
				_, _ = checkpoint.CreateGitCheckpoint(checkpointCtx, ref, WorkerSummary{Completed: []string{"收到关闭信号，保存当前上下文"}, NextAction: "等待租约恢复后继续"})
				stop()
			}
			outcome.result.Status = RunInterrupted
			_ = json.NewEncoder(output).Encode(outcome.result)
			return parent.Err()
		case <-ticker.C:
			if heartbeater != nil {
				if _, err := heartbeater.Heartbeat(ctx, ref); err != nil {
					cancel()
					outcome := <-done
					if checkpoint != nil {
						checkpointCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
						_, _ = checkpoint.CreateGitCheckpoint(checkpointCtx, ref, WorkerSummary{Completed: []string{"租约心跳中断，保存当前上下文"}, NextAction: "等待租约恢复后继续"})
						stop()
					}
					outcome.result.Status = RunInterrupted
					_ = json.NewEncoder(output).Encode(outcome.result)
					return err
				}
			}
		}
	}
}

func NewWorkerCommand(binary string, input *os.File, agentEnv map[string]string) *exec.Cmd {
	cmd := exec.Command(binary, "agent-worker", "--input-fd", "3")
	cmd.ExtraFiles = []*os.File{input}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "TMPDIR=" + os.Getenv("TMPDIR")}
	keys := make([]string, 0, len(agentEnv))
	for key := range agentEnv {
		if strings.HasPrefix(key, "AGENT_") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = append(env, key+"="+agentEnv[key])
	}
	cmd.Env = env
	return cmd
}

type EnvChatter struct {
	registry *providers.Registry
	values   map[string]string
}

func NewEnvChatter(registry *providers.Registry, values map[string]string) *EnvChatter {
	return &EnvChatter{registry: registry, values: values}
}
func (e *EnvChatter) ChatAgent(ctx context.Context, _ string, messages []providers.Message, maxTokens int) (providers.Result, error) {
	providerType := strings.TrimSpace(e.values["AGENT_PROVIDER_TYPE"])
	key := strings.TrimSpace(e.values["AGENT_API_KEY"])
	model := strings.TrimSpace(e.values["AGENT_MODEL"])
	if providerType == "" || key == "" || model == "" {
		return providers.Result{}, fmt.Errorf("%w: agent provider_type, api_key, and model are required", ErrMissingWorkerConfig)
	}
	provider, err := e.registry.Get(providerType)
	if err != nil {
		return providers.Result{}, err
	}
	base := strings.TrimRight(strings.TrimSpace(e.values["AGENT_BASE_URL"]), "/")
	return provider.Chat(ctx, providers.Config{APIKey: key, BaseURL: base, Model: model}, messages, maxTokens)
}
func ProcessAgentEnv() map[string]string {
	result := map[string]string{}
	for _, key := range []string{"AGENT_PROVIDER_TYPE", "AGENT_API_KEY", "AGENT_BASE_URL", "AGENT_MODEL"} {
		result[key] = os.Getenv(key)
	}
	return result
}
