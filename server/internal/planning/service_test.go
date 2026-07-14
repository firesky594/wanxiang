package planning

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/providers"
	"wanxiang-agent/server/internal/testutil"
)

type fakePlanner struct {
	result providers.Result
	err    error
	calls  int
}

func (f *fakePlanner) ChatAgent(context.Context, string, []providers.Message, int) (providers.Result, error) {
	f.calls++
	return f.result, f.err
}

func TestServicePlansCreatedTaskTransactionallyAndIdempotently(t *testing.T) {
	cfg, conn, taskID := planningFixture(t)
	fake := &fakePlanner{result: providers.Result{Content: `{"summary":"deliver","work_items":[{"key":"api","title":"API","description":"build api","kind":"backend","required_capabilities":["go"],"acceptance_criteria":["tests pass"],"depends_on":[]},{"key":"ui","title":"UI","description":"build ui","kind":"frontend","required_capabilities":["vue"],"acceptance_criteria":["build passes"],"depends_on":["api"]}]}`}}
	svc := NewService(cfg, conn, fake)
	plan, err := svc.PlanTask(t.Context(), taskID)
	if err != nil || len(plan.WorkItems) != 2 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	if _, err := svc.PlanTask(t.Context(), taskID); err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("calls=%d", fake.calls)
	}
	assertCount(t, conn, "task_steps", 2)
	assertCount(t, conn, "workflow_edges", 1)
	var status, summary string
	if err := conn.QueryRow(`select status,manager_summary from tasks where id=?`, taskID).Scan(&status, &summary); err != nil {
		t.Fatal(err)
	}
	if status != "planned" || summary != "deliver" {
		t.Fatalf("status=%q summary=%q", status, summary)
	}
}

func TestServiceBlocksInvalidPlanningOutputWithoutLeakingIt(t *testing.T) {
	cfg, conn, taskID := planningFixture(t)
	fake := &fakePlanner{result: providers.Result{Content: `private-key invalid`}}
	_, err := NewService(cfg, conn, fake).PlanTask(t.Context(), taskID)
	if err == nil {
		t.Fatal("expected error")
	}
	var status, summary string
	if err := conn.QueryRow(`select status,manager_summary from tasks where id=?`, taskID).Scan(&status, &summary); err != nil {
		t.Fatal(err)
	}
	if status != "blocked: planning_error" || strings.Contains(summary, "private-key") {
		t.Fatalf("status=%q summary=%q", status, summary)
	}
	assertCount(t, conn, "task_steps", 0)
}

func planningFixture(t *testing.T) (config.Config, *sql.DB, int64) {
	t.Helper()
	cfg, _ := config.Load(t.TempDir())
	conn := testutil.OpenDB(t)
	dir := filepath.Join(cfg.AgentDir, "manager")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "system_prompt.md"), []byte("plan safely"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := conn.Exec(`insert into projects(slug,dir,status,remote_url,created_at) values('p','/tmp/p','created','','now')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := res.LastInsertId()
	res, err = conn.Exec(`insert into tasks(project_id,title,description,status,created_at) values(?,?,?,'created','now')`, projectID, "task", "description")
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()
	return cfg, conn, taskID
}

func assertCount(t *testing.T, conn *sql.DB, table string, want int) {
	t.Helper()
	var got int
	if err := conn.QueryRow(`select count(*) from ` + table).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s count=%d want=%d", table, got, want)
	}
}
