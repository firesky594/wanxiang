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
	result   providers.Result
	err      error
	calls    int
	messages []providers.Message
}

func (f *fakePlanner) ChatAgent(_ context.Context, _ string, messages []providers.Message, _ int) (providers.Result, error) {
	f.calls++
	f.messages = messages
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
	var eventTypes string
	if err := conn.QueryRow(`select group_concat(event_type, ',') from runtime_events where task_id=? order by id`, taskID).Scan(&eventTypes); err != nil {
		t.Fatal(err)
	}
	if eventTypes != "task.planning.started,task.planning.completed" {
		t.Fatalf("events=%q", eventTypes)
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

func TestServicePlansReworkIntoNewVersionWithoutChangingHistory(t *testing.T) {
	cfg, conn, taskID := planningFixture(t)
	_, _ = conn.Exec(`update tasks set status='rework_planning' where id=?`, taskID)
	_, _ = conn.Exec(`insert into task_plan_versions(task_id,version,status,summary,created_at) values(?,1,'completed','old','now')`, taskID)
	_, _ = conn.Exec(`insert into task_steps(task_id,agent_name,kind,status,input,created_at,plan_version) values(?,'worker','backend','completed','{}','now',1)`, taskID)
	var projectID int64
	_ = conn.QueryRow(`select project_id from tasks where id=?`, taskID).Scan(&projectID)
	snapshot, _ := conn.Exec(`insert into delivery_snapshots(task_id,project_id,version,manager_notification_id,main_commit,status,summary,summary_hash,evidence_json,created_by,created_at) values(?,?,1,99,'abc','revision_requested','delivery','hash','{"tests":[{"command":"go test ./..."}]}','manager','now')`, taskID, projectID)
	snapshotID, _ := snapshot.LastInsertId()
	_, _ = conn.Exec(`insert into task_plan_versions(task_id,version,source_snapshot_id,status,summary,created_at) values(?,2,?,'planning','','now')`, taskID, snapshotID)
	fake := &fakePlanner{result: providers.Result{Content: `{"summary":"rework","work_items":[{"key":"fix","title":"修正","description":"补充","kind":"backend","required_capabilities":["go"],"acceptance_criteria":["测试通过"],"depends_on":[]}]}`}}
	plan, err := NewService(cfg, conn, fake).PlanRework(t.Context(), taskID, 2, "补充移动端")
	if err != nil || plan.Summary != "rework" {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	var oldCount, newCount int
	_ = conn.QueryRow(`select count(*) from task_steps where task_id=? and plan_version=1 and status='completed'`, taskID).Scan(&oldCount)
	_ = conn.QueryRow(`select count(*) from task_steps where task_id=? and plan_version=2 and status='created'`, taskID).Scan(&newCount)
	if oldCount != 1 || newCount != 1 {
		t.Fatalf("old=%d new=%d", oldCount, newCount)
	}
	if len(fake.messages) == 0 || !strings.Contains(fake.messages[len(fake.messages)-1].Content, "go test ./...") {
		t.Fatal("rework evidence was not sent to manager")
	}
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
