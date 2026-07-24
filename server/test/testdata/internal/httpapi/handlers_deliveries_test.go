package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"wanxiang-agent/server/internal/deliveries"
	"wanxiang-agent/server/internal/events"
	"wanxiang-agent/server/internal/testutil"
)

func TestAdminDeliveryAPIRequiresAuthAndAcceptsDecision(t *testing.T) {
	db := testutil.OpenDB(t)
	svc := deliveries.NewService(db, events.NewBus(db))
	snap := seedDelivery(t, db, svc)
	router := NewRouter(Dependencies{DB: db, Deliveries: svc})
	if res := adminRequest(router, "", http.MethodGet, "/api/admin/deliveries", ""); res.Code != http.StatusUnauthorized {
		t.Fatalf("unauth=%d", res.Code)
	}
	seedAdmin(t, db, "admin", "secret123")
	login := adminRequest(router, "", http.MethodPost, "/api/admin/login", `{"username":"admin","password":"secret123"}`)
	var auth struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(login.Body.Bytes(), &auth)
	list := adminRequest(router, auth.Token, http.MethodGet, "/api/admin/deliveries", "")
	if list.Code != 200 || !strings.Contains(list.Body.String(), "awaiting_acceptance") {
		t.Fatalf("list=%d %s", list.Code, list.Body.String())
	}
	detail := adminRequest(router, auth.Token, http.MethodGet, "/api/admin/deliveries/"+itoa(snap.ID), "")
	if detail.Code != 200 || !strings.Contains(detail.Body.String(), "go test") {
		t.Fatalf("detail=%d %s", detail.Code, detail.Body.String())
	}
	decision := adminRequest(router, auth.Token, http.MethodPost, "/api/admin/deliveries/"+itoa(snap.ID)+"/decisions", `{"decision":"accepted","comment":"ok","idempotency_key":"web-1"}`)
	if decision.Code != 200 || !strings.Contains(decision.Body.String(), "completed") {
		t.Fatalf("decision=%d %s", decision.Code, decision.Body.String())
	}
}

func TestAdminDeliveryAPIMapsDecisionErrors(t *testing.T) {
	db := testutil.OpenDB(t)
	svc := deliveries.NewService(db, nil)
	snap := seedDelivery(t, db, svc)
	router := NewRouter(Dependencies{DB: db, Deliveries: svc})
	seedAdmin(t, db, "admin", "secret123")
	login := adminRequest(router, "", http.MethodPost, "/api/admin/login", `{"username":"admin","password":"secret123"}`)
	var auth struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(login.Body.Bytes(), &auth)
	res := adminRequest(router, auth.Token, http.MethodPost, "/api/admin/deliveries/"+itoa(snap.ID)+"/decisions", `{"decision":"rejected","idempotency_key":"bad"}`)
	if res.Code != 400 || !strings.Contains(res.Body.String(), "decision_comment_required") {
		t.Fatalf("%d %s", res.Code, res.Body.String())
	}
}

func seedDelivery(t *testing.T, db *sql.DB, svc *deliveries.Service) deliveries.Snapshot {
	t.Helper()
	p, _ := db.Exec(`insert into projects(slug,dir,status,remote_url,created_at) values('delivery','/tmp/delivery','active','','now')`)
	projectID, _ := p.LastInsertId()
	ta, _ := db.Exec(`insert into tasks(project_id,title,description,status,created_at) values(?,'delivery','d','merged','now')`, projectID)
	taskID, _ := ta.LastInsertId()
	_, _ = db.Exec(`insert into task_plan_versions(task_id,version,status,created_at) values(?,1,'completed','now')`, taskID)
	st, _ := db.Exec(`insert into task_steps(task_id,agent_name,kind,status,input,created_at,plan_version) values(?,'worker','backend','completed','{}','now',1)`, taskID)
	stepID, _ := st.LastInsertId()
	rep, _ := db.Exec(`insert into completion_reports(project_id,task_id,step_id,lease_id,lease_version,agent_name,agent_role,version,source_branch,checkpoint_commit,head_commit,completed_json,incomplete_json,key_files_json,tests_json,risks_json,dependencies_json,merge_order_json,created_at) values(?,?,?,'l',1,'worker','backend',1,'agent/worker/x','a','b','["done"]','[]','["a.go"]','[{"command":"go test","status":"passed"}]','[]','[]','[]','now')`, projectID, taskID, stepID)
	reportID, _ := rep.LastInsertId()
	mr, _ := db.Exec(`insert into merge_requests(project_id,task_id,title,source_branch,target_branch,status,created_by,created_at,report_id,step_id,source_commit,merge_commit,project_lead) values(?,?,'mr','agent/worker/x','main','merged','worker','now',?,?,'b','c','worker')`, projectID, taskID, reportID, stepID)
	mrID, _ := mr.LastInsertId()
	n, _ := db.Exec(`insert into manager_notifications(project_id,task_id,mr_id,report_id,project_lead,main_commit,payload_json,status,created_at) values(?,?,?,?,'worker','c','{}','pending','now')`, projectID, taskID, mrID, reportID)
	notificationID, _ := n.LastInsertId()
	snap, err := svc.BuildSnapshot(context.Background(), notificationID)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}
