package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wanxiang-agent/server/internal/agents"
	"wanxiang-agent/server/internal/auth"
	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/events"
	"wanxiang-agent/server/internal/gitx"
	"wanxiang-agent/server/internal/issues"
	"wanxiang-agent/server/internal/mr"
	"wanxiang-agent/server/internal/tasks"
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

func TestAgentRouteUsesAuthenticatedAgentIdentity(t *testing.T) {
	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	seedAgentToken(t, conn, "worker-1", "agent-secret")
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
	ctx := context.Background()
	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	bus := events.NewBus(conn)
	taskSvc := tasks.NewService(cfg, conn, bus)
	task, err := taskSvc.CreateTask(ctx, "HTTP merge", "Verify manager identity")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	projectDir := filepath.Join(cfg.ProjectDir, task.ProjectSlug)
	mustHTTPTestGit(t, projectDir, "checkout", "-b", "agent/backend/http-merge")
	if err := os.WriteFile(filepath.Join(projectDir, "http-merge.txt"), []byte("merged\n"), 0o644); err != nil {
		t.Fatalf("write branch file: %v", err)
	}
	mustHTTPTestGit(t, projectDir, "add", "http-merge.txt")
	mustHTTPTestGit(t, projectDir, "commit", "-m", "test: add http merge file")

	agentSvc := agents.NewService(cfg, conn)
	if _, err := agentSvc.EnsureManager(ctx); err != nil {
		t.Fatalf("EnsureManager: %v", err)
	}
	if err := agentSvc.SaveManagerSecret(ctx, "MANAGER_API_KEY", "test-key"); err != nil {
		t.Fatalf("SaveManagerSecret: %v", err)
	}
	if _, err := agentSvc.EnsureManager(ctx); err != nil {
		t.Fatalf("EnsureManager ready: %v", err)
	}
	seedAgentToken(t, conn, "backend-dev", "backend-token")
	seedAgentToken(t, conn, "manager", "manager-token")
	mrSvc := mr.NewService(cfg, conn, bus, agentSvc, issues.NewService(conn))
	created, err := mrSvc.Create(ctx, task.ProjectID, task.ID, "HTTP merge", "agent/backend/http-merge", "backend-dev")
	if err != nil {
		t.Fatalf("Create MR: %v", err)
	}
	router := NewRouter(Dependencies{DB: conn, MR: mrSvc})

	url := fmt.Sprintf("/api/agent/mr/%d/merge", created.ID)
	backendReq := httptest.NewRequest(http.MethodPost, url, bytes.NewBufferString(`{"actor":"manager"}`))
	backendReq.Header.Set("Authorization", "Bearer backend-token")
	backendRec := httptest.NewRecorder()
	router.ServeHTTP(backendRec, backendReq)
	if backendRec.Code != http.StatusForbidden {
		t.Fatalf("backend impersonation status=%d body=%s", backendRec.Code, backendRec.Body.String())
	}

	managerReq := httptest.NewRequest(http.MethodPost, url, bytes.NewBufferString(`{"actor":"backend-dev"}`))
	managerReq.Header.Set("Authorization", "Bearer manager-token")
	managerRec := httptest.NewRecorder()
	router.ServeHTTP(managerRec, managerReq)
	if managerRec.Code != http.StatusOK {
		t.Fatalf("manager status=%d body=%s", managerRec.Code, managerRec.Body.String())
	}
	content, err := gitx.Run(ctx, projectDir, "show", "main:http-merge.txt")
	if err != nil || content != "merged\n" {
		t.Fatalf("main content=%q err=%v", content, err)
	}
}

func TestMissingManagerKeyCannotBeBypassedByAuthenticatedHeartbeat(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	bus := events.NewBus(conn)
	taskSvc := tasks.NewService(cfg, conn, bus)
	task, err := taskSvc.CreateTask(ctx, "Blocked manager merge", "Manager key must be present")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	projectDir := filepath.Join(cfg.ProjectDir, task.ProjectSlug)
	mustHTTPTestGit(t, projectDir, "checkout", "-b", "agent/manager/missing-key")
	if err := os.WriteFile(filepath.Join(projectDir, "missing-key.txt"), []byte("must not merge\n"), 0o644); err != nil {
		t.Fatalf("write branch file: %v", err)
	}
	mustHTTPTestGit(t, projectDir, "add", "missing-key.txt")
	mustHTTPTestGit(t, projectDir, "commit", "-m", "test: add missing-key branch")

	agentSvc := agents.NewService(cfg, conn, bus)
	status, err := agentSvc.EnsureManager(ctx)
	if err != nil {
		t.Fatalf("EnsureManager: %v", err)
	}
	if status.Status != "blocked: missing_secret" {
		t.Fatalf("initial manager status=%q", status.Status)
	}
	seedAgentToken(t, conn, "manager", "manager-token")
	mrSvc := mr.NewService(cfg, conn, bus, agentSvc, issues.NewService(conn, bus))
	created, err := mrSvc.Create(ctx, task.ProjectID, task.ID, "Must stay open", "agent/manager/missing-key", "manager")
	if err != nil {
		t.Fatalf("Create MR: %v", err)
	}
	router := NewRouter(Dependencies{DB: conn, Agents: agentSvc, MR: mrSvc})

	heartbeatReq := httptest.NewRequest(http.MethodPost, "/api/agent/heartbeat", bytes.NewBufferString(`{"name":"manager","role":"manager","status":"online"}`))
	heartbeatReq.Header.Set("Authorization", "Bearer manager-token")
	heartbeatRec := httptest.NewRecorder()
	router.ServeHTTP(heartbeatRec, heartbeatReq)
	if heartbeatRec.Code != http.StatusBadRequest {
		t.Errorf("heartbeat status=%d body=%s", heartbeatRec.Code, heartbeatRec.Body.String())
	}

	mergeReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/agent/mr/%d/merge", created.ID), nil)
	mergeReq.Header.Set("Authorization", "Bearer manager-token")
	mergeRec := httptest.NewRecorder()
	router.ServeHTTP(mergeRec, mergeReq)
	if mergeRec.Code != http.StatusForbidden {
		t.Errorf("merge status=%d body=%s", mergeRec.Code, mergeRec.Body.String())
	}
	var registryStatus, mrStatus string
	if err := conn.QueryRow(`select status from agent_registry where name='manager'`).Scan(&registryStatus); err != nil {
		t.Fatalf("load manager status: %v", err)
	}
	if err := conn.QueryRow(`select status from merge_requests where id=?`, created.ID).Scan(&mrStatus); err != nil {
		t.Fatalf("load MR status: %v", err)
	}
	if registryStatus != "blocked: missing_secret" || mrStatus != "open" {
		t.Errorf("registry status=%q MR status=%q", registryStatus, mrStatus)
	}
	if _, err := gitx.Run(ctx, projectDir, "show", "main:missing-key.txt"); err == nil {
		t.Errorf("missing-key branch was merged into main")
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
