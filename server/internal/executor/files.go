package executor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	securefiles "wanxiang-agent/server/internal/files"
	"wanxiang-agent/server/internal/leases"
)

const (
	maxReadBytes  = 256 * 1024
	maxWriteBytes = 512 * 1024
)

type LeaseAuthorizer interface {
	Authorize(context.Context, leases.LeaseRef, string) error
}

type FileTools struct {
	db     *sql.DB
	leases LeaseAuthorizer
}

// NewFileTools 创建受租约保护的文件工具。
func NewFileTools(db *sql.DB, leaseService LeaseAuthorizer) *FileTools {
	return &FileTools{db: db, leases: leaseService}
}

// ReadFile 鉴权并读取工作区相对文件。
func (t *FileTools) ReadFile(ctx context.Context, ref leases.LeaseRef, relativePath string) ([]byte, error) {
	target, err := t.authorizePath(ctx, ref, relativePath)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("target is not a regular file")
	}
	if info.Size() > maxReadBytes {
		return nil, errors.New("file exceeds read limit")
	}
	f, err := os.Open(target)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	content, err := io.ReadAll(io.LimitReader(f, maxReadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxReadBytes {
		return nil, errors.New("file exceeds read limit")
	}
	return content, nil
}

// WriteFile 鉴权并原子写入工作区相对文件。
func (t *FileTools) WriteFile(ctx context.Context, ref leases.LeaseRef, relativePath string, content []byte) error {
	if len(content) > maxWriteBytes {
		return errors.New("content exceeds write limit")
	}
	target, err := t.authorizePath(ctx, ref, relativePath)
	if err != nil {
		return err
	}
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	// Recheck after creating parents so a concurrent link replacement cannot bypass validation.
	root, err := t.workspaceRoot(ctx, ref)
	if err != nil {
		return err
	}
	target, err = securefiles.UnderRoot(root, target)
	if err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, statErr := os.Lstat(target); statErr == nil {
		if !info.Mode().IsRegular() {
			return errors.New("target is not a regular file")
		}
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	tmp, err := os.CreateTemp(parent, ".wanxiang-write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(mode); err == nil {
		_, err = tmp.Write(content)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(tmpName, target); err != nil {
		return err
	}
	return nil
}

func (t *FileTools) authorizePath(ctx context.Context, ref leases.LeaseRef, relativePath string) (string, error) {
	clean, err := validateWorkerPath(relativePath)
	if err != nil {
		return "", err
	}
	if t.leases == nil {
		return "", leases.ErrConflict
	}
	if err := t.leases.Authorize(ctx, ref, clean); err != nil {
		return "", err
	}
	root, err := t.workspaceRoot(ctx, ref)
	if err != nil {
		return "", err
	}
	return securefiles.UnderRoot(root, filepath.Join(root, clean))
}

func (t *FileTools) workspaceRoot(ctx context.Context, ref leases.LeaseRef) (string, error) {
	if t.db == nil {
		return "", leases.ErrConflict
	}
	var root string
	err := t.db.QueryRowContext(ctx, `select worktree_path from project_workspaces where task_id=? and step_id=? and agent_name=? and status='ready'`, ref.TaskID, ref.StepID, ref.AgentName).Scan(&root)
	if err != nil || root == "" {
		return "", leases.ErrConflict
	}
	return root, nil
}

func validateWorkerPath(path string) (string, error) {
	if path == "" || filepath.IsAbs(path) || strings.Contains(path, "\\") {
		return "", errors.New("invalid worker path")
	}
	clean := filepath.Clean(path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || strings.HasPrefix(clean, ":") {
		return "", errors.New("worker path escapes workspace")
	}
	parts := strings.Split(filepath.ToSlash(clean), "/")
	for _, part := range parts {
		lower := strings.ToLower(part)
		extension := strings.ToLower(filepath.Ext(lower))
		if strings.HasPrefix(part, ":") ||
			lower == ".git" || lower == ".env" || lower == "env" || strings.HasSuffix(lower, ".env") ||
			lower == "credentials" || strings.HasPrefix(lower, "credentials.") ||
			lower == "credential" || strings.HasPrefix(lower, "credential.") ||
			lower == "secrets" || lower == ".secrets" || strings.HasPrefix(lower, "secrets.") ||
			lower == "secret" || strings.HasPrefix(lower, "secret.") ||
			lower == ".ssh" || lower == ".aws" || lower == ".docker" || lower == ".gnupg" || lower == ".kube" ||
			lower == ".npmrc" || lower == ".pypirc" || lower == ".netrc" ||
			lower == ".git-credentials" ||
			lower == "id_rsa" || lower == "id_ed25519" || lower == "service-account.json" ||
			extension == ".pem" || extension == ".key" || extension == ".p12" ||
			extension == ".pfx" || extension == ".jks" || extension == ".keystore" {
			return "", errors.New("sensitive path is platform managed")
		}
	}
	lower := strings.ToLower(filepath.ToSlash(clean))
	if lower == "wanxiangagent.md" || lower == "wanxiangagentworkmission.md" || lower == "deploy" || strings.HasPrefix(lower, "deploy/") {
		return "", fmt.Errorf("platform path is not writable by worker")
	}
	return clean, nil
}
