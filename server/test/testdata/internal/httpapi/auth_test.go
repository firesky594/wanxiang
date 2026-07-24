package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wanxiang-agent/server/internal/agents"
	"wanxiang-agent/server/internal/auth"
	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/events"
	"wanxiang-agent/server/internal/gitx"
	"wanxiang-agent/server/internal/mr"
	"wanxiang-agent/server/internal/testutil"
)

func TestAdminLoginReturnsSessionToken(t *testing.T) {
	conn := testutil.OpenDB(t)
	seedAdmin(t, conn, "admin", "secret123")
	router := NewRouter(Dependencies{DB: conn})

	req := httptest.NewRequest(http.MethodPost, "/api/admin/login", bytes.NewBufferString(`{"username":"admin","password":"secret123"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"token"`) {
		t.Fatalf("response missing token: %s", rec.Body.String())
	}
	var response struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	var tokenHash, username, expiresAt string
	if err := conn.QueryRow(`select s.token_hash,u.username,s.expires_at from admin_sessions s join users u on u.id=s.user_id`).Scan(&tokenHash, &username, &expiresAt); err != nil {
		t.Fatalf("load session: %v", err)
	}
	if tokenHash != auth.HashSecret(response.Token) || username != "admin" {
		t.Fatalf("session hash=%q username=%q", tokenHash, username)
	}
	if _, err := time.Parse(time.RFC3339Nano, expiresAt); err != nil {
		t.Fatalf("invalid expiry %q: %v", expiresAt, err)
	}
	if len(rec.Result().Cookies()) != 1 || !rec.Result().Cookies()[0].HttpOnly {
		t.Fatalf("expected HttpOnly session cookie: %v", rec.Result().Cookies())
	}
}

func TestAdminLoginRejectsWrongPassword(t *testing.T) {
	conn := testutil.OpenDB(t)
	seedAdmin(t, conn, "admin", "secret123")
	router := NewRouter(Dependencies{DB: conn})

	req := httptest.NewRequest(http.MethodPost, "/api/admin/login", bytes.NewBufferString(`{"username":"admin","password":"wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminBootstrapWorksOnceAndRemainsDisabled(t *testing.T) {
	conn := testutil.OpenDB(t)
	router := NewRouter(Dependencies{DB: conn})

	bootstrap := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/admin/bootstrap", bytes.NewBufferString(`{"username":"admin","password":"secret123"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	first := bootstrap()
	if first.Code != http.StatusCreated {
		t.Fatalf("first bootstrap status=%d body=%s", first.Code, first.Body.String())
	}
	var passwordHash string
	if err := conn.QueryRow(`select password_hash from users where username='admin'`).Scan(&passwordHash); err != nil {
		t.Fatalf("load bootstrap password hash: %v", err)
	}
	if !strings.HasPrefix(passwordHash, "pbkdf2-sha256$") {
		t.Fatalf("bootstrap password hash=%q", passwordHash)
	}
	valid, err := auth.VerifyPassword("secret123", passwordHash)
	if err != nil || !valid {
		t.Fatalf("verify bootstrap password valid=%v err=%v", valid, err)
	}
	if _, err := conn.Exec(`delete from users`); err != nil {
		t.Fatalf("delete users: %v", err)
	}
	second := bootstrap()
	if second.Code != http.StatusConflict {
		t.Fatalf("second bootstrap status=%d body=%s", second.Code, second.Body.String())
	}
}

func TestAdminLoginRehashesLegacyPassword(t *testing.T) {
	conn := testutil.OpenDB(t)
	_, err := conn.Exec(`insert into users(username,password_hash,created_at) values(?,?,datetime('now'))`, "legacy-admin", auth.HashSecret("legacy-password"))
	if err != nil {
		t.Fatalf("seed legacy admin: %v", err)
	}
	router := NewRouter(Dependencies{DB: conn})

	req := httptest.NewRequest(http.MethodPost, "/api/admin/login", bytes.NewBufferString(`{"username":"legacy-admin","password":"legacy-password"}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var upgraded string
	if err := conn.QueryRow(`select password_hash from users where username='legacy-admin'`).Scan(&upgraded); err != nil {
		t.Fatalf("load upgraded hash: %v", err)
	}
	if !strings.HasPrefix(upgraded, "pbkdf2-sha256$") {
		t.Fatalf("legacy hash was not upgraded: %q", upgraded)
	}
	valid, err := auth.VerifyPassword("legacy-password", upgraded)
	if err != nil || !valid {
		t.Fatalf("verify upgraded password valid=%v err=%v", valid, err)
	}
}

func TestProtectedAdminRouteRejectsRandomHeaderAndAcceptsPersistedSession(t *testing.T) {
	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	seedAdmin(t, conn, "admin", "secret123")
	router := NewRouter(Dependencies{DB: conn, Agents: agents.NewService(cfg, conn)})

	req := httptest.NewRequest(http.MethodGet, "/api/admin/manager/status", nil)
	req.Header.Set("Authorization", "anything")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("random header status=%d body=%s", rec.Code, rec.Body.String())
	}

	loginReq := httptest.NewRequest(http.MethodPost, "/api/admin/login", bytes.NewBufferString(`{"username":"admin","password":"secret123"}`))
	loginRec := httptest.NewRecorder()
	router.ServeHTTP(loginRec, loginReq)
	var response struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(loginRec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode login: %v", err)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/admin/manager/status", nil)
	req.Header.Set("Authorization", "Bearer "+response.Token)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("persisted session status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminAuthFallsBackToValidCookieWhenBearerIsExpired(t *testing.T) {
	conn := testutil.OpenDB(t)
	seedAdmin(t, conn, "admin", "secret123")
	router := NewRouter(Dependencies{DB: conn})

	loginReq := httptest.NewRequest(http.MethodPost, "/api/admin/login", bytes.NewBufferString(`{"username":"admin","password":"secret123"}`))
	loginRec := httptest.NewRecorder()
	router.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK || len(loginRec.Result().Cookies()) != 1 {
		t.Fatalf("login status=%d cookies=%v", loginRec.Code, loginRec.Result().Cookies())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/events/stream", nil)
	req.Header.Set("Authorization", "Bearer expired-local-token")
	req.AddCookie(loginRec.Result().Cookies()[0])
	handler := RequireAdmin(conn)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminAuthClearsInvalidSessionCookie(t *testing.T) {
	conn := testutil.OpenDB(t)
	handler := RequireAdmin(conn)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/admin/tasks", nil)
	req.AddCookie(&http.Cookie{Name: adminSessionCookie, Value: "expired"})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != adminSessionCookie || cookies[0].MaxAge >= 0 {
		t.Fatalf("expected cleared admin cookie, got %v", cookies)
	}
}

func TestAdminAgentConfigSavesAndNeverReturnsAPIKey(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"OK"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer provider.Close()

	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	seedAdmin(t, conn, "admin", "secret123")
	svc := agents.NewService(cfg, conn)
	router := NewRouter(Dependencies{DB: conn, Agents: svc})

	loginReq := httptest.NewRequest(http.MethodPost, "/api/admin/login", bytes.NewBufferString(`{"username":"admin","password":"secret123"}`))
	loginRec := httptest.NewRecorder()
	router.ServeHTTP(loginRec, loginReq)
	var login struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(loginRec.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}

	body := fmt.Sprintf(`{"provider_type":"deepseek","base_url":%q,"model":"deepseek-test","api_key":"never-return-this"}`, provider.URL)
	putReq := httptest.NewRequest(http.MethodPut, "/api/admin/agents/worker-1/config", bytes.NewBufferString(body))
	putReq.Header.Set("Authorization", "Bearer "+login.Token)
	putRec := httptest.NewRecorder()
	router.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK || strings.Contains(putRec.Body.String(), "never-return-this") {
		t.Fatalf("put status=%d body=%s", putRec.Code, putRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/admin/agents", nil)
	listReq.Header.Set("Authorization", "Bearer "+login.Token)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK || strings.Contains(listRec.Body.String(), "never-return-this") || !strings.Contains(listRec.Body.String(), `"secret_configured":true`) {
		t.Fatalf("list status=%d body=%s", listRec.Code, listRec.Body.String())
	}
}

func TestAgentRouteUsesAuthenticatedAgentIdentity(t *testing.T) {
	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	seedAgentToken(t, conn, "worker-1", "agent-secret")
	if _, err := conn.Exec(`update agent_registry set dir=? where name='worker-1'`, filepath.Join(cfg.AgentDir, "worker-1")); err != nil {
		t.Fatal(err)
	}
	svc := agents.NewService(cfg, conn)
	router := NewRouter(Dependencies{DB: conn, Agents: svc})

	req := httptest.NewRequest(http.MethodPost, "/api/agent/heartbeat", bytes.NewBufferString(`{"name":"../../escape","role":"worker","status":"online"}`))
	req.Header.Set("Authorization", "Bearer agent-secret")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("heartbeat status=%d body=%s", rec.Code, rec.Body.String())
	}

	var name, dir string
	if err := conn.QueryRow(`select name,dir from agent_registry where name='worker-1'`).Scan(&name, &dir); err != nil {
		t.Fatalf("load authenticated heartbeat: %v", err)
	}
	if name != "worker-1" || dir != filepath.Join(cfg.AgentDir, "worker-1") {
		t.Fatalf("name=%q dir=%q", name, dir)
	}
}

func TestAgentRouteRejectsUnknownToken(t *testing.T) {
	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	router := NewRouter(Dependencies{DB: conn, Agents: agents.NewService(cfg, conn)})

	req := httptest.NewRequest(http.MethodPost, "/api/agent/heartbeat", bytes.NewBufferString(`{"name":"worker-1","role":"worker","status":"online"}`))
	req.Header.Set("Authorization", "Bearer random")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown token status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMergeRouteUsesAuthenticatedManagerIdentity(t *testing.T) {
	conn := testutil.OpenDB(t)
	cfg, _ := config.Load(t.TempDir())
	seedAgentToken(t, conn, "manager", "manager-token")
	router := NewRouter(Dependencies{DB: conn, MR: mr.NewService(cfg, conn, events.NewBus(conn), nil)})
	legacy := agentRequest(router, "manager-token", http.MethodPost, "/api/agent/mr/1/merge", `{}`)
	if legacy.Code != http.StatusNotFound {
		t.Fatalf("legacy status=%d body=%s", legacy.Code, legacy.Body.String())
	}
}

func TestMissingManagerKeyCannotBeBypassedByAuthenticatedHeartbeat(t *testing.T) {
	cfg, _ := config.Load(t.TempDir())
	conn := testutil.OpenDB(t)
	seedAgentToken(t, conn, "manager", "manager-token")
	router := NewRouter(Dependencies{DB: conn, MR: mr.NewService(cfg, conn, events.NewBus(conn), nil)})
	merge := agentRequest(router, "manager-token", http.MethodPost, "/api/agent/mrs/1/merge", `{"agent_name":"manager","role":"manager"}`)
	if merge.Code != http.StatusConflict {
		t.Fatalf("merge status=%d body=%s", merge.Code, merge.Body.String())
	}
}

func TestAgentPrincipalUsesRegistryRoleAndIgnoresForgedHeaders(t *testing.T) {
	conn := testutil.OpenDB(t)
	if _, err := conn.Exec(`insert into agent_registry(name,role,dir,status) values('lead','project_lead','agents/lead','ready')`); err != nil {
		t.Fatal(err)
	}
	token := "principal-token"
	if _, err := conn.Exec(`insert into agent_tokens(agent_name,token_hash,scopes,created_at) values('lead',?,'agent','now')`, auth.HashSecret(token)); err != nil {
		t.Fatal(err)
	}
	var got AgentPrincipalValue
	handler := RequireAgent(conn)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ok bool
		got, ok = AgentPrincipal(r.Context())
		if !ok {
			t.Fatal("principal missing")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Agent-Name", "manager")
	req.Header.Set("X-Agent-Role", "manager")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	if got.Name != "lead" || got.Role != "project_lead" {
		t.Fatalf("principal=%+v", got)
	}
}

func seedAdmin(t *testing.T, conn *sql.DB, username string, password string) {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	_, err = conn.Exec(`insert into users(username,password_hash,created_at) values(?,?,datetime('now'))`, username, hash)
	if err != nil {
		t.Fatalf("seedAdmin: %v", err)
	}
}

func seedAgentToken(t *testing.T, conn *sql.DB, agentName, token string) {
	t.Helper()
	role := "worker"
	if agentName == "manager" {
		role = "manager"
	}
	if _, err := conn.Exec(`insert into agent_registry(name,role,dir,status) values(?,?,?,'ready') on conflict(name) do nothing`, agentName, role, "agents/"+agentName); err != nil {
		t.Fatalf("seedAgentRegistry: %v", err)
	}
	_, err := conn.Exec(`insert into agent_tokens(agent_name,token_hash,scopes,expires_at,created_at) values(?,?,?,?,?)`,
		agentName, auth.HashSecret(token), "runtime", time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("seedAgentToken: %v", err)
	}
}

func mustHTTPTestGit(t *testing.T, repoDir string, args ...string) {
	t.Helper()
	out, err := gitx.Run(context.Background(), repoDir, args...)
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}
