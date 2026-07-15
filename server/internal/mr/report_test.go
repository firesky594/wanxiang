package mr

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/events"
	"wanxiang-agent/server/internal/gitx"
	"wanxiang-agent/server/internal/testutil"
)

type reportFixture struct {
	service *Service
	db      *sql.DB
	input   CompletionReportInput
}

func newReportFixture(t *testing.T) reportFixture {
	t.Helper()
	root := t.TempDir()
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	db := testutil.OpenDB(t)
	repo := filepath.Join(cfg.ProjectDir, "report-project")
	mustReportGit(t, root, "init", "--initial-branch=main", repo)
	mustReportGit(t, repo, "config", "user.email", "test@example.com")
	mustReportGit(t, repo, "config", "user.name", "Test")
	mustReportGit(t, repo, "commit", "--allow-empty", "-m", "初始化")
	branch := "agent/worker/report"
	mustReportGit(t, repo, "checkout", "-b", branch)
	mustReportGit(t, repo, "commit", "--allow-empty", "-m", "完成报告")
	head := strings.TrimSpace(mustReportGit(t, repo, "rev-parse", "HEAD"))
	now := time.Now().UTC()
	project, _ := db.Exec(`insert into projects(slug,dir,status,remote_url,created_at) values('report-project',?,'created','','now')`, repo)
	projectID, _ := project.LastInsertId()
	task, _ := db.Exec(`insert into tasks(project_id,title,description,status,created_at) values(?,'report','report','workspace_ready','now')`, projectID)
	taskID, _ := task.LastInsertId()
	step, _ := db.Exec(`insert into task_steps(task_id,agent_name,kind,status,input,lease_id,lease_version,created_at) values(?,'worker','backend','in_progress','{}','lease_report',1,'now')`, taskID)
	stepID, _ := step.LastInsertId()
	assignment, _ := db.Exec(`insert into task_assignments(task_id,step_id,agent_name,reports_to,status,decision_id,created_at) values(?,?,'worker','lead','assigned',1,'now')`, taskID, stepID)
	assignmentID, _ := assignment.LastInsertId()
	_, _ = db.Exec(`insert into team_decisions(task_id,project_lead,requires_lead,reason,created_at) values(?,'lead',1,'multi','now')`, taskID)
	_, _ = db.Exec(`insert into project_workspaces(project_id,task_id,step_id,assignment_id,agent_name,reports_to,branch_name,worktree_path,base_commit,provision_commit,write_scope_json,metadata_hash,status,created_at,updated_at) values(?,?,?,?, 'worker','lead',?,?,?,?,'["."]','hash','ready','now','now')`, projectID, taskID, stepID, assignmentID, branch, repo, head, head)
	_, _ = db.Exec(`insert into task_step_leases(task_id,step_id,agent_name,lease_id,lease_version,status,branch_name,worktree_path,acquired_at,expires_at,last_heartbeat_at,created_at,updated_at) values(?,?,'worker','lease_report',1,'active',?,?,?, ?,?,'now','now')`, taskID, stepID, branch, repo, now.Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	cp, _ := db.Exec(`insert into task_checkpoints(task_id,step_id,lease_id,idempotency_key,git_commit,branch_name,clean,files_json,tests_json,summary_json,summary_hash,created_at) values(?,?,'lease_report','report',?, ?,1,'[]','[]','{}','hash','now')`, taskID, stepID, head, branch)
	cpID, _ := cp.LastInsertId()
	_, _ = db.Exec(`update task_steps set checkpoint_id=? where id=?`, cpID, stepID)
	input := CompletionReportInput{AgentName: "worker", Role: "worker", ProjectID: projectID, TaskID: taskID, StepID: stepID, LeaseID: "lease_report", LeaseVersion: 1, SourceBranch: branch, CheckpointCommit: head, HeadCommit: head, Completed: []string{"完成"}, Tests: []TestEvidence{{Command: "go test ./...", Status: "passed"}}}
	return reportFixture{service: NewService(cfg, db, events.NewBus(db), nil), db: db, input: input}
}

func TestSubmitReportCreatesReportMRAndEventsAtomically(t *testing.T) {
	fixture := newReportFixture(t)
	detail, err := fixture.service.SubmitReport(t.Context(), Principal{Name: "worker", Role: "worker"}, fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Report.Version != 1 || detail.MergeRequest.Status != MRPendingReview {
		t.Fatalf("detail=%+v", detail)
	}
	for table, want := range map[string]int{"completion_reports": 1, "merge_requests": 1, "runtime_events": 2} {
		var count int
		if err := fixture.db.QueryRow(`select count(*) from ` + table).Scan(&count); err != nil || count != want {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	if _, err := fixture.service.SubmitReport(t.Context(), Principal{Name: "worker", Role: "worker"}, fixture.input); !errorsIs(err, ErrStateConflict) {
		t.Fatalf("duplicate err=%v", err)
	}
}

func TestSubmitReportRollsBackWhenSecondEventFails(t *testing.T) {
	fixture := newReportFixture(t)
	if _, err := fixture.db.Exec(`create trigger reject_mr_event before insert on runtime_events when NEW.event_type='mr.created' begin select raise(abort,'event rejected'); end`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.SubmitReport(t.Context(), Principal{Name: "worker", Role: "worker"}, fixture.input); err == nil {
		t.Fatal("submit succeeded")
	}
	for _, table := range []string{"completion_reports", "merge_requests", "runtime_events"} {
		var count int
		if err := fixture.db.QueryRow(`select count(*) from ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
}

func mustReportGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitx.Run(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return out
}

func errorsIs(err, target error) bool {
	return err != nil && (err == target || strings.Contains(err.Error(), target.Error()))
}
