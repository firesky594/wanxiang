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

func TestAdminQueryReturnsWorkflowCollections(t *testing.T) {
	router, token, task := queryFixture(t)
	for _, path := range []string{
		"/api/admin/projects",
		"/api/admin/tasks/" + itoa(task.ID) + "/events",
		"/api/admin/tasks/" + itoa(task.ID) + "/match",
		"/api/admin/mrs?task_id=" + itoa(task.ID),
		"/api/admin/issues?task_id=" + itoa(task.ID),
	} {
		res := adminRequest(router, token, http.MethodGet, path, "")
		if res.Code != http.StatusOK {
			t.Fatalf("path=%s status=%d body=%s", path, res.Code, res.Body.String())
		}
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
	router := NewRouter(Dependencies{DB: conn, Bus: bus, Tasks: taskSvc, Issues: issueSvc, MR: mrSvc, Assignments: assignmentSvc})
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
