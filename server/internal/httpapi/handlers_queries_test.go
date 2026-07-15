package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"wanxiang-agent/server/internal/assignments"
	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/events"
	"wanxiang-agent/server/internal/issues"
	"wanxiang-agent/server/internal/mr"
	"wanxiang-agent/server/internal/tasks"
	"wanxiang-agent/server/internal/testutil"
	"wanxiang-agent/server/internal/workspaces"
)

func TestAdminQueryTasksAndDetail(t *testing.T) {
	router, token, task := queryFixture(t)

	res := adminRequest(router, token, http.MethodGet, "/api/admin/tasks?limit=10&offset=0", "")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), task.Title) {
		t.Fatalf("list status=%d body=%s", res.Code, res.Body.String())
	}
	res = adminRequest(router, token, http.MethodGet, "/api/admin/tasks/"+itoa(task.ID), "")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), task.ProjectSlug) {
		t.Fatalf("detail status=%d body=%s", res.Code, res.Body.String())
	}
	res = adminRequest(router, token, http.MethodGet, "/api/admin/tasks/999999", "")
	if res.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestAdminQueryRejectsBadPaginationAndTransition(t *testing.T) {
	router, token, task := queryFixture(t)
	res := adminRequest(router, token, http.MethodGet, "/api/admin/tasks?limit=101", "")
	if res.Code != http.StatusBadRequest {
		t.Fatalf("pagination status=%d body=%s", res.Code, res.Body.String())
	}
	res = adminRequest(router, token, http.MethodPatch, "/api/admin/tasks/"+itoa(task.ID)+"/status", `{"status":"completed"}`)
	if res.Code != http.StatusConflict {
		t.Fatalf("transition status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestAdminCreateTaskCanReuseRegisteredProject(t *testing.T) {
	router, token, existing := queryFixture(t)
	res := adminRequest(router, token, http.MethodPost, "/api/admin/tasks", `{"title":"reuse project","project_id":`+itoa(existing.ProjectID)+`}`)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"project_id":`+itoa(existing.ProjectID)) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestAdminWorkspaceRepairRequiresExplicitDirection(t *testing.T) {
	router, token, task := queryFixture(t)
	res := adminRequest(router, token, http.MethodPost, "/api/admin/tasks/"+itoa(task.ID)+"/workspace/repair", `{"direction":"automatic"}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestAdminQueryReturnsWorkflowCollections(t *testing.T) {
	router, token, task := queryFixture(t)
	for _, path := range []string{
		"/api/admin/projects",
		"/api/admin/tasks/" + itoa(task.ID) + "/events",
		"/api/admin/tasks/" + itoa(task.ID) + "/match",
		"/api/admin/tasks/" + itoa(task.ID) + "/workspace",
		"/api/admin/mrs?task_id=" + itoa(task.ID),
		"/api/admin/issues?task_id=" + itoa(task.ID),
	} {
		res := adminRequest(router, token, http.MethodGet, path, "")
		if res.Code != http.StatusOK {
			t.Fatalf("path=%s status=%d body=%s", path, res.Code, res.Body.String())
		}
	}
}

func TestAdminMRListAndDetailReturnReportAndReviews(t *testing.T) {
	cfg, _ := config.Load(t.TempDir())
	db := testutil.OpenDB(t)
	bus := events.NewBus(db)
	svc := mr.NewService(cfg, db, bus, nil)
	project, _ := db.Exec(`insert into projects(slug,dir,status,remote_url,created_at) values('mr-query','/tmp/mr-query','created','','now')`)
	projectID, _ := project.LastInsertId()
	taskRow, _ := db.Exec(`insert into tasks(project_id,title,description,status,created_at) values(?,'mr','mr','workspace_ready','now')`, projectID)
	taskID, _ := taskRow.LastInsertId()
	report, err := db.Exec(`insert into completion_reports(project_id,task_id,step_id,lease_id,lease_version,agent_name,agent_role,version,source_branch,checkpoint_commit,head_commit,completed_json,incomplete_json,key_files_json,tests_json,risks_json,dependencies_json,merge_order_json,user_decision,created_at) values(?,?,1,'lease',1,'worker','worker',1,'agent/worker/mr','abc','abc','["完成 API"]','[]','["server/api.go"]','[{"command":"go test ./...","status":"passed"}]','["需要发布"]','[]','[]','','now')`, projectID, taskID)
	if err != nil {
		t.Fatal(err)
	}
	reportID, _ := report.LastInsertId()
	mrRow, err := db.Exec(`insert into merge_requests(project_id,task_id,step_id,report_id,lease_id,report_version,title,source_branch,target_branch,source_commit,project_lead,status,created_by,created_at) values(?,?,1,?,'lease',1,'完成 API','agent/worker/mr','main','abc','lead','approved','worker','now')`, projectID, taskID, reportID)
	if err != nil {
		t.Fatal(err)
	}
	mrID, _ := mrRow.LastInsertId()
	_, _ = db.Exec(`insert into mr_reviews(mr_id,reviewer,role,status,body,created_at) values(?,'lead','project_lead','approved','检查通过','now')`, mrID)
	adminRouter := NewRouter(Dependencies{DB: db, MR: svc})
	seedAdmin(t, db, "admin2", "secret123")
	login := adminRequest(adminRouter, "", http.MethodPost, "/api/admin/login", `{"username":"admin2","password":"secret123"}`)
	var session struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(login.Body.Bytes(), &session)
	list := adminRequest(adminRouter, session.Token, http.MethodGet, "/api/admin/mrs", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "完成 API") || !strings.Contains(list.Body.String(), "完成 API") {
		t.Fatalf("list=%d %s", list.Code, list.Body.String())
	}
	detail := adminRequest(adminRouter, session.Token, http.MethodGet, "/api/admin/mrs/"+itoa(mrID), "")
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), "检查通过") || !strings.Contains(detail.Body.String(), "go test ./...") {
		t.Fatalf("detail=%d %s", detail.Code, detail.Body.String())
	}
}

func queryFixture(t *testing.T) (http.Handler, string, tasks.Task) {
	t.Helper()
	cfg, _ := config.Load(t.TempDir())
	conn := testutil.OpenDB(t)
	bus := events.NewBus(conn)
	taskSvc := tasks.NewService(cfg, conn, bus)
	issueSvc := issues.NewService(conn, bus)
	mrSvc := mr.NewService(cfg, conn, bus, nil, issueSvc)
	assignmentSvc := assignments.NewService(cfg, conn)
	workspaceSvc := workspaces.NewService(cfg, conn, bus)
	router := NewRouter(Dependencies{DB: conn, Bus: bus, Tasks: taskSvc, Issues: issueSvc, MR: mrSvc, Assignments: assignmentSvc, Workspaces: workspaceSvc})
	res := adminRequest(router, "", http.MethodPost, "/api/admin/bootstrap", `{"username":"admin","password":"secret123"}`)
	var auth struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &auth); err != nil || auth.Token == "" {
		t.Fatalf("bootstrap status=%d body=%s err=%v", res.Code, res.Body.String(), err)
	}
	task, err := taskSvc.CreateTask(t.Context(), "persisted task", "description")
	if err != nil {
		t.Fatal(err)
	}
	return router, auth.Token, task
}

func adminRequest(handler http.Handler, token, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	handler.ServeHTTP(rec, req)
	return rec
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}
