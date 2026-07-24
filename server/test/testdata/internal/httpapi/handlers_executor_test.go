package httpapi

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"wanxiang-agent/server/internal/auth"
	"wanxiang-agent/server/internal/executor"
	"wanxiang-agent/server/internal/testutil"
)

func TestAdminCanReadRedactedExecutorTimeline(t *testing.T) {
	db := testutil.OpenDB(t)
	adminToken, agentToken := executorAuthFixture(t, db)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, _ := db.Exec(`insert into executor_runs(task_id,step_id,agent_name,lease_id,lease_version,status,error_summary,created_at,updated_at) values(1,2,'agent-a','secret-lease',1,'failed','API_KEY=hidden','now','now')`)
	runID, _ := result.LastInsertId()
	_, _ = db.Exec(`insert into executor_actions(run_id,sequence,action_type,relative_path,status,result_summary,result_hash,created_at) values(?,1,'read_file','src/main.go','passed','ok','hash',?)`, runID, now)
	router := NewRouter(Dependencies{DB: db, Executor: executor.NewAdminService(db, nil)})
	req := httptest.NewRequest(http.MethodGet, "/api/admin/executor/runs/"+itoa(runID), nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, secret := range []string{"secret-lease", "hidden", "API_KEY"} {
		if strings.Contains(body, secret) {
			t.Fatalf("response leaked %q: %s", secret, body)
		}
	}
	if !strings.Contains(body, "src/main.go") {
		t.Fatalf("body=%s", body)
	}
	for _, token := range []string{"", agentToken} {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/executor/runs/"+itoa(runID), nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("token=%q status=%d", token, rec.Code)
		}
	}
}

func TestAgentAndAnonymousCannotStartOrStopExecutor(t *testing.T) {
	db := testutil.OpenDB(t)
	adminToken, agentToken := executorAuthFixture(t, db)
	router := NewRouter(Dependencies{DB: db, Executor: executor.NewAdminService(db, nil)})
	for _, path := range []string{"/api/admin/executor/scan", "/api/admin/executor/runs/1/stop"} {
		for _, token := range []string{"", agentToken} {
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(`{}`))
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("path=%s token=%q status=%d", path, token, rec.Code)
			}
		}
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(`{}`))
		req.Header.Set("Authorization", "Bearer "+adminToken)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code == http.StatusUnauthorized {
			t.Fatalf("admin unauthorized path=%s", path)
		}
	}
}

func executorAuthFixture(t *testing.T, db DBExec) (string, string) {
	t.Helper()
	adminToken := "admin-token"
	agentToken := "agent-token"
	result, err := db.Exec(`insert into users(username,password_hash,created_at) values('admin','hash','now')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := result.LastInsertId()
	expires := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	_, _ = db.Exec(`insert into admin_sessions(user_id,token_hash,expires_at,created_at) values(?,?,?,'now')`, userID, auth.HashSecret(adminToken), expires)
	_, _ = db.Exec(`insert into agent_tokens(agent_name,token_hash,scopes,expires_at,created_at) values('agent-a',?,'runtime',?,'now')`, auth.HashSecret(agentToken), expires)
	return adminToken, agentToken
}

type DBExec interface {
	Exec(string, ...any) (sql.Result, error)
}
