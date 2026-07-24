package gitx

import (
	"context"
	"os/exec"
)

// Run 在指定仓库安全执行 Git 参数命令。
func Run(ctx context.Context, repoDir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
