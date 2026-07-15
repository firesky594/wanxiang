package deliveries

import (
	"context"
	"database/sql"
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
