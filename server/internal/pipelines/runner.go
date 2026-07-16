package pipelines

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
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
	if !allowed[s.Command] {
		return Result{FailureClass: "permission_blocked", Err: ErrInvalidDefinition}
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(s.TimeoutSeconds)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, s.Command, s.Args...)
	cmd.Dir = dir
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
func redact(v string) string {
	lower := strings.ToLower(v)
	for _, m := range []string{"authorization=", "bearer ", "api_key=", "token=", "password=", "secret=", "sk-"} {
		if i := strings.Index(lower, m); i >= 0 {
			return v[:i] + "[REDACTED]"
		}
	}
	return v
}
