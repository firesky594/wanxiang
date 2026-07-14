package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/httpapi"
)

func TestNewKeepsMissingKeyManagerBlockedWithoutRuntime(t *testing.T) {
	cfg, _ := config.Load(t.TempDir())
	application, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer application.Close()

	var status string
	if err := application.DB.QueryRow(`select status from agent_registry where name='manager'`).Scan(&status); err != nil {
		t.Fatalf("load manager: %v", err)
	}
	if status != "blocked: missing_secret" {
		t.Fatalf("status=%q", status)
	}
	var count int
	if err := application.DB.QueryRow(`select count(*) from runtime_events where event_type='manager.started'`).Scan(&count); err != nil {
		t.Fatalf("count manager events: %v", err)
	}
	if count != 0 {
		t.Fatalf("manager started without key")
	}
}

func TestNewStartsManagerRuntimeWhenKeyExistsAndAppCloses(t *testing.T) {
	cfg, _ := config.Load(t.TempDir())
	managerDir := filepath.Join(cfg.AgentDir, "manager")
	if err := os.MkdirAll(managerDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(managerDir, "env"), []byte("MANAGER_API_KEY=test-key\n"), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}

	application, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var count int
	if err := application.DB.QueryRow(`select count(*) from runtime_events where event_type='manager.started' and actor='manager'`).Scan(&count); err != nil {
		t.Fatalf("count manager events: %v", err)
	}
	if count != 1 {
		t.Fatalf("manager.started count=%d", count)
	}

	closer, ok := any(application).(interface{ Close() error })
	if !ok {
		t.Fatalf("App does not provide Close")
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestSavingManagerKeyStartsRuntimeWithoutRestart(t *testing.T) {
	cfg, _ := config.Load(t.TempDir())
	application, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer application.Close()
	router := httpapi.NewRouter(application.HTTP)

	bootstrapRec := httptest.NewRecorder()
	bootstrapReq := httptest.NewRequest(http.MethodPost, "/api/admin/bootstrap", bytes.NewBufferString(`{"username":"admin","password":"secret123"}`))
	router.ServeHTTP(bootstrapRec, bootstrapReq)
	if bootstrapRec.Code != http.StatusCreated {
		t.Fatalf("bootstrap status=%d body=%s", bootstrapRec.Code, bootstrapRec.Body.String())
	}
	var authResponse struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(bootstrapRec.Body.Bytes(), &authResponse); err != nil {
		t.Fatalf("decode bootstrap: %v", err)
	}

	secretRec := httptest.NewRecorder()
	secretReq := httptest.NewRequest(http.MethodPost, "/api/admin/manager/secrets", bytes.NewBufferString(`{"key":"MANAGER_API_KEY","value":"local-secret-value"}`))
	secretReq.Header.Set("Authorization", "Bearer "+authResponse.Token)
	router.ServeHTTP(secretRec, secretReq)
	if secretRec.Code != http.StatusOK {
		t.Fatalf("save secret status=%d body=%s", secretRec.Code, secretRec.Body.String())
	}

	var payload string
	if err := application.DB.QueryRow(`select payload_json from runtime_events where event_type='manager.started'`).Scan(&payload); err != nil {
		t.Fatalf("load manager.started: %v", err)
	}
	if bytes.Contains([]byte(payload), []byte("local-secret-value")) {
		t.Fatalf("manager event leaked API key: %s", payload)
	}
}
