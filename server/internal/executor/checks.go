package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

// NewCheckRunner 创建命令检查执行器。
func NewCheckRunner(db *sql.DB, leaseService LeaseAuthorizer) *CheckRunner {
	return &CheckRunner{db: db, leases: leaseService}
}

// RunCheck 校验租约后运行受限检查命令。
func (r *CheckRunner) RunCheck(ctx context.Context, ref leases.LeaseRef, request CheckRequest) CheckResult {
	result := CheckResult{Command: formatCheckCommand(request), ExitCode: -1}
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
	if err := validatePackageScript(root, request); err != nil {
		result.Error = err.Error()
		return result
	}
	before, err := controlledWorkspaceState(ctx, root)
	if err != nil {
		result.Error = "check workspace state is unavailable"
		return result
	}
	checkRoot, cleanup, err := copyCheckWorkspace(ctx, root)
	if err != nil {
		result.Error = "check sandbox is unavailable"
		return result
	}
	defer cleanup()
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
	binary, err := exec.LookPath(request.Command)
	if err != nil {
		result.Error = "check command is unavailable"
		return result
	}
	cmd, err := sandboxedCheckCommand(runCtx, checkRoot, binary, request.Args)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	output := &checkOutputBuffer{limit: maxCheckOutputBytes}
	cmd.Stdout = output
	cmd.Stderr = output
	runErr := cmd.Run()
	result.Output = Redact(output.String())
	after, stateErr := controlledWorkspaceState(context.Background(), root)
	if stateErr != nil || before != after {
		result.Error = "check modified the controlled workspace"
		return result
	}
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

func formatCheckCommand(request CheckRequest) string {
	return Redact(strings.TrimSpace(request.Command + " " + strings.Join(request.Args, " ")))
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
		if arg == "" || strings.ContainsAny(arg, "\r\n;&|<>`$\\") {
			return errors.New("check argument is unsafe")
		}
	}
	switch request.Command {
	case "go":
		if err := validateGoCheckArgs(request.Args); err != nil {
			return err
		}
	case "npm", "pnpm":
		if err := validatePackageCheckArgs(request.Args); err != nil {
			return err
		}
	default:
		return errors.New("check command is not allowed")
	}
	return nil
}

func validateGoCheckArgs(args []string) error {
	if len(args) == 0 || (args[0] != "test" && args[0] != "vet") {
		return errors.New("go check is not allowed")
	}
	for _, arg := range args[1:] {
		if isSafeGoPackage(arg) {
			continue
		}
		switch args[0] {
		case "test":
			if isSafeGoTestFlag(arg) {
				continue
			}
		case "vet":
			if arg == "-json" || arg == "-v" {
				continue
			}
		}
		return errors.New("go check argument is not allowed")
	}
	return nil
}

func isSafeGoPackage(value string) bool {
	if value == "." {
		return true
	}
	if !strings.HasPrefix(value, "./") || filepath.IsAbs(value) || strings.ContainsAny(value, ":@=") {
		return false
	}
	for _, part := range strings.Split(strings.TrimPrefix(value, "./"), "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func isSafeGoTestFlag(value string) bool {
	switch value {
	case "-v", "-race", "-short", "-failfast", "-json", "-cover", "-count=1", "-shuffle=off":
		return true
	}
	if strings.HasPrefix(value, "-timeout=") {
		duration, err := time.ParseDuration(strings.TrimPrefix(value, "-timeout="))
		return err == nil && duration > 0 && duration <= 2*time.Minute
	}
	if strings.HasPrefix(value, "-parallel=") {
		count, err := strconv.Atoi(strings.TrimPrefix(value, "-parallel="))
		return err == nil && count > 0 && count <= 32
	}
	return false
}

func validatePackageCheckArgs(args []string) error {
	if len(args) == 1 && args[0] == "test" {
		return nil
	}
	if len(args) == 2 && args[0] == "run" {
		switch args[1] {
		case "test", "lint", "build":
			return nil
		}
	}
	return errors.New("package check is not allowed")
}

func validatePackageScript(root string, request CheckRequest) error {
	if request.Command != "npm" && request.Command != "pnpm" {
		return nil
	}
	scriptName := request.Args[0]
	if scriptName == "run" {
		scriptName = request.Args[1]
	}
	content, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return errors.New("package manifest is unavailable")
	}
	var manifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(content, &manifest) != nil {
		return errors.New("package manifest is invalid")
	}
	script := strings.TrimSpace(manifest.Scripts[scriptName])
	if script == "" || !safePackageScript(script) {
		return errors.New("package script is not allowed")
	}
	return nil
}

func safePackageScript(script string) bool {
	if strings.ContainsAny(script, "\r\n;|<>`$\\\"'") {
		return false
	}
	allowed := map[string]bool{
		"ava": true, "eslint": true, "jest": true, "mocha": true, "ng": true,
		"next": true, "nuxt": true, "react-scripts": true, "rollup": true,
		"tsc": true, "vite": true, "vitest": true, "vue-tsc": true, "webpack": true,
	}
	parts := strings.Fields(script)
	expectCommand := true
	for _, part := range parts {
		if part == "&&" {
			if expectCommand {
				return false
			}
			expectCommand = true
			continue
		}
		if expectCommand {
			if !allowed[part] {
				return false
			}
			expectCommand = false
			continue
		}
		lower := strings.ToLower(part)
		if filepath.IsAbs(part) || part == ".." || strings.HasPrefix(part, "../") ||
			strings.Contains(part, "/../") || strings.HasSuffix(part, "/..") ||
			strings.HasPrefix(lower, "--prefix") || strings.HasPrefix(lower, "--cache") ||
			strings.HasPrefix(lower, "--cwd") || strings.HasPrefix(lower, "--dir") ||
			strings.HasPrefix(lower, "--global") || strings.HasPrefix(lower, "--config") ||
			strings.HasPrefix(lower, "--exec") || strings.HasPrefix(lower, "--output") ||
			strings.HasPrefix(lower, "--outdir") || strings.HasPrefix(lower, "--out-dir") {
			return false
		}
	}
	return !expectCommand
}

func copyCheckWorkspace(ctx context.Context, root string) (string, func(), error) {
	parent, err := os.MkdirTemp("", "wanxiang-check-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(parent) }
	target := filepath.Join(parent, "work")
	if err := os.Mkdir(target, 0o700); err != nil {
		cleanup()
		return "", nil, err
	}
	cmd := exec.CommandContext(ctx, "cp", "-a", "--reflink=auto", filepath.Join(root, "."), target)
	if output, err := cmd.CombinedOutput(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("copy check workspace: %w: %s", err, Redact(string(output)))
	}
	for _, dir := range []string{".check-home", ".check-tmp", ".check-cache"} {
		if err := os.MkdirAll(filepath.Join(target, dir), 0o700); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	return target, cleanup, nil
}

func sandboxedCheckCommand(ctx context.Context, root, binary string, commandArgs []string) (*exec.Cmd, error) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, errors.New("check sandbox is unavailable")
	}
	args := []string{
		"--die-with-parent",
		"--unshare-all",
		"--new-session",
		"--clearenv",
		"--ro-bind", "/usr", "/usr",
		"--ro-bind", "/bin", "/bin",
		"--ro-bind", "/lib", "/lib",
		"--ro-bind", "/lib64", "/lib64",
		"--ro-bind", "/etc", "/etc",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--dir", "/tmp/home",
		"--dir", "/tmp/cache",
		"--bind", root, "/workspace",
		"--chdir", "/workspace",
		"--setenv", "PATH", "/workspace/node_modules/.bin:/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin",
		"--setenv", "HOME", "/tmp/home",
		"--setenv", "TMPDIR", "/tmp",
		"--setenv", "GOCACHE", "/tmp/cache/go-build",
		"--setenv", "GOTMPDIR", "/tmp",
		"--setenv", "GOPATH", "/tmp/gopath",
		"--setenv", "GOPROXY", "off",
		"--setenv", "GOTOOLCHAIN", "local",
		"--setenv", "GOFLAGS", "-mod=readonly",
		"--setenv", "NPM_CONFIG_CACHE", "/tmp/cache/npm",
		"--setenv", "PNPM_HOME", "/tmp/cache/pnpm",
		"--setenv", "CI", "1",
	}
	if moduleCache := hostGoModuleCache(ctx); moduleCache != "" {
		args = append(args,
			"--ro-bind", moduleCache, "/gomodcache",
			"--setenv", "GOMODCACHE", "/gomodcache",
		)
	}
	args = append(args, "--", binary)
	args = append(args, commandArgs...)
	return exec.CommandContext(ctx, bwrap, args...), nil
}

func hostGoModuleCache(ctx context.Context) string {
	commandCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, "go", "env", "GOMODCACHE")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	path := strings.TrimSpace(string(output))
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return ""
	}
	return path
}

func controlledWorkspaceState(ctx context.Context, root string) (string, error) {
	branch, err := gitCommand(ctx, root, "branch", "--show-current")
	if err != nil {
		return "", err
	}
	head, err := gitCommand(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	status, err := gitCommand(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	return branch + "\x00" + head + "\x00" + status, nil
}

func gitCommand(ctx context.Context, root string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	output, err := cmd.Output()
	return string(output), err
}

type checkOutputBuffer struct {
	mu    sync.Mutex
	data  strings.Builder
	limit int
}

func (b *checkOutputBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	original := len(value)
	remaining := b.limit - b.data.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = b.data.Write(value)
	}
	return original, nil
}

func (b *checkOutputBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}
