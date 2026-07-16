package pipelines

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wanxiang-agent/server/internal/testutil"
)

type fakeRunner struct {
	results []Result
	calls   int
}

func (f *fakeRunner) Run(context.Context, string, Step) Result {
	r := f.results[f.calls]
	f.calls++
	return r
}
func TestWorkerRetriesEnvironmentButNotCodeAndCreatesRollback(t *testing.T) {
	db := testutil.OpenDB(t)
	svc := NewService(db)
	d := Definition{Steps: []StepDefinition{{ID: "build", Kind: "build", Command: "go", Args: []string{"build", "./..."}, TimeoutSeconds: 2, MaxAttempts: 2, Reversible: true}}}
	r, _ := svc.Start(t.Context(), StartInput{ProjectID: 1, Definition: d, IdempotencyKey: "r", RequestedBy: "admin"})
	fake := &fakeRunner{results: []Result{{FailureClass: "environment_failure", Err: errors.New("timeout")}, {}}}
	w := NewWorker(db, fake, time.Hour, func(int64) (string, error) { return t.TempDir(), nil })
	_ = w.Scan(t.Context())
	_, _ = db.Exec(`update pipeline_steps set next_retry_at='2000-01-01' where run_id=?`, r.ID)
	_ = w.Scan(t.Context())
	got, _ := svc.Get(t.Context(), r.ID)
	if got.Status != "passed" || fake.calls != 2 {
		t.Fatalf("%+v calls=%d", got, fake.calls)
	}
}
func TestReleaseWaitsForConfirmationAndFailureCreatesRollback(t *testing.T) {
	db := testutil.OpenDB(t)
	svc := NewService(db)
	d := Definition{Steps: []StepDefinition{{ID: "build", Kind: "build", Command: "go", Args: []string{"build", "./..."}, Artifact: "app.bin", TimeoutSeconds: 2, MaxAttempts: 1, Reversible: true}, {ID: "release", Kind: "release", Command: "pm2", Args: []string{"restart", "app"}, HealthURL: "http://127.0.0.1:1/health", TimeoutSeconds: 2, MaxAttempts: 1, Reversible: true}}}
	r, _ := svc.Start(t.Context(), StartInput{ProjectID: 1, Definition: d, SafeCommit: "abc", IdempotencyKey: "rel", RequestedBy: "admin"})
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "app.bin"), []byte("old"), 0644)
	fake := &fakeRunner{results: []Result{{}, {FailureClass: "code_failure", Err: errors.New("failed")}}}
	w := NewWorker(db, fake, time.Hour, func(int64) (string, error) { return dir, nil })
	_ = w.Scan(t.Context())
	if fake.calls != 1 {
		t.Fatal("ran without confirmation")
	}
	_, _ = svc.Confirm(t.Context(), r.ID, "release", "admin")
	_ = w.Scan(t.Context())
	var count int
	_ = db.QueryRow(`select count(*) from pipeline_rollbacks where run_id=? and status='awaiting_confirmation'`, r.ID).Scan(&count)
	if count != 1 {
		t.Fatalf("rollback=%d", count)
	}
}
func TestConfirmedRollbackRestoresSafeCommitAndRestartsRelease(t *testing.T) {
	db := testutil.OpenDB(t)
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer health.Close()
	dir := t.TempDir()
	git := func(args ...string) string {
		c := exec.Command("git", args...)
		c.Dir = dir
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %v %s", args, e, b)
		}
		return strings.TrimSpace(string(b))
	}
	git("init", "-b", "main")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "test")
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("safe"), 0644)
	_ = os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("app.bin\n"), 0644)
	git("add", ".")
	git("commit", "-m", "safe")
	safe := git("rev-parse", "HEAD")
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("failed"), 0644)
	git("add", ".")
	git("commit", "-m", "failed")
	svc := NewService(db)
	d := Definition{Steps: []StepDefinition{{ID: "build", Kind: "build", Command: "go", Args: []string{"build", "./..."}, Artifact: "app.bin", TimeoutSeconds: 2, MaxAttempts: 1, Reversible: true}, {ID: "release", Kind: "release", Command: "pm2", Args: []string{"restart", "app"}, HealthURL: health.URL + "/health", TimeoutSeconds: 2, MaxAttempts: 1, Reversible: true}}}
	r, _ := svc.Start(t.Context(), StartInput{ProjectID: 1, Definition: d, SafeCommit: safe, IdempotencyKey: "rollback", RequestedBy: "admin"})
	backup := filepath.Join(t.TempDir(), "app.bin")
	_ = os.WriteFile(backup, []byte("safe-binary"), 0644)
	backupHash, _ := hashArtifact(backup)
	_ = os.WriteFile(filepath.Join(dir, "app.bin"), []byte("failed-binary"), 0644)
	_, _ = db.Exec(`update pipeline_runs set backup_path=?,backup_hash=? where id=?`, backup, backupHash, r.ID)
	_, _ = db.Exec(`update pipeline_steps set status='failed' where run_id=?`, r.ID)
	_, _ = db.Exec(`insert into pipeline_rollbacks(run_id,safe_commit,status,created_at) values(?,?,'awaiting_confirmation','now')`, r.ID, safe)
	if e := svc.ConfirmRollback(t.Context(), r.ID, "admin"); e != nil {
		t.Fatal(e)
	}
	fake := &fakeRunner{results: []Result{{}}}
	w := NewWorker(db, fake, time.Hour, func(int64) (string, error) { return dir, nil })
	w.pm2Path = func(context.Context, string, string) (string, error) { return filepath.Join(dir, "app.bin"), nil }
	if e := w.Scan(t.Context()); e != nil {
		t.Fatal(e)
	}
	gotBinary, _ := os.ReadFile(filepath.Join(dir, "app.bin"))
	var rollbackStatus string
	_ = db.QueryRow(`select status from pipeline_rollbacks where run_id=?`, r.ID).Scan(&rollbackStatus)
	if git("rev-parse", "HEAD") != safe || fake.calls != 1 || string(gotBinary) != "safe-binary" || rollbackStatus != "rolled_back" {
		t.Fatalf("head=%s calls=%d", git("rev-parse", "HEAD"), fake.calls)
	}
}
func TestBuildHashesDeclaredArtifact(t *testing.T) {
	db := testutil.OpenDB(t)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "app.bin"), []byte("artifact"), 0644)
	svc := NewService(db)
	d := Definition{Steps: []StepDefinition{{ID: "build", Kind: "build", Command: "go", Args: []string{"build", "./..."}, Artifact: "app.bin", TimeoutSeconds: 2, MaxAttempts: 1, Reversible: true}}}
	r, _ := svc.Start(t.Context(), StartInput{ProjectID: 1, Definition: d, IdempotencyKey: "artifact", RequestedBy: "admin"})
	fake := &fakeRunner{results: []Result{{}}}
	w := NewWorker(db, fake, time.Hour, func(int64) (string, error) { return dir, nil })
	_ = w.Scan(t.Context())
	got, _ := svc.Get(t.Context(), r.ID)
	if len(got.ArtifactHash) != 64 {
		t.Fatalf("hash=%q", got.ArtifactHash)
	}
}
