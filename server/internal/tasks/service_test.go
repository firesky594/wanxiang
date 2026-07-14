package tasks

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/events"
	"wanxiang-agent/server/internal/gitx"
	"wanxiang-agent/server/internal/testutil"
)

func TestCreateTaskInitializesProjectRepository(t *testing.T) {
	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	svc := NewService(cfg, conn, events.NewBus(conn))

	task, err := svc.CreateTask(context.Background(), "Build login", "Create login API and UI")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.ProjectID == 0 {
		t.Fatalf("ProjectID not set")
	}
	projectDir := filepath.Join(cfg.ProjectDir, task.ProjectSlug)
	if _, err := os.Stat(filepath.Join(projectDir, ".git")); err != nil {
		t.Fatalf("project git not initialized: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".wanxiang", "task.yaml")); err != nil {
		t.Fatalf("task.yaml missing: %v", err)
	}
	for key, want := range map[string]string{
		"user.name":  "Wanxiang Agent",
		"user.email": "wanxiang-agent@localhost",
	} {
		got, err := gitx.Run(context.Background(), projectDir, "config", "--local", "--get", key)
		if err != nil {
			t.Fatalf("git config %s: %v: %s", key, err, got)
		}
		if strings.TrimSpace(got) != want {
			t.Fatalf("git config %s=%q want=%q", key, strings.TrimSpace(got), want)
		}
	}
}

func TestCreateTaskCleansUpWhenGitAddFails(t *testing.T) {
	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	svc := NewService(cfg, conn, events.NewBus(conn))
	badIndex := t.TempDir()
	t.Setenv("GIT_INDEX_FILE", badIndex)

	_, err := svc.CreateTask(context.Background(), "Broken add", "force git add failure")
	if err == nil || !strings.Contains(err.Error(), "git add failed") {
		t.Fatalf("err=%v", err)
	}
	assertNoTaskRecordsOrProjects(t, conn, cfg.ProjectDir)
}

func TestCreateTaskCleansUpWhenGitCommitFails(t *testing.T) {
	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	svc := NewService(cfg, conn, events.NewBus(conn))
	t.Setenv("GIT_COMMITTER_DATE", "not-a-date")

	_, err := svc.CreateTask(context.Background(), "Broken commit", "force git commit failure")
	if err == nil || !strings.Contains(err.Error(), "git commit failed") {
		t.Fatalf("err=%v", err)
	}
	assertNoTaskRecordsOrProjects(t, conn, cfg.ProjectDir)
}

func TestCreateTaskPublishesPersistedEvent(t *testing.T) {
	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	svc := NewService(cfg, conn, events.NewBus(conn))

	task, err := svc.CreateTask(context.Background(), "Event task", "description-not-for-events")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	var eventType, actor, payload string
	if err := conn.QueryRow(`select event_type,actor,payload_json from runtime_events where task_id=?`, task.ID).Scan(&eventType, &actor, &payload); err != nil {
		t.Fatalf("load task event: %v", err)
	}
	if eventType != "task.created" || actor != "admin" {
		t.Fatalf("event_type=%q actor=%q", eventType, actor)
	}
	if strings.Contains(payload, "description-not-for-events") {
		t.Fatalf("task description leaked into event: %s", payload)
	}
}

func assertNoTaskRecordsOrProjects(t *testing.T, conn interface {
	QueryRow(query string, args ...any) *sql.Row
}, projectDir string) {
	t.Helper()
	for _, table := range []string{"projects", "tasks"} {
		var count int
		if err := conn.QueryRow("select count(*) from " + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s count=%d", table, count)
		}
	}
	entries, err := os.ReadDir(projectDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("ReadDir projects: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("project directory not cleaned: %v", entries)
	}
}
