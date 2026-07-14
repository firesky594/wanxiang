package mr

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wanxiang-agent/server/internal/agents"
	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/events"
	"wanxiang-agent/server/internal/gitx"
	"wanxiang-agent/server/internal/issues"
	"wanxiang-agent/server/internal/tasks"
	"wanxiang-agent/server/internal/testutil"
)

func TestManagerMergeRejectsNonManager(t *testing.T) {
	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	taskSvc := tasks.NewService(cfg, conn, events.NewBus(conn))
	task, err := taskSvc.CreateTask(context.Background(), "Build login", "Create login API and UI")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	mrSvc := NewService(cfg, conn, events.NewBus(conn), agents.NewService(cfg, conn))
	created, err := mrSvc.Create(context.Background(), task.ProjectID, task.ID, "Backend work", "agent/backend/task-1", "backend-dev")
	if err != nil {
		t.Fatalf("Create MR: %v", err)
	}
	if err := mrSvc.ManagerMerge(context.Background(), created.ID, "backend-dev"); err == nil {
		t.Fatalf("non-manager merge should fail")
	}
}

func TestManagerMergeMergesSourceBranchIntoMain(t *testing.T) {
	ctx := context.Background()
	cfg, conn, task, projectDir := setupMergeProject(t)
	commitBranchFile(t, projectDir, "agent/backend/task-1", "feature.txt", "merged content\n")
	managerSvc := setManagerReady(t, cfg, conn)

	bus := events.NewBus(conn)
	svc := NewService(cfg, conn, bus, managerSvc)
	created, err := svc.Create(ctx, task.ProjectID, task.ID, "Backend work", "agent/backend/task-1", "backend-dev")
	if err != nil {
		t.Fatalf("Create MR: %v", err)
	}
	if err := svc.ManagerMerge(ctx, created.ID, "manager"); err != nil {
		t.Fatalf("ManagerMerge: %v", err)
	}

	content, err := gitx.Run(ctx, projectDir, "show", "main:feature.txt")
	if err != nil {
		t.Fatalf("git show main:feature.txt: %v: %s", err, content)
	}
	if content != "merged content\n" {
		t.Fatalf("content=%q", content)
	}
	var status string
	if err := conn.QueryRow(`select status from merge_requests where id=?`, created.ID).Scan(&status); err != nil {
		t.Fatalf("load MR status: %v", err)
	}
	if status != "merged" {
		t.Fatalf("status=%q", status)
	}
	rows, err := conn.Query(`select event_type,actor from runtime_events where task_id=? and event_type in ('mr.created','mr.merged') order by id`, task.ID)
	if err != nil {
		t.Fatalf("query MR events: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var eventType, actor string
		if err := rows.Scan(&eventType, &actor); err != nil {
			t.Fatalf("scan MR event: %v", err)
		}
		got = append(got, eventType+":"+actor)
	}
	if strings.Join(got, ",") != "mr.created:backend-dev,mr.merged:manager" {
		t.Fatalf("events=%v", got)
	}
}

func TestManagerMergeBlockingIssues(t *testing.T) {
	tests := []struct {
		name   string
		mrLink bool
	}{
		{name: "direct MR issue", mrLink: true},
		{name: "task-level issue", mrLink: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			cfg, conn, task, projectDir := setupMergeProject(t)
			commitBranchFile(t, projectDir, "agent/backend/task-1", "blocked.txt", "blocked\n")
			managerSvc := setManagerReady(t, cfg, conn)
			bus := events.NewBus(conn)
			issueSvc := issues.NewService(conn)
			svc := NewService(cfg, conn, bus, managerSvc, issueSvc)
			created, err := svc.Create(ctx, task.ProjectID, task.ID, "Blocked work", "agent/backend/task-1", "backend-dev")
			if err != nil {
				t.Fatalf("Create MR: %v", err)
			}
			mrID := int64(0)
			if tt.mrLink {
				mrID = created.ID
			}
			taskID := task.ID
			if _, err := issueSvc.Create(ctx, issues.CreateIssueInput{TaskID: &taskID, MRID: mrID, Title: "Stop", Body: "Human block", Blocking: true, CreatedBy: "admin"}); err != nil {
				t.Fatalf("Create issue: %v", err)
			}

			err = svc.ManagerMerge(ctx, created.ID, "manager")
			if err == nil || !strings.Contains(err.Error(), "blocked") {
				t.Fatalf("err=%v", err)
			}
			assertMRStatus(t, conn, created.ID, "open")
		})
	}
}

func TestManagerMergeRequiresReadyManager(t *testing.T) {
	ctx := context.Background()
	cfg, conn, task, projectDir := setupMergeProject(t)
	commitBranchFile(t, projectDir, "agent/backend/task-1", "feature.txt", "content\n")
	agentSvc := agents.NewService(cfg, conn)
	if _, err := agentSvc.EnsureManager(ctx); err != nil {
		t.Fatalf("EnsureManager: %v", err)
	}
	svc := NewService(cfg, conn, events.NewBus(conn), agentSvc)
	created, err := svc.Create(ctx, task.ProjectID, task.ID, "Work", "agent/backend/task-1", "backend-dev")
	if err != nil {
		t.Fatalf("Create MR: %v", err)
	}

	err = svc.ManagerMerge(ctx, created.ID, "manager")
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("err=%v", err)
	}
	assertMRStatus(t, conn, created.ID, "open")
}

func TestManagerMergeRejectsNonMainTargetAndEscapedProject(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, cfg config.Config, projectID, mrID int64, conn execer)
	}{
		{
			name: "non-main target",
			mutate: func(t *testing.T, _ config.Config, _ int64, mrID int64, conn execer) {
				mustExec(t, conn, `update merge_requests set target_branch='develop' where id=?`, mrID)
			},
		},
		{
			name: "project outside root",
			mutate: func(t *testing.T, _ config.Config, projectID, _ int64, conn execer) {
				mustExec(t, conn, `update projects set dir=? where id=?`, t.TempDir(), projectID)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			cfg, conn, task, projectDir := setupMergeProject(t)
			commitBranchFile(t, projectDir, "agent/backend/task-1", "feature.txt", "content\n")
			managerSvc := setManagerReady(t, cfg, conn)
			svc := NewService(cfg, conn, events.NewBus(conn), managerSvc)
			created, err := svc.Create(ctx, task.ProjectID, task.ID, "Work", "agent/backend/task-1", "backend-dev")
			if err != nil {
				t.Fatalf("Create MR: %v", err)
			}
			tt.mutate(t, cfg, task.ProjectID, created.ID, conn)

			if err := svc.ManagerMerge(ctx, created.ID, "manager"); err == nil {
				t.Fatalf("invalid merge should fail")
			}
			assertMRStatus(t, conn, created.ID, "open")
		})
	}
}

func TestManagerMergeAbortsConflictAndLeavesMROpen(t *testing.T) {
	ctx := context.Background()
	cfg, conn, task, projectDir := setupMergeProject(t)
	if err := os.WriteFile(filepath.Join(projectDir, "conflict.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatalf("write main conflict: %v", err)
	}
	mustGit(t, projectDir, "add", "conflict.txt")
	mustGit(t, projectDir, "commit", "-m", "test: add main conflict")
	mustGit(t, projectDir, "checkout", "-b", "agent/backend/conflict")
	if err := os.WriteFile(filepath.Join(projectDir, "conflict.txt"), []byte("branch\n"), 0o644); err != nil {
		t.Fatalf("write branch conflict: %v", err)
	}
	mustGit(t, projectDir, "add", "conflict.txt")
	mustGit(t, projectDir, "commit", "-m", "test: branch conflict")
	mustGit(t, projectDir, "checkout", "main")
	if err := os.WriteFile(filepath.Join(projectDir, "conflict.txt"), []byte("main changed\n"), 0o644); err != nil {
		t.Fatalf("write changed main conflict: %v", err)
	}
	mustGit(t, projectDir, "add", "conflict.txt")
	mustGit(t, projectDir, "commit", "-m", "test: change main conflict")
	managerSvc := setManagerReady(t, cfg, conn)
	svc := NewService(cfg, conn, events.NewBus(conn), managerSvc)
	created, err := svc.Create(ctx, task.ProjectID, task.ID, "Conflict", "agent/backend/conflict", "backend-dev")
	if err != nil {
		t.Fatalf("Create MR: %v", err)
	}

	if err := svc.ManagerMerge(ctx, created.ID, "manager"); err == nil {
		t.Fatalf("conflicting merge should fail")
	}
	assertMRStatus(t, conn, created.ID, "open")
	if _, err := os.Stat(filepath.Join(projectDir, ".git", "MERGE_HEAD")); !os.IsNotExist(err) {
		t.Fatalf("merge was not aborted: %v", err)
	}
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func setupMergeProject(t *testing.T) (config.Config, *sql.DB, tasks.Task, string) {
	t.Helper()
	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	taskSvc := tasks.NewService(cfg, conn, events.NewBus(conn))
	task, err := taskSvc.CreateTask(context.Background(), "Merge work", "Create merge fixture")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return cfg, conn, task, filepath.Join(cfg.ProjectDir, task.ProjectSlug)
}

func commitBranchFile(t *testing.T, repoDir, branch, name, content string) {
	t.Helper()
	mustGit(t, repoDir, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(repoDir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write branch file: %v", err)
	}
	mustGit(t, repoDir, "add", name)
	mustGit(t, repoDir, "commit", "-m", "test: add branch file")
}

func setManagerReady(t *testing.T, cfg config.Config, conn *sql.DB) *agents.Service {
	t.Helper()
	svc := agents.NewService(cfg, conn)
	if _, err := svc.EnsureManager(context.Background()); err != nil {
		t.Fatalf("EnsureManager: %v", err)
	}
	if err := svc.SaveManagerSecret(context.Background(), "MANAGER_API_KEY", "test-key"); err != nil {
		t.Fatalf("SaveManagerSecret: %v", err)
	}
	if _, err := svc.EnsureManager(context.Background()); err != nil {
		t.Fatalf("EnsureManager ready: %v", err)
	}
	return svc
}

func mustGit(t *testing.T, repoDir string, args ...string) {
	t.Helper()
	out, err := gitx.Run(context.Background(), repoDir, args...)
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func mustExec(t *testing.T, conn execer, query string, args ...any) {
	t.Helper()
	if _, err := conn.Exec(query, args...); err != nil {
		t.Fatalf("Exec: %v", err)
	}
}

func assertMRStatus(t *testing.T, conn *sql.DB, mrID int64, want string) {
	t.Helper()
	var got string
	if err := conn.QueryRow(`select status from merge_requests where id=?`, mrID).Scan(&got); err != nil {
		t.Fatalf("load MR status: %v", err)
	}
	if got != want {
		t.Fatalf("status=%q want=%q", got, want)
	}
}
