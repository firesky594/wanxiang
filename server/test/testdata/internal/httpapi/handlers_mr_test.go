package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"wanxiang-agent/server/internal/auth"
	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/events"
	"wanxiang-agent/server/internal/mr"
	"wanxiang-agent/server/internal/testutil"
)

func TestCompletionReportRouteRequiresAgentAndMatchingIdentity(t *testing.T) {
	cfg, _ := config.Load(t.TempDir())
	db := testutil.OpenDB(t)
	_, _ = db.Exec(`insert into agent_registry(name,role,dir,status) values('worker','worker','agents/worker','ready')`)
	token := "worker-token"
	_, _ = db.Exec(`insert into agent_tokens(agent_name,token_hash,scopes,created_at) values('worker',?,'runtime','now')`, auth.HashSecret(token))
	router := NewRouter(Dependencies{DB: db, MR: mr.NewService(cfg, db, events.NewBus(db), nil)})

	unauthorized := agentRequest(router, "", http.MethodPost, "/api/agent/completion-reports", `{}`)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized=%d %s", unauthorized.Code, unauthorized.Body.String())
	}
	forged := agentRequest(router, token, http.MethodPost, "/api/agent/completion-reports", `{"agent_name":"manager","role":"manager"}`)
	if forged.Code != http.StatusForbidden || !json.Valid(forged.Body.Bytes()) {
		t.Fatalf("forged=%d %s", forged.Code, forged.Body.String())
	}
	missing := agentRequest(router, token, http.MethodPost, "/api/agent/completion-reports", `{"agent_name":"worker","role":"worker"}`)
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("missing=%d %s", missing.Code, missing.Body.String())
	}
}

func TestNewMRRoutesExistAndLegacyCreateRouteIsRemoved(t *testing.T) {
	cfg, _ := config.Load(t.TempDir())
	db := testutil.OpenDB(t)
	_, _ = db.Exec(`insert into agent_registry(name,role,dir,status) values('lead','project_lead','agents/lead','ready')`)
	token := "lead-token"
	_, _ = db.Exec(`insert into agent_tokens(agent_name,token_hash,scopes,created_at) values('lead',?,'runtime','now')`, auth.HashSecret(token))
	router := NewRouter(Dependencies{DB: db, MR: mr.NewService(cfg, db, events.NewBus(db), nil)})
	for _, path := range []string{"/api/agent/mrs/1/reviews", "/api/agent/mrs/1/merge"} {
		res := agentRequest(router, token, http.MethodPost, path, `{}`)
		if res.Code == http.StatusNotFound {
			t.Fatalf("new route missing %s", path)
		}
	}
	legacy := agentRequest(router, token, http.MethodPost, "/api/agent/mr/create", `{}`)
	if legacy.Code != http.StatusNotFound {
		t.Fatalf("legacy=%d %s", legacy.Code, legacy.Body.String())
	}
}

func TestAdminManagerNotificationsIsReadOnly(t *testing.T) {
	cfg, _ := config.Load(t.TempDir())
	db := testutil.OpenDB(t)
	seedAdmin(t, db, "admin-mr", "secret123")
	router := NewRouter(Dependencies{DB: db, MR: mr.NewService(cfg, db, events.NewBus(db), nil)})
	login := adminRequest(router, "", http.MethodPost, "/api/admin/login", `{"username":"admin-mr","password":"secret123"}`)
	var session struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(login.Body.Bytes(), &session)
	res := adminRequest(router, session.Token, http.MethodGet, "/api/admin/manager-notifications", "")
	if res.Code != http.StatusOK || !json.Valid(res.Body.Bytes()) {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
}
