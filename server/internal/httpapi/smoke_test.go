package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wanxiang-agent/server/internal/agents"
	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/events"
	"wanxiang-agent/server/internal/tasks"
	"wanxiang-agent/server/internal/testutil"
)

func TestSmokeHealthAndManagerStatus(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("Load config: %v", err)
	}
	conn := testutil.OpenDB(t)
	bus := events.NewBus(conn)
	router := NewRouter(Dependencies{
		DB:     conn,
		Bus:    bus,
		Agents: agents.NewService(cfg, conn),
		Tasks:  tasks.NewService(cfg, conn, bus),
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("health status=%d", rec.Code)
	}

	rec = httptest.NewRecorder()
	bootstrap := httptest.NewRequest(http.MethodPost, "/api/admin/bootstrap", strings.NewReader(`{"username":"admin","password":"secret123"}`))
	router.ServeHTTP(rec, bootstrap)
	if rec.Code != http.StatusCreated {
		t.Fatalf("bootstrap status=%d body=%s", rec.Code, rec.Body.String())
	}
	var authResponse struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &authResponse); err != nil {
		t.Fatalf("decode bootstrap response: %v", err)
	}

	rec = httptest.NewRecorder()
	managerReq := httptest.NewRequest(http.MethodGet, "/api/admin/manager/status", nil)
	managerReq.Header.Set("Authorization", "Bearer "+authResponse.Token)
	router.ServeHTTP(rec, managerReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "missing_secret") {
		t.Fatalf("manager should request missing secret: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	body := strings.NewReader(`{"title":"Smoke Task","description":"Create a local project"}`)
	taskReq := httptest.NewRequest(http.MethodPost, "/api/admin/tasks", body)
	taskReq.Header.Set("Authorization", "Bearer "+authResponse.Token)
	router.ServeHTTP(rec, taskReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("task status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Task tasks.Task `json:"task"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode task response: %v", err)
	}
	gitDir := filepath.Join(cfg.ProjectDir, response.Task.ProjectSlug, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		t.Fatalf("task project Git repository not created: %v", err)
	}
}
