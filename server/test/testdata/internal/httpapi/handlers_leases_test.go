package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"wanxiang-agent/server/internal/auth"
	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/leases"
	"wanxiang-agent/server/internal/testutil"
	"wanxiang-agent/server/internal/workspaces"
)

func TestAgentLeaseAPIUsesAuthenticatedIdentityAndReturnsConflict(t *testing.T) {
	router, conn, taskID, stepID, tokenA, tokenB := leaseRouterFixture(t)
	path := "/api/agent/tasks/" + itoa(taskID) + "/steps/" + itoa(stepID) + "/lease/acquire"
	res := agentRequest(router, tokenA, http.MethodPost, path, `{"agent_name":"agent-b"}`)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"agent_name":"agent-a"`) {
		t.Fatalf("acquire status=%d body=%s", res.Code, res.Body.String())
	}
	var payload struct {
		Lease leases.Lease `json:"lease"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil || payload.Lease.LeaseID == "" {
		t.Fatalf("decode=%v payload=%+v", err, payload)
	}
	heartbeatPath := "/api/agent/tasks/" + itoa(taskID) + "/steps/" + itoa(stepID) + "/lease/heartbeat"
	body := `{"lease_id":"` + payload.Lease.LeaseID + `","lease_version":` + itoa(payload.Lease.LeaseVersion) + `}`
	res = agentRequest(router, tokenB, http.MethodPost, heartbeatPath, body)
	if res.Code != http.StatusConflict || strings.Contains(res.Body.String(), payload.Lease.LeaseID) {
		t.Fatalf("conflict status=%d body=%s", res.Code, res.Body.String())
	}
	var count int
	_ = conn.QueryRow(`select count(*) from task_step_leases where step_id=?`, stepID).Scan(&count)
	if count != 1 {
		t.Fatalf("lease count=%d", count)
	}
}

func TestAdminLeaseTimelineAndFreezeWriteAudit(t *testing.T) {
	router, conn, taskID, stepID, tokenA, _ := leaseRouterFixture(t)
	acquire := agentRequest(router, tokenA, http.MethodPost, "/api/agent/tasks/"+itoa(taskID)+"/steps/"+itoa(stepID)+"/lease/acquire", `{}`)
	if acquire.Code != http.StatusOK {
		t.Fatalf("acquire=%d %s", acquire.Code, acquire.Body.String())
	}
	bootstrap := adminRequest(router, "", http.MethodPost, "/api/admin/bootstrap", `{"username":"admin","password":"secret123"}`)
	var session struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(bootstrap.Body.Bytes(), &session)
	freezePath := "/api/admin/tasks/" + itoa(taskID) + "/steps/" + itoa(stepID) + "/lease/freeze"
	res := adminRequest(router, session.Token, http.MethodPost, freezePath, `{"reason":"人工检查"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("freeze=%d %s", res.Code, res.Body.String())
	}
	res = adminRequest(router, session.Token, http.MethodGet, "/api/admin/tasks/"+itoa(taskID)+"/leases", "")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"status":"frozen"`) {
		t.Fatalf("timeline=%d %s", res.Code, res.Body.String())
	}
	var audits int
	_ = conn.QueryRow(`select count(*) from audit_logs where actor='admin' and action='lease.freeze'`).Scan(&audits)
	if audits != 1 {
		t.Fatalf("audits=%d", audits)
	}
}

func leaseRouterFixture(t *testing.T) (http.Handler, *sql.DB, int64, int64, string, string) {
	t.Helper()
	conn := testutil.OpenDB(t)
	project, _ := conn.Exec(`insert into projects(slug,dir,status,remote_url,created_at) values('lease-http','/tmp/lease-http','created','','now')`)
	projectID, _ := project.LastInsertId()
	task, _ := conn.Exec(`insert into tasks(project_id,title,description,status,created_at) values(?,'lease','http','workspace_ready','now')`, projectID)
	taskID, _ := task.LastInsertId()
	step, _ := conn.Exec(`insert into task_steps(task_id,agent_name,kind,status,input,created_at) values(?,'agent-a','backend','assigned','{}','now')`, taskID)
	stepID, _ := step.LastInsertId()
	_, _ = conn.Exec(`insert into task_assignments(task_id,step_id,agent_name,status,decision_id,created_at) values(?,?,'agent-a','assigned',1,'now')`, taskID, stepID)
	_, _ = conn.Exec(`insert into project_workspaces(project_id,task_id,step_id,assignment_id,agent_name,branch_name,worktree_path,base_commit,provision_commit,write_scope_json,metadata_hash,status,created_at,updated_at) values(?,?,?,1,'agent-a','agent/agent-a/http','/tmp/http-worktree','base','base','["."]','hash','ready','now','now')`, projectID, taskID, stepID)
	tokenA, tokenB := "agent-a-token", "agent-b-token"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = conn.Exec(`insert into agent_registry(name,role,dir,status) values('agent-a','worker','agents/agent-a','ready'),('agent-b','worker','agents/agent-b','ready')`)
	_, _ = conn.Exec(`insert into agent_tokens(agent_name,token_hash,scopes,created_at) values('agent-a',?,'runtime',?)`, auth.HashSecret(tokenA), now)
	_, _ = conn.Exec(`insert into agent_tokens(agent_name,token_hash,scopes,created_at) values('agent-b',?,'runtime',?)`, auth.HashSecret(tokenB), now)
	workspaceSvc := workspaces.NewService(config.Config{}, conn, nil)
	leaseSvc := leases.NewService(conn, leases.SystemClock{}, workspaceSvc)
	return NewRouter(Dependencies{DB: conn, Leases: leaseSvc}), conn, taskID, stepID, tokenA, tokenB
}

func agentRequest(handler http.Handler, token, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	handler.ServeHTTP(rec, req)
	return rec
}
