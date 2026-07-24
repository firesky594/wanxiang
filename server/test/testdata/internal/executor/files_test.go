package executor

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/leases"
	"wanxiang-agent/server/internal/testutil"
	"wanxiang-agent/server/internal/workspaces"
)

func TestReadFileWriteFileWithinLeaseScope(t *testing.T) {
	tools, ref, root := fileToolsFixture(t)
	path := filepath.Join(root, "src", "main.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := tools.ReadFile(t.Context(), ref, "src/main.go")
	if err != nil || string(got) != "package old\n" {
		t.Fatalf("read=%q err=%v", got, err)
	}
	if err := tools.WriteFile(t.Context(), ref, "src/main.go", []byte("package main\n")); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != "package main\n" {
		t.Fatalf("written=%q", got)
	}
}

func TestReadFileWriteFileRejectUnsafePathsAndBadLease(t *testing.T) {
	tools, ref, root := fileToolsFixture(t)
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "src", "link")); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/etc/passwd", "../outside", "README.md", "src/link", "src/.env", "src/env", ".git/config", "wanxiangAgent.md", "wanxiangAgentWorkMission.md", "deploy/app.env"} {
		t.Run(path, func(t *testing.T) {
			if _, err := tools.ReadFile(t.Context(), ref, path); err == nil {
				t.Fatal("expected rejection")
			}
			if err := tools.WriteFile(t.Context(), ref, path, []byte("x")); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
	bad := ref
	bad.LeaseVersion++
	if _, err := tools.ReadFile(t.Context(), bad, "src/main.go"); !errors.Is(err, leases.ErrConflict) {
		t.Fatalf("err=%v", err)
	}
}

func TestWriteFileFailurePreservesOriginal(t *testing.T) {
	tools, ref, root := fileToolsFixture(t)
	path := filepath.Join(root, "src", "main.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("original"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := tools.WriteFile(context.Background(), ref, "src/main.go", bytes.Repeat([]byte("x"), maxWriteBytes+1)); err == nil {
		t.Fatal("expected oversized write rejection")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "original" {
		t.Fatalf("original changed: %q", got)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
}

func fileToolsFixture(t *testing.T) (*FileTools, leases.LeaseRef, string) {
	t.Helper()
	conn := testutil.OpenDB(t)
	root := t.TempDir()
	git(t, root, "init", "-b", "agent/agent-a/files")
	git(t, root, "config", "user.name", "Test")
	git(t, root, "config", "user.email", "test@example.com")
	git(t, root, "commit", "--allow-empty", "-m", "初始化测试工作区")
	base := strings.TrimSpace(git(t, root, "rev-parse", "HEAD"))
	projectID := mustInsertID(t, conn, `insert into projects(slug,dir,status,remote_url,created_at) values('executor-files',?,'created','','now')`, root)
	taskID := mustInsertID(t, conn, `insert into tasks(project_id,title,description,status,created_at) values(?,'files','test','workspace_ready','now')`, projectID)
	stepID := mustInsertID(t, conn, `insert into task_steps(task_id,agent_name,kind,status,input,created_at) values(?,'agent-a','backend','assigned','{}','now')`, taskID)
	if _, err := conn.Exec(`insert into task_assignments(task_id,step_id,agent_name,status,decision_id,created_at) values(?,?,'agent-a','assigned',1,'now')`, taskID, stepID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`insert into project_workspaces(project_id,task_id,step_id,assignment_id,agent_name,branch_name,worktree_path,base_commit,provision_commit,write_scope_json,metadata_hash,status,created_at,updated_at) values(?,?,?,1,'agent-a','agent/agent-a/files',?,?,?,'["src"]','hash','ready','now','now')`, projectID, taskID, stepID, root, base, base); err != nil {
		t.Fatal(err)
	}
	workspaceService := workspaces.NewService(config.Config{}, conn, nil)
	leaseService := leases.NewService(conn, nil, workspaceService)
	lease, err := leaseService.Acquire(t.Context(), taskID, stepID, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	return NewFileTools(conn, leaseService), lease.LeaseRef, root
}

func mustInsertID(t *testing.T, conn *sql.DB, query string, args ...any) int64 {
	t.Helper()
	result, err := conn.Exec(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
