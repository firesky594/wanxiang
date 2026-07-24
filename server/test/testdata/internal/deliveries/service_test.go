package deliveries

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"wanxiang-agent/server/internal/events"
	"wanxiang-agent/server/internal/testutil"
)

func TestBuildSnapshotIsCompleteAndIdempotent(t *testing.T) {
	db := testutil.OpenDB(t)
	taskID, notificationID := deliveryFixture(t, db)
	svc := NewService(db, events.NewBus(db))
	first, err := svc.BuildSnapshot(context.Background(), notificationID)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	second, err := svc.BuildSnapshot(context.Background(), notificationID)
	if err != nil || second.ID != first.ID {
		t.Fatalf("idempotency: %#v %v", second, err)
	}
	if first.Status != "awaiting_acceptance" || len(first.Evidence.MergeRequests) != 1 || len(first.Evidence.Tests) != 1 {
		t.Fatalf("snapshot: %#v", first)
	}
	var status string
	_ = db.QueryRow(`select status from tasks where id=?`, taskID).Scan(&status)
	if status != "awaiting_acceptance" {
		t.Fatalf("task status=%s", status)
	}
}

func TestBuildSnapshotRejectsIncompleteCurrentPlan(t *testing.T) {
	db := testutil.OpenDB(t)
	_, notificationID := deliveryFixture(t, db)
	_, _ = db.Exec(`update task_steps set status='in_progress'`)
	_, err := NewService(db, nil).BuildSnapshot(context.Background(), notificationID)
	if err != ErrNotReady {
		t.Fatalf("err=%v", err)
	}
}

func TestBuildSnapshotRedactsSecretsAndCreatesHighRiskIssue(t *testing.T) {
	db := testutil.OpenDB(t)
	taskID, n := deliveryFixture(t, db)
	_, _ = db.Exec(`update completion_reports set risks_json='["生产部署 Bearer [TEST_TOKEN]"]',tests_json='[{"command":"curl -H Authorization:Bearer [TEST_TOKEN]","status":"passed"}]'`)
	snap, err := NewService(db, nil).BuildSnapshot(context.Background(), n)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(snap)
	if strings.Contains(string(encoded), "TEST_TOKEN") {
		t.Fatalf("secret leaked: %s", encoded)
	}
	var blockers int
	_ = db.QueryRow(`select count(*) from issues where task_id=? and blocking=1`, taskID).Scan(&blockers)
	if blockers != 1 {
		t.Fatalf("blockers=%d", blockers)
	}
}

func TestBuildSnapshotUsesLatestNotificationAndKeepsNotificationAudit(t *testing.T) {
	db := testutil.OpenDB(t)
	_, first := deliveryFixture(t, db)
	var taskID, projectID, mrID, reportID int64
	_ = db.QueryRow(`select task_id,project_id,mr_id,report_id from manager_notifications where id=?`, first).Scan(&taskID, &projectID, &mrID, &reportID)
	res, _ := db.Exec(`insert into manager_notifications(project_id,task_id,mr_id,report_id,project_lead,main_commit,payload_json,status,created_at) values(?,?,?,?,?,'latest','{}','pending','later')`, projectID, taskID, mrID+1, reportID, "lead")
	latest, _ := res.LastInsertId()
	snap, err := NewService(db, nil).BuildSnapshot(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if snap.MainCommit != "latest" {
		t.Fatalf("main_commit=%s", snap.MainCommit)
	}
	var links, pending int
	_ = db.QueryRow(`select count(*) from delivery_snapshot_notifications where snapshot_id=?`, snap.ID).Scan(&links)
	_ = db.QueryRow(`select count(*) from manager_notifications where task_id=? and status='pending'`, taskID).Scan(&pending)
	if links != 2 || pending != 0 || latest == 0 {
		t.Fatalf("links=%d pending=%d", links, pending)
	}
}

func TestBuildSnapshotCollectsCompleteEvidenceAndRedactsCredentialForms(t *testing.T) {
	db := testutil.OpenDB(t)
	_, n := deliveryFixture(t, db)
	_, _ = db.Exec(`update task_steps set input='{"secret":"PASSWORD=hunter2"}'`)
	_, _ = db.Exec(`update completion_reports set user_decision='TOKEN=private'`)
	_, _ = db.Exec(`insert into mr_reviews(mr_id,reviewer,role,status,body,created_at) select id,'lead','project_lead','approved','SECRET=value','now' from merge_requests limit 1`)
	snap, err := NewService(db, nil).BuildSnapshot(context.Background(), n)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(snap.Evidence)
	if len(snap.Evidence.WorkItems) != 1 || len(snap.Evidence.Reviews) != 1 || len(snap.Evidence.UserDecisions) != 1 || strings.Contains(string(encoded), "hunter2") || strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "value") {
		t.Fatalf("evidence=%s", encoded)
	}
}

func deliveryFixture(t *testing.T, db *sql.DB) (int64, int64) {
	t.Helper()
	p, _ := db.Exec(`insert into projects(slug,dir,status,remote_url,created_at) values('p','/tmp/p','active','','now')`)
	projectID, _ := p.LastInsertId()
	ta, _ := db.Exec(`insert into tasks(project_id,title,description,status,created_at) values(?, 't','d','merged','now')`, projectID)
	taskID, _ := ta.LastInsertId()
	_, _ = db.Exec(`insert into task_plan_versions(task_id,version,status,summary,created_at) values(?,1,'completed','plan','now')`, taskID)
	st, _ := db.Exec(`insert into task_steps(task_id,agent_name,kind,status,input,output,created_at,plan_version) values(?,'worker','backend','completed','{}','','now',1)`, taskID)
	stepID, _ := st.LastInsertId()
	rep, _ := db.Exec(`insert into completion_reports(project_id,task_id,step_id,lease_id,lease_version,agent_name,agent_role,version,source_branch,checkpoint_commit,head_commit,completed_json,incomplete_json,key_files_json,tests_json,risks_json,dependencies_json,merge_order_json,created_at) values(?,?,?,'l',1,'worker','backend',1,'agent/worker/x','a','b','["done"]','[]','["a.go"]','[{"command":"go test ./...","status":"passed"}]','[]','[]','[]','now')`, projectID, taskID, stepID)
	reportID, _ := rep.LastInsertId()
	mr, _ := db.Exec(`insert into merge_requests(project_id,task_id,title,source_branch,target_branch,status,created_by,created_at,report_id,step_id,source_commit,merge_commit,project_lead) values(?,?,'mr','agent/worker/x','main','merged','worker','now',?,?, 'b','c','worker')`, projectID, taskID, reportID, stepID)
	mrID, _ := mr.LastInsertId()
	n, _ := db.Exec(`insert into manager_notifications(project_id,task_id,mr_id,report_id,project_lead,main_commit,payload_json,status,created_at) values(?,?,?,?, 'worker','c','{}','pending','now')`, projectID, taskID, mrID, reportID)
	notificationID, _ := n.LastInsertId()
	return taskID, notificationID
}
