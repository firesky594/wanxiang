package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	cmd := exec.CommandContext(ctx, "cp", "-a", "--reflink=auto", filepath.Clean(root)+string(filepath.Separator)+".", target)
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
	const bwrap = "/usr/bin/bwrap"
	info, err := os.Lstat(bwrap)
	if err != nil {
		return nil, errors.New("check sandbox is unavailable")
	}
	stat, ownedByRoot := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 ||
		info.Mode().Perm()&0o022 != 0 || !ownedByRoot || stat.Uid != 0 {
		return nil, errors.New("check sandbox is unavailable")
	}
	toolchain, err := resolveCheckToolchain(binary)
	if err != nil {
		return nil, errors.New("check toolchain is unavailable")
	}
	args := []string{
		"--die-with-parent",
		"--unshare-all",
		"--new-session",
		"--clearenv",
		"--bind", root, "/workspace",
		"--dir", "/usr",
		"--ro-bind", "/usr/bin", "/usr/bin",
		"--ro-bind", "/usr/lib", "/usr/lib",
		"--ro-bind", "/usr/lib64", "/usr/lib64",
		"--symlink", "usr/bin", "/bin",
		"--symlink", "usr/lib", "/lib",
		"--symlink", "usr/lib64", "/lib64",
		"--dir", "/usr/include",
		"--ro-bind", "/usr/include", "/usr/include",
		"--dir", "/usr/share",
		"--ro-bind", "/usr/share/zoneinfo", "/usr/share/zoneinfo",
		"--dir", "/etc",
		"--ro-bind", "/etc/passwd", "/etc/passwd",
		"--ro-bind", "/etc/group", "/etc/group",
		"--ro-bind", "/etc/nsswitch.conf", "/etc/nsswitch.conf",
		"--ro-bind", "/etc/hosts", "/etc/hosts",
		"--ro-bind", "/etc/localtime", "/etc/localtime",
		"--dir", "/etc/ssl",
		"--ro-bind", "/etc/ssl/certs", "/etc/ssl/certs",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--dir", "/tmp/home",
		"--dir", "/tmp/cache",
		"--dir", "/toolchains",
		"--ro-bind", toolchain.source, toolchain.target,
		"--chdir", "/workspace",
		"--setenv", "PATH", toolchain.target + "/bin:/usr/bin:/bin:/workspace/node_modules/.bin",
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
	if toolchain.goRoot {
		args = append(args, "--setenv", "GOROOT", toolchain.target)
		prepareCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		moduleMounts, mountErr := prepareGoModuleMounts(prepareCtx, root, filepath.Join(toolchain.source, "bin", "go"))
		cancel()
		if mountErr != nil {
			return nil, errors.New("check module sandbox is unavailable")
		}
		args = append(args, "--dir", "/gomodcache")
		args = append(args, goModuleMountArgs(moduleMounts)...)
		args = append(args,
			"--setenv", "GOMODCACHE", "/gomodcache",
			"--setenv", "GOSUMDB", "off",
		)
	}
	args = append(args, "--", toolchain.binary)
	args = append(args, toolchain.prefixArgs...)
	args = append(args, commandArgs...)
	return exec.CommandContext(ctx, bwrap, args...), nil
}

type checkToolchain struct {
	source     string
	target     string
	binary     string
	goRoot     bool
	prefixArgs []string
}

func resolveCheckToolchain(binary string) (checkToolchain, error) {
	absolute, err := filepath.Abs(binary)
	if err != nil {
		return checkToolchain{}, err
	}
	name := filepath.Base(absolute)
	binDir := filepath.Dir(absolute)
	if filepath.Base(binDir) != "bin" {
		return checkToolchain{}, errors.New("unexpected toolchain layout")
	}
	switch name {
	case "go":
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil || filepath.Base(filepath.Dir(resolved)) != "bin" {
			return checkToolchain{}, errors.New("invalid go toolchain")
		}
		source := filepath.Dir(filepath.Dir(resolved))
		if err := validateTrustedToolchain("go", source); err != nil {
			return checkToolchain{}, err
		}
		return checkToolchain{
			source: source,
			target: "/toolchains/go",
			binary: "/toolchains/go/bin/go",
			goRoot: true,
		}, nil
	case "npm", "pnpm":
		source, err := filepath.EvalSymlinks(filepath.Dir(binDir))
		if err != nil {
			return checkToolchain{}, err
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil || !pathWithin(source, resolved) {
			return checkToolchain{}, errors.New("invalid node toolchain")
		}
		node, err := filepath.EvalSymlinks(filepath.Join(source, "bin", "node"))
		if err != nil || !pathWithin(source, node) {
			return checkToolchain{}, errors.New("node runtime is unavailable")
		}
		if err := validateTrustedToolchain(name, source); err != nil {
			return checkToolchain{}, err
		}
		cli := "/toolchains/node/lib/node_modules/npm/bin/npm-cli.js"
		if name == "pnpm" {
			cli = "/toolchains/node/lib/node_modules/pnpm/bin/pnpm.cjs"
		}
		return checkToolchain{
			source:     source,
			target:     "/toolchains/node",
			binary:     "/toolchains/node/bin/node",
			prefixArgs: []string{cli},
		}, nil
	default:
		return checkToolchain{}, errors.New("unsupported check toolchain")
	}
}

func validateTrustedToolchain(command, root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) == string(filepath.Separator) {
		return errors.New("toolchain root is unsafe")
	}
	allowed := false
	switch command {
	case "go":
		for _, prefix := range []string{"/usr/local/btgojdk", "/usr/local/go", "/usr/lib/go", "/usr/lib/golang"} {
			if pathWithin(prefix, root) {
				allowed = true
				break
			}
		}
	case "npm", "pnpm":
		allowed = pathWithin("/www/server/nodejs", root) || pathWithin("/usr", root)
	}
	if !allowed {
		return errors.New("toolchain root is not allowed")
	}
	for current := filepath.Clean(root); current != string(filepath.Separator); current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
			return errors.New("toolchain path is unsafe")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			return errors.New("toolchain ownership is unsafe")
		}
	}
	return nil
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

type goModuleMount struct {
	source string
	target string
}

type listedGoModule struct {
	Path    string
	Version string
	Dir     string
	GoMod   string
	Main    bool
	Replace *listedGoModule
}

func prepareGoModuleMounts(ctx context.Context, workspace, goBinary string) ([]goModuleMount, error) {
	moduleCache, err := trustedGoModuleCache(ctx, goBinary)
	if err != nil {
		return nil, err
	}
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, goBinary, "list", "-m", "-json", "all")
	cmd.Dir = workspace
	cmd.Env = []string{
		"PATH=" + filepath.Dir(goBinary) + ":/usr/bin:/bin",
		"HOME=/tmp",
		"GOROOT=" + filepath.Dir(filepath.Dir(goBinary)),
		"GOMODCACHE=" + moduleCache,
		"GOPATH=" + filepath.Dir(filepath.Dir(moduleCache)),
		"GOPROXY=off",
		"GOSUMDB=off",
		"GOTOOLCHAIN=local",
		"GOFLAGS=-mod=readonly",
		"GOWORK=off",
		"GOENV=off",
	}
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	seen := map[string]bool{}
	var mounts []goModuleMount
	for {
		var module listedGoModule
		if err := decoder.Decode(&module); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		if module.Main {
			if module.Dir != "" {
				mainDir, err := filepath.EvalSymlinks(module.Dir)
				if err != nil || !pathWithin(workspace, mainDir) {
					return nil, errors.New("main module is outside workspace")
				}
			}
			continue
		}
		effective := module
		if module.Replace != nil {
			effective = *module.Replace
			if effective.Version == "" {
				localDir, err := filepath.EvalSymlinks(effective.Dir)
				if err != nil || !pathWithin(workspace, localDir) {
					return nil, errors.New("local module replacement is outside workspace")
				}
				continue
			}
		}
		if effective.Version == "" {
			return nil, errors.New("module dependency is not materialized")
		}
		if effective.Dir == "" && effective.GoMod == "" {
			continue
		}
		dependencyPaths := []string{effective.Dir}
		if effective.GoMod != "" {
			if !strings.HasSuffix(effective.GoMod, ".mod") {
				return nil, errors.New("module metadata path is unsafe")
			}
			metadataBase := strings.TrimSuffix(effective.GoMod, ".mod")
			for _, extension := range []string{".info", ".mod", ".zip", ".ziphash"} {
				candidate := metadataBase + extension
				if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
					dependencyPaths = append(dependencyPaths, candidate)
				}
			}
		}
		for _, dependencyPath := range dependencyPaths {
			if dependencyPath == "" {
				continue
			}
			source, err := filepath.EvalSymlinks(dependencyPath)
			if err != nil || !pathWithin(moduleCache, source) {
				return nil, errors.New("module dependency is outside trusted cache")
			}
			relative, err := filepath.Rel(moduleCache, source)
			if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return nil, errors.New("module dependency path is unsafe")
			}
			target := filepath.ToSlash(filepath.Join("/gomodcache", relative))
			if !seen[target] {
				seen[target] = true
				mounts = append(mounts, goModuleMount{source: source, target: target})
				if len(mounts) > 256 {
					return nil, errors.New("module dependency limit exceeded")
				}
			}
		}
	}
	sort.Slice(mounts, func(i, j int) bool { return mounts[i].target < mounts[j].target })
	return mounts, nil
}

func trustedGoModuleCache(ctx context.Context, goBinary string) (string, error) {
	cmd := exec.CommandContext(ctx, goBinary, "env", "GOMODCACHE")
	cmd.Env = []string{
		"PATH=" + filepath.Dir(goBinary) + ":/usr/bin:/bin",
		"HOME=" + os.Getenv("HOME"),
		"GOROOT=" + filepath.Dir(filepath.Dir(goBinary)),
		"GOTOOLCHAIN=local",
		"GOENV=off",
	}
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	path, err := filepath.EvalSymlinks(strings.TrimSpace(string(output)))
	if err != nil || !filepath.IsAbs(path) || path == string(filepath.Separator) ||
		!strings.HasSuffix(filepath.Clean(path), filepath.Join("pkg", "mod")) {
		return "", errors.New("go module cache is unsafe")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", errors.New("go module cache is unavailable")
	}
	return path, nil
}

func goModuleMountArgs(mounts []goModuleMount) []string {
	directories := map[string]bool{}
	for _, mount := range mounts {
		for parent := filepath.Dir(mount.target); parent != "/gomodcache" && parent != string(filepath.Separator); parent = filepath.Dir(parent) {
			directories[parent] = true
		}
	}
	ordered := make([]string, 0, len(directories))
	for directory := range directories {
		ordered = append(ordered, directory)
	}
	sort.Slice(ordered, func(i, j int) bool {
		leftDepth := strings.Count(ordered[i], "/")
		rightDepth := strings.Count(ordered[j], "/")
		if leftDepth == rightDepth {
			return ordered[i] < ordered[j]
		}
		return leftDepth < rightDepth
	})
	args := make([]string, 0, len(ordered)*2+len(mounts)*3)
	for _, directory := range ordered {
		args = append(args, "--dir", directory)
	}
	for _, mount := range mounts {
		args = append(args, "--ro-bind", mount.source, mount.target)
	}
	return args
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
