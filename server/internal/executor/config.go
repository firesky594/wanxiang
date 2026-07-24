package executor

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const maxAgentEnvBytes = 64 * 1024

// CopyTestEnv 复制测试环境配置供回归测试使用。
func CopyTestEnv(source, target string) error {
	if source == "" || target == "" {
		return errors.New("source and target env paths are required")
	}
	sourceInfo, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("inspect source env %q: %w", source, err)
	}
	if !sourceInfo.Mode().IsRegular() || sourceInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("source env %q must be a regular file", source)
	}
	if sourceInfo.Size() > maxAgentEnvBytes {
		return fmt.Errorf("source env %q exceeds size limit", source)
	}
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("target env %q already exists", target)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect target env %q: %w", target, err)
	}
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create target agent directory %q: %w", parent, err)
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("target agent directory %q is unsafe", parent)
	}

	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open source env %q: %w", source, err)
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create target env %q: %w", target, err)
	}
	keep := false
	defer func() {
		_ = out.Close()
		if !keep {
			_ = os.Remove(target)
		}
	}()
	written, err := io.Copy(out, io.LimitReader(in, maxAgentEnvBytes+1))
	if err != nil {
		return fmt.Errorf("copy test env to %q: %w", target, err)
	}
	if written > maxAgentEnvBytes {
		return fmt.Errorf("source env %q exceeds size limit", source)
	}
	if err := out.Chmod(0o600); err != nil {
		return fmt.Errorf("secure target env %q: %w", target, err)
	}
	if err := out.Sync(); err != nil {
		return fmt.Errorf("sync target env %q: %w", target, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close target env %q: %w", target, err)
	}
	keep = true
	return nil
}
