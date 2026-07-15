package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"

	"wanxiang-agent/server/internal/leases"
)

const maxCheckOutputBytes = maxRedactedBytes

type CheckRequest struct {
	Command string
	Args    []string
	Timeout time.Duration
}

type CheckResult struct {
	Command  string `json:"command"`
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out"`
	Error    string `json:"error,omitempty"`
}

type CheckRunner struct {
	db     *sql.DB
	leases LeaseAuthorizer
}

func NewCheckRunner(db *sql.DB, leaseService LeaseAuthorizer) *CheckRunner {
	return &CheckRunner{db: db, leases: leaseService}
}

func (r *CheckRunner) RunCheck(ctx context.Context, ref leases.LeaseRef, request CheckRequest) CheckResult {
	result := CheckResult{Command: strings.TrimSpace(request.Command + " " + strings.Join(request.Args, " ")), ExitCode: -1}
	if err := validateCheck(request); err != nil {
		result.Error = err.Error()
		return result
	}
	root, scope, err := r.runtimeContext(ctx, ref)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if r.leases == nil {
		result.Error = leases.ErrConflict.Error()
		return result
	}
	if err := r.leases.Authorize(ctx, ref, scope); err != nil {
		result.Error = err.Error()
		return result
	}
	timeout := request.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	if timeout > 2*time.Minute {
		result.Error = "check timeout exceeds limit"
		return result
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, request.Command, request.Args...)
	cmd.Dir = root
	output, runErr := cmd.CombinedOutput()
	result.Output = Redact(string(output))
	if runCtx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		result.Error = "check timed out"
		return result
	}
	if runErr == nil {
		result.ExitCode = 0
		return result
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		result.Error = "check failed"
	} else {
		result.Error = Redact(runErr.Error())
	}
	return result
}

func (r *CheckRunner) runtimeContext(ctx context.Context, ref leases.LeaseRef) (string, string, error) {
	if r.db == nil {
		return "", "", leases.ErrConflict
	}
	var root, scopeJSON string
	err := r.db.QueryRowContext(ctx, `select worktree_path,write_scope_json from project_workspaces where task_id=? and step_id=? and agent_name=? and status='ready'`, ref.TaskID, ref.StepID, ref.AgentName).Scan(&root, &scopeJSON)
	if err != nil {
		return "", "", leases.ErrConflict
	}
	var scopes []string
	if json.Unmarshal([]byte(scopeJSON), &scopes) != nil || len(scopes) == 0 {
		return "", "", leases.ErrConflict
	}
	return root, scopes[0], nil
}

func validateCheck(request CheckRequest) error {
	if request.Command == "" || strings.ContainsAny(request.Command, " \t\r\n;&|<>") {
		return errors.New("check command is not allowed")
	}
	for _, arg := range request.Args {
		if arg == "" || strings.ContainsAny(arg, "\r\n;&|<>") {
			return errors.New("check argument is unsafe")
		}
	}
	switch request.Command {
	case "go":
		if len(request.Args) == 0 || (request.Args[0] != "test" && request.Args[0] != "vet") {
			return errors.New("go check is not allowed")
		}
	case "npm", "pnpm":
		if len(request.Args) == 0 {
			return errors.New("package check is not allowed")
		}
		if request.Args[0] == "test" {
			return nil
		}
		if len(request.Args) < 2 || request.Args[0] != "run" || (request.Args[1] != "test" && request.Args[1] != "lint" && request.Args[1] != "build") {
			return errors.New("package script is not allowed")
		}
	default:
		return errors.New("check command is not allowed")
	}
	return nil
}
