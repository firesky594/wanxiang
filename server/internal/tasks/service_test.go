package tasks

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/events"
	"wanxiang-agent/server/internal/gitx"
	"wanxiang-agent/server/internal/testutil"
)

func TestListTasksReturnsNewestFirst(t *testing.T) {
	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	svc := NewService(cfg, conn, events.NewBus(conn))
	first := insertTaskFixture(t, conn, "first", "2026-07-14T01:00:00Z")
	second := insertTaskFixture(t, conn, "second", "2026-07-14T02:00:00Z")

	got, err := svc.List(context.Background(), 10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 || got[0].ID != second || got[1].ID != first {
		t.Fatalf("tasks=%+v", got)
	}
}

func TestGetTaskReturnsProjectStepsAndEdges(t *testing.T) {
	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	svc := NewService(cfg, conn, events.NewBus(conn))
	taskID := insertTaskFixture(t, conn, "detail", "2026-07-14T01:00:00Z")
	res, err := conn.Exec(`insert into task_steps(task_id,agent_name,kind,status,input,output,created_at) values(?,?,?,?,?,?,?)`, taskID, "manager", "plan", "created", "in", "", "2026-07-14T01:00:01Z")
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ := res.LastInsertId()
	if _, err := conn.Exec(`insert into workflow_edges(task_id,from_step_id,to_step_id,label,created_at) values(?,?,?,?,?)`, taskID, nil, stepID, "starts", "2026-07-14T01:00:02Z"); err != nil {
		t.Fatal(err)
	}

	got, err := svc.Get(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Project.ID == 0 || got.Task.ProjectSlug == "" || len(got.Steps) != 1 || len(got.Edges) != 1 {
		t.Fatalf("detail=%+v", got)
	}
	if _, err := svc.Get(context.Background(), 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing err=%v", err)
	}
}

func TestUpdateTaskStatusEnforcesTransitions(t *testing.T) {
	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	svc := NewService(cfg, conn, events.NewBus(conn))
	taskID := insertTaskFixture(t, conn, "status", "2026-07-14T01:00:00Z")

	if _, err := svc.UpdateStatus(context.Background(), taskID, "completed", "admin"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("invalid transition err=%v", err)
	}
	got, err := svc.UpdateStatus(context.Background(), taskID, "planning", "manager")
	if err != nil || got.Status != "planning" {
		t.Fatalf("UpdateStatus task=%+v err=%v", got, err)
	}
	var eventType string
	if err := conn.QueryRow(`select event_type from runtime_events where task_id=? order by id desc limit 1`, taskID).Scan(&eventType); err != nil {
		t.Fatal(err)
	}
	if eventType != "task.status_changed" {
		t.Fatalf("event=%q", eventType)
	}
}

func insertTaskFixture(t *testing.T, conn *sql.DB, title, createdAt string) int64 {
	t.Helper()
	res, err := conn.Exec(`insert into projects(slug,dir,status,remote_url,created_at) values(?,?,?,?,?)`, "project-"+title, "/tmp/"+title, "created", "", createdAt)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := res.LastInsertId()
	res, err = conn.Exec(`insert into tasks(project_id,title,description,status,priority,created_at) values(?,?,?,?,0,?)`, projectID, title, title+" description", "created", createdAt)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()
	return taskID
}

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

func TestCreateTaskWithInputReusesRegisteredCleanMainProject(t *testing.T) {
	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	svc := NewService(cfg, conn, events.NewBus(conn))
	first, err := svc.CreateTask(context.Background(), "First", "new project")
	if err != nil {
		t.Fatal(err)
	}

	second, err := svc.CreateTaskWithInput(context.Background(), CreateTaskInput{Title: "Second", Description: "same project", ProjectID: &first.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	if second.ProjectID != first.ProjectID || second.ProjectSlug != first.ProjectSlug {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	var projects, tasks int
	_ = conn.QueryRow(`select count(*) from projects`).Scan(&projects)
	_ = conn.QueryRow(`select count(*) from tasks`).Scan(&tasks)
	if projects != 1 || tasks != 2 {
		t.Fatalf("projects=%d tasks=%d", projects, tasks)
	}
}

func TestCreateTaskWithInputRejectsUnsafeOrDirtyRegisteredProject(t *testing.T) {
	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	svc := NewService(cfg, conn, events.NewBus(conn))
	missing := int64(999)
	if _, err := svc.CreateTaskWithInput(context.Background(), CreateTaskInput{Title: "missing", ProjectID: &missing}); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("missing err=%v", err)
	}
	result, err := conn.Exec(`insert into projects(slug,dir,status,remote_url,created_at) values('outside','/tmp/outside-project','created','','now')`)
	if err != nil {
		t.Fatal(err)
	}
	outside, _ := result.LastInsertId()
	if _, err := svc.CreateTaskWithInput(context.Background(), CreateTaskInput{Title: "outside", ProjectID: &outside}); !errors.Is(err, ErrProjectConflict) {
		t.Fatalf("outside err=%v", err)
	}

	first, err := svc.CreateTask(context.Background(), "First", "new project")
	if err != nil {
		t.Fatal(err)
	}
	projectDir := filepath.Join(cfg.ProjectDir, first.ProjectSlug)
	if err := os.WriteFile(filepath.Join(projectDir, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateTaskWithInput(context.Background(), CreateTaskInput{Title: "dirty", ProjectID: &first.ProjectID}); !errors.Is(err, ErrProjectConflict) {
		t.Fatalf("dirty err=%v", err)
	}
	if err := os.Remove(filepath.Join(projectDir, "dirty.txt")); err != nil {
		t.Fatal(err)
	}
	if output, err := gitx.Run(context.Background(), projectDir, "checkout", "-b", "develop"); err != nil {
		t.Fatalf("checkout: %v %s", err, output)
	}
	if _, err := svc.CreateTaskWithInput(context.Background(), CreateTaskInput{Title: "wrong branch", ProjectID: &first.ProjectID}); !errors.Is(err, ErrProjectConflict) {
		t.Fatalf("branch err=%v", err)
	}
	var count int
	_ = conn.QueryRow(`select count(*) from tasks`).Scan(&count)
	if count != 1 {
		t.Fatalf("tasks=%d", count)
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
