package gitx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const projectLockRetryInterval = 25 * time.Millisecond

// AcquireProjectLock 获取跨进程项目 Git 排他锁，并返回可重复调用的释放函数。
func AcquireProjectLock(ctx context.Context, dataDir string, projectID int64) (func(), error) {
	if projectID <= 0 {
		return nil, errors.New("project id must be positive")
	}
	dataDir, err := filepath.Abs(dataDir)
	if err != nil || dataDir == string(filepath.Separator) {
		return nil, errors.New("project lock data directory is unsafe")
	}
	lockDir := filepath.Join(dataDir, "repo-locks")
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return nil, fmt.Errorf("create project lock directory: %w", err)
	}
	info, err := os.Lstat(lockDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("project lock directory is unsafe")
	}
	dirStat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || dirStat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("project lock directory ownership is unsafe")
	}
	lockPath := filepath.Join(lockDir, fmt.Sprintf("project-%d.lock", projectID))
	fd, err := unix.Open(lockPath, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open project lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), lockPath)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open project lock file")
	}
	closeFile := func() {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = file.Close()
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil ||
		stat.Mode&unix.S_IFMT != unix.S_IFREG ||
		stat.Uid != uint32(os.Geteuid()) ||
		stat.Nlink != 1 {
		closeFile()
		return nil, errors.New("project lock file is unsafe")
	}
	if err := unix.Fchmod(fd, 0o600); err != nil {
		closeFile()
		return nil, fmt.Errorf("secure project lock file: %w", err)
	}
	ticker := time.NewTicker(projectLockRetryInterval)
	defer ticker.Stop()
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			var once sync.Once
			return func() {
				once.Do(closeFile)
			}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			closeFile()
			return nil, fmt.Errorf("lock project repository: %w", err)
		}
		select {
		case <-ctx.Done():
			closeFile()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
