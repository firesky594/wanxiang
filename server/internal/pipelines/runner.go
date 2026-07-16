package pipelines

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Result struct {
	Output       string
	FailureClass string
	Err          error
}
type Runner interface {
	Run(context.Context, string, Step) Result
}
type CommandRunner struct{}

func (CommandRunner) Run(ctx context.Context, dir string, s Step) Result {
	if !allowedStep(StepDefinition{ID: s.Key, Kind: s.Kind, Command: s.Command, Args: s.Args, TimeoutSeconds: s.TimeoutSeconds, MaxAttempts: s.MaxAttempts, Reversible: s.Reversible}) {
		return Result{FailureClass: "permission_blocked", Err: ErrInvalidDefinition}
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(s.TimeoutSeconds)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, s.Command, s.Args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir(), "TMPDIR=" + os.TempDir(), "LANG=C.UTF-8"}
	if s.Command == "pm2" {
		pm2Home, err := trustedPM2Home()
		if err != nil {
			return Result{FailureClass: "environment_failure", Err: err}
		}
		cmd.Env = append(cmd.Env, "PM2_HOME="+pm2Home)
	}
	var b bytes.Buffer
	cmd.Stdout = &b
	cmd.Stderr = &b
	e := cmd.Run()
	out := redact(b.String())
	if len(out) > 4096 {
		out = out[:4096]
	}
	if e == nil {
		return Result{Output: out}
	}
	class := "code_failure"
	var ee *exec.Error
	if errors.As(e, &ee) || errors.Is(cctx.Err(), context.DeadlineExceeded) {
		class = "environment_failure"
	}
	return Result{Output: out, FailureClass: class, Err: e}
}

func trustedPM2Home() (string, error) {
	home := os.Getenv("WANXIANG_PM2_HOME")
	if !filepath.IsAbs(home) {
		return "", errors.New("WANXIANG_PM2_HOME must be an absolute path")
	}
	info, err := os.Stat(home)
	if err != nil || !info.IsDir() {
		return "", errors.New("WANXIANG_PM2_HOME unavailable")
	}
	return home, nil
}
func redact(v string) string {
	lower := strings.ToLower(v)
	for _, m := range []string{"authorization=", "bearer ", "api_key=", "token=", "password=", "secret=", "sk-"} {
		if i := strings.Index(lower, m); i >= 0 {
			return v[:i] + "[REDACTED]"
		}
	}
	return v
}
