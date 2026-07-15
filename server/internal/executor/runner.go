package executor

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"wanxiang-agent/server/internal/gitx"
	"wanxiang-agent/server/internal/leases"
	"wanxiang-agent/server/internal/providers"
)

const maxProviderRequests = 3

type AgentChatter interface {
	ChatAgent(context.Context, string, []providers.Message, int) (providers.Result, error)
}
type Runner struct {
	db          *sql.DB
	chat        AgentChatter
	files       *FileTools
	checks      *CheckRunner
	checkpoints *CheckpointRunner
}

func NewRunner(db *sql.DB, chat AgentChatter, files *FileTools, checks *CheckRunner, checkpoints *CheckpointRunner) *Runner {
	return &Runner{db: db, chat: chat, files: files, checks: checks, checkpoints: checkpoints}
}

func (r *Runner) Run(ctx context.Context, input WorkerInput) (WorkerResult, error) {
	result := WorkerResult{Status: RunRunning}
	ref := leases.LeaseRef{TaskID: input.TaskID, StepID: input.StepID, AgentName: input.AgentName, LeaseID: input.LeaseID, LeaseVersion: input.LeaseVersion}
	runID, err := r.startRun(ctx, ref)
	if err != nil {
		result.Status = RunFailed
		return result, err
	}
	messages := []providers.Message{{Role: "system", Content: "你是受控执行 Agent。只返回协议版本 1 的 JSON，不得输出密钥，不得请求 shell、部署或越界路径。"}, {Role: "user", Content: r.workPrompt(ctx, input)}}
	sequence := 0
	for request := 1; request <= maxProviderRequests; request++ {
		chatResult, chatErr := r.chat.ChatAgent(ctx, input.AgentName, messages, 2048)
		result.RequestCount = request
		if chatErr != nil {
			if retryableProviderError(chatErr) && request < maxProviderRequests {
				continue
			}
			result.Status = providerFailureStatus(chatErr)
			return r.finish(ctx, runID, result, chatErr)
		}
		result.InputTokens += chatResult.InputTokens
		result.OutputTokens += chatResult.OutputTokens
		response, parseErr := ParseProviderResponse(chatResult.Content)
		if parseErr != nil {
			result.Status = RunFailed
			return r.finish(ctx, runID, result, parseErr)
		}
		messages = append(messages, providers.Message{Role: "assistant", Content: chatResult.Content})
		for _, action := range response.Actions {
			sequence++
			actionResult, actionErr := r.executeAction(ctx, ref, response, action)
			r.auditAction(ctx, runID, sequence, action, actionResult, actionErr)
			if actionErr != nil {
				result.Status = RunFailed
				return r.finish(ctx, runID, result, actionErr)
			}
			messages = append(messages, providers.Message{Role: "user", Content: "动作结果：" + actionResult})
		}
		result.Summary = response.Summary
		result.NextAction = response.NextAction
		switch response.Status {
		case ProviderCompleted:
			result.Status = RunCompleted
			return r.finish(ctx, runID, result, nil)
		case ProviderBlocked:
			result.Status = RunStopped
			return r.finish(ctx, runID, result, errors.New("provider reported blocked"))
		case ProviderCheckpoint:
			result.Status = RunCheckpointed
			return r.finish(ctx, runID, result, nil)
		}
	}
	result.Status = RunFailed
	return r.finish(ctx, runID, result, errors.New("provider request budget exhausted"))
}

func (r *Runner) executeAction(ctx context.Context, ref leases.LeaseRef, response ProviderResponse, action ActionRequest) (string, error) {
	switch action.Type {
	case ActionReadFile:
		content, err := r.files.ReadFile(ctx, ref, action.Path)
		return string(content), err
	case ActionWriteFile:
		err := r.files.WriteFile(ctx, ref, action.Path, []byte(action.Content))
		return "write completed", err
	case ActionRunCheck:
		got := r.checks.RunCheck(ctx, ref, CheckRequest{Command: action.Command, Args: action.Args})
		if got.Error != "" {
			return got.Output, errors.New(got.Error)
		}
		return got.Output, nil
	case ActionGitStatus:
		root, err := r.files.workspaceRoot(ctx, ref)
		if err != nil {
			return "", err
		}
		out, err := gitx.Run(ctx, root, "status", "--short", "--branch")
		return Redact(out), err
	case ActionCheckpoint:
		if r.checkpoints == nil {
			return "", errors.New("checkpoint tool unavailable")
		}
		cp, err := r.checkpoints.CreateGitCheckpoint(ctx, ref, WorkerSummary{Completed: []string{response.Summary}, NextAction: response.NextAction})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("checkpoint %d", cp.ID), nil
	default:
		return "", errors.New("unknown action")
	}
}

func (r *Runner) workPrompt(ctx context.Context, input WorkerInput) string {
	var title, description string
	_ = r.db.QueryRowContext(ctx, `select title,description from tasks where id=?`, input.TaskID).Scan(&title, &description)
	return fmt.Sprintf("任务 %d，步骤 %d。标题：%s\n描述：%s", input.TaskID, input.StepID, title, description)
}
func (r *Runner) startRun(ctx context.Context, ref leases.LeaseRef) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := r.db.ExecContext(ctx, `insert into executor_runs(task_id,step_id,agent_name,lease_id,lease_version,status,created_at,started_at,updated_at) values(?,?,?,?,?,'running',?,?,?) on conflict(lease_id) do update set status='running',updated_at=excluded.updated_at`, ref.TaskID, ref.StepID, ref.AgentName, ref.LeaseID, ref.LeaseVersion, now, now, now)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	if id == 0 {
		_ = r.db.QueryRowContext(ctx, `select id from executor_runs where lease_id=?`, ref.LeaseID).Scan(&id)
	}
	r.runtimeEvent(ctx, id, "task.executor.started", map[string]any{"step_id": ref.StepID, "lease_version": ref.LeaseVersion})
	return id, nil
}
func (r *Runner) auditAction(ctx context.Context, runID int64, seq int, action ActionRequest, value string, actionErr error) {
	status := "passed"
	summary := "ok"
	if actionErr != nil {
		status = "failed"
		summary = Redact(actionErr.Error())
	}
	sum := sha256.Sum256([]byte(value))
	hash := hex.EncodeToString(sum[:])
	_, _ = r.db.ExecContext(ctx, `insert into executor_actions(run_id,sequence,action_type,relative_path,status,result_summary,result_hash,created_at) values(?,?,?,?,?,?,?,?)`, runID, seq, string(action.Type), action.Path, status, summary, hash, time.Now().UTC().Format(time.RFC3339Nano))
	r.runtimeEvent(ctx, runID, "task.executor.action", map[string]any{"sequence": seq, "action_type": action.Type, "path": action.Path, "status": status, "result_hash": hash})
}
func (r *Runner) finish(ctx context.Context, runID int64, result WorkerResult, runErr error) (WorkerResult, error) {
	result.FinishedAt = time.Now().UTC()
	message := ""
	if runErr != nil {
		message = Redact(runErr.Error())
	}
	_, _ = r.db.ExecContext(ctx, `update executor_runs set status=?,request_count=?,input_tokens=?,output_tokens=?,error_summary=?,exited_at=?,updated_at=? where id=?`, string(result.Status), result.RequestCount, result.InputTokens, result.OutputTokens, message, result.FinishedAt.Format(time.RFC3339Nano), result.FinishedAt.Format(time.RFC3339Nano), runID)
	eventType := "task.executor.exited"
	if runErr != nil {
		eventType = "task.executor.failed"
	}
	r.runtimeEvent(ctx, runID, eventType, map[string]any{"status": result.Status, "request_count": result.RequestCount, "input_tokens": result.InputTokens, "output_tokens": result.OutputTokens, "error": message})
	return result, runErr
}
func (r *Runner) runtimeEvent(ctx context.Context, runID int64, eventType string, payload any) {
	encoded, _ := json.Marshal(payload)
	_, _ = r.db.ExecContext(ctx, `insert into runtime_events(task_id,event_type,actor,payload_json,created_at) select task_id,?,agent_name,?,? from executor_runs where id=?`, eventType, string(encoded), time.Now().UTC().Format(time.RFC3339Nano), runID)
}
func retryableProviderError(err error) bool {
	v := strings.ToLower(err.Error())
	return strings.Contains(v, "429") || strings.Contains(v, "http 5") || strings.Contains(v, "timeout") || strings.Contains(v, "deadline")
}
func providerFailureStatus(err error) RunStatus {
	v := strings.ToLower(err.Error())
	if strings.Contains(v, "provider_type") || strings.Contains(v, "api_key") || strings.Contains(v, "model are required") || strings.Contains(v, "401") || strings.Contains(v, "403") {
		return RunStopped
	}
	return RunFailed
}
