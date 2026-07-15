package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestNewStartsLeaseRecoveryWorkerAndCloseWaits(t *testing.T) {
	cfg, _ := config.Load(t.TempDir())
	application, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if application.LeaseRecovery == nil {
		t.Fatal("lease recovery worker not configured")
	}
	select {
	case <-application.LeaseRecovery.FirstScanDone():
	case <-time.After(time.Second):
		t.Fatal("lease recovery startup scan did not finish")
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNewStartsExecutorSupervisorAndCloseWaits(t *testing.T) {
	cfg, _ := config.Load(t.TempDir())
	application, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if application.Executor == nil {
		t.Fatal("executor supervisor not configured")
	}
	select {
	case <-application.Executor.FirstScanDone():
	case <-time.After(time.Second):
		t.Fatal("executor startup scan did not finish")
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNewStartsManagerRuntimeWhenKeyExistsAndAppCloses(t *testing.T) {
	provider := successfulProviderServer(t)
	defer provider.Close()
	cfg, _ := config.Load(t.TempDir())
	managerDir := filepath.Join(cfg.AgentDir, "manager")
	if err := os.MkdirAll(managerDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	env := "AGENT_PROVIDER_TYPE=openai\nAGENT_API_KEY=test-key\nAGENT_BASE_URL=" + provider.URL + "\nAGENT_MODEL=test-model\n"
	if err := os.WriteFile(filepath.Join(managerDir, "env"), []byte(env), 0o600); err != nil {
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
	provider := successfulProviderServer(t)
	defer provider.Close()
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
	secretReq := httptest.NewRequest(http.MethodPut, "/api/admin/agents/manager/config", bytes.NewBufferString(`{"provider_type":"openai","base_url":"`+provider.URL+`","model":"test-model","api_key":"local-secret-value"}`))
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

func TestNewStaysAvailableWhenManagerProviderRejectsConfiguration(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key"}}`))
	}))
	defer provider.Close()
	cfg, _ := config.Load(t.TempDir())
	managerDir := filepath.Join(cfg.AgentDir, "manager")
	if err := os.MkdirAll(managerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	env := "AGENT_PROVIDER_TYPE=openai\nAGENT_API_KEY=bad-key\nAGENT_BASE_URL=" + provider.URL + "\nAGENT_MODEL=test-model\n"
	if err := os.WriteFile(filepath.Join(managerDir, "env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	application, err := New(cfg)
	if err != nil {
		t.Fatalf("app must remain available for configuration repair: %v", err)
	}
	defer application.Close()
	var status string
	if err := application.DB.QueryRow(`select status from agent_registry where name='manager'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "blocked: provider_error" {
		t.Fatalf("status=%q", status)
	}
}

func successfulProviderServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"OK"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
}
