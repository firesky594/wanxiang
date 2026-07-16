package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"wanxiang-agent/server/internal/pipelines"
	"wanxiang-agent/server/internal/testutil"
)

func TestPipelineAdminAPIAuthStartAndConfirmation(t *testing.T) {
	db := testutil.OpenDB(t)
	dir := t.TempDir()
	git := func(a ...string) string {
		c := exec.Command("git", a...)
		c.Dir = dir
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git: %v %s", e, b)
		}
		return strings.TrimSpace(string(b))
	}
	git("init", "-b", "main")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "test")
	_ = os.MkdirAll(filepath.Join(dir, ".wanxiang"), 0755)
	_ = os.WriteFile(filepath.Join(dir, ".wanxiang", "pipeline.json"), []byte(`{"steps":[{"id":"build","kind":"build","command":"go","args":["build","./..."],"artifact":"app.bin","timeout_seconds":5,"max_attempts":1,"reversible":true},{"id":"release","kind":"release","command":"pm2","args":["restart","demo"],"health_url":"http://127.0.0.1:30188/api/health","timeout_seconds":5,"max_attempts":1,"reversible":true}]}`), 0644)
	git("add", ".")
	git("commit", "-m", "init")
	head := git("rev-parse", "HEAD")
	p, _ := db.Exec(`insert into projects(slug,dir,status,main_commit,remote_url,created_at) values('p',?,'active',?,'','now')`, dir, head)
	pid, _ := p.LastInsertId()
	svc := pipelines.NewService(db)
	router := NewRouter(Dependencies{DB: db, Pipelines: svc})
	path := "/api/admin/projects/" + itoa(pid) + "/pipelines"
	if r := adminRequest(router, "", http.MethodPost, path, `{"idempotency_key":"x"}`); r.Code != 401 {
		t.Fatalf("anon=%d", r.Code)
	}
	seedAdmin(t, db, "admin", "secret123")
	login := adminRequest(router, "", http.MethodPost, "/api/admin/login", `{"username":"admin","password":"secret123"}`)
	var auth struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(login.Body.Bytes(), &auth)
	start := adminRequest(router, auth.Token, http.MethodPost, path, `{"idempotency_key":"x"}`)
	if start.Code != 201 || !strings.Contains(start.Body.String(), "awaiting_confirmation") {
		t.Fatalf("start=%d %s", start.Code, start.Body.String())
	}
}
