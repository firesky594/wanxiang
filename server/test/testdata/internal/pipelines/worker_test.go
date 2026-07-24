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

	"wanxiang-agent/server/internal/gitx"
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

type signalingRunner struct {
	called chan struct{}
	result Result
}

func (r signalingRunner) Run(context.Context, string, Step) Result {
	select {
	case r.called <- struct{}{}:
	default:
	}
	return r.result
}

func TestWorkerRetriesEnvironmentButNotCodeAndCreatesRollback(t *testing.T) {
	db := testutil.OpenDB(t)
	svc := NewService(db)
	d := Definition{Steps: []StepDefinition{{ID: "build", Kind: "build", Command: "go", Args: []string{"build", "./..."}, TimeoutSeconds: 2, MaxAttempts: 2, Reversible: true}}}
	r, _ := svc.Start(t.Context(), StartInput{ProjectID: 1, Definition: d, IdempotencyKey: "r", RequestedBy: "admin"})
	fake := &fakeRunner{results: []Result{{FailureClass: "environment_failure", Err: errors.New("timeout")}, {}}}
	dir := t.TempDir()
	w := NewWorker(db, fake, time.Hour, func(int64) (string, error) { return dir, nil }, t.TempDir())
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
	w := NewWorker(db, fake, time.Hour, func(int64) (string, error) { return dir, nil }, t.TempDir())
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
	failed := git("rev-parse", "HEAD")
	if _, err := db.Exec(`insert into projects(id,slug,dir,status,main_commit,remote_url,created_at)
		values(1,'rollback-project',?,'active',?,'','now')`, dir, failed); err != nil {
		t.Fatal(err)
	}
	svc := NewService(db)
	d := Definition{Steps: []StepDefinition{{ID: "build", Kind: "build", Command: "go", Args: []string{"build", "./..."}, Artifact: "app.bin", TimeoutSeconds: 2, MaxAttempts: 1, Reversible: true}, {ID: "release", Kind: "release", Command: "pm2", Args: []string{"restart", "app"}, HealthURL: health.URL + "/health", TimeoutSeconds: 2, MaxAttempts: 1, Reversible: true}}}
	r, _ := svc.Start(t.Context(), StartInput{ProjectID: 1, Definition: d, SafeCommit: safe, IdempotencyKey: "rollback", RequestedBy: "admin"})
	backup := filepath.Join(t.TempDir(), "app.bin")
	_ = os.WriteFile(backup, []byte("safe-binary"), 0644)
	backupHash, _ := hashArtifact(backup)
	_ = os.WriteFile(filepath.Join(dir, "app.bin"), []byte("failed-binary"), 0644)
	_, _ = db.Exec(`update pipeline_runs set backup_path=?,backup_hash=? where id=?`, backup, backupHash, r.ID)
	_, _ = db.Exec(`update pipeline_steps set status='failed' where run_id=?`, r.ID)
	_, _ = db.Exec(`insert into pipeline_rollbacks(run_id,safe_commit,expected_head,status,created_at)
		values(?,?,?,'awaiting_confirmation','now')`, r.ID, safe, failed)
	if e := svc.ConfirmRollback(t.Context(), r.ID, "admin"); e != nil {
		t.Fatal(e)
	}
	fake := &fakeRunner{results: []Result{{}}}
	w := NewWorker(db, fake, time.Hour, func(int64) (string, error) { return dir, nil }, t.TempDir())
	w.pm2Path = func(context.Context, string, string) (string, error) { return filepath.Join(dir, "app.bin"), nil }
	if e := w.Scan(t.Context()); e != nil {
		t.Fatal(e)
	}
	gotBinary, _ := os.ReadFile(filepath.Join(dir, "app.bin"))
	var rollbackStatus, mainCommit string
	_ = db.QueryRow(`select status from pipeline_rollbacks where run_id=?`, r.ID).Scan(&rollbackStatus)
	_ = db.QueryRow(`select main_commit from projects where id=1`).Scan(&mainCommit)
	if git("rev-parse", "HEAD") != safe || mainCommit != safe || fake.calls != 1 || string(gotBinary) != "safe-binary" || rollbackStatus != "rolled_back" {
		t.Fatalf("head=%s main_commit=%s calls=%d status=%s", git("rev-parse", "HEAD"), mainCommit, fake.calls, rollbackStatus)
	}
}

func TestRollbackReloadsClaimAfterWaitingForProjectLock(t *testing.T) {
	db := testutil.OpenDB(t)
	dir := t.TempDir()
	git := func(args ...string) string {
		command := exec.Command("git", args...)
		command.Dir = dir
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "-b", "main")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "test")
	git("commit", "--allow-empty", "-m", "safe")
	safe := git("rev-parse", "HEAD")
	git("commit", "--allow-empty", "-m", "later")
	later := git("rev-parse", "HEAD")
	if _, err := db.Exec(`insert into projects(id,slug,dir,status,main_commit,remote_url,created_at)
		values(1,'locked-rollback',?,'active',?,'','now')`, dir, later); err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	definition := Definition{Steps: []StepDefinition{{
		ID: "build", Kind: "build", Command: "go", Args: []string{"build", "./..."},
		Artifact: "app.bin", TimeoutSeconds: 2, MaxAttempts: 1, Reversible: true,
	}}}
	run, err := service.Start(t.Context(), StartInput{
		ProjectID: 1, Definition: definition, SafeCommit: safe,
		IdempotencyKey: "locked-rollback", RequestedBy: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := db.Exec(`insert into pipeline_rollbacks(run_id,safe_commit,expected_head,status,created_at)
		values(?,?,?,'pending','now')`, run.ID, safe, later)
	if err != nil {
		t.Fatal(err)
	}
	rollbackID, _ := result.LastInsertId()
	dataDir := t.TempDir()
	release, err := gitx.AcquireProjectLock(t.Context(), dataDir, 1)
	if err != nil {
		t.Fatal(err)
	}
	dirRead := make(chan struct{}, 1)
	worker := NewWorker(db, &fakeRunner{}, time.Hour, func(projectID int64) (string, error) {
		var projectDir string
		err := db.QueryRow(`select dir from projects where id=?`, projectID).Scan(&projectDir)
		select {
		case dirRead <- struct{}{}:
		default:
		}
		return projectDir, err
	}, dataDir)
	done := make(chan error, 1)
	go func() {
		done <- worker.scanRollbacks(t.Context())
	}()
	select {
	case <-dirRead:
	case <-time.After(2 * time.Second):
		release()
		t.Fatal("rollback did not reach project lock")
	}
	if _, err = db.Exec(`update pipeline_rollbacks set safe_commit=? where id=? and status='running'`, later, rollbackID); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rollback did not finish after lock release")
	}
	var status, lastError, mainCommit string
	if err = db.QueryRow(`select status,last_error from pipeline_rollbacks where id=?`, rollbackID).Scan(&status, &lastError); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`select main_commit from projects where id=1`).Scan(&mainCommit); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || !strings.Contains(lastError, "changed while waiting") ||
		git("rev-parse", "HEAD") != later || mainCommit != later {
		t.Fatalf("status=%s error=%q head=%s main_commit=%s", status, lastError, git("rev-parse", "HEAD"), mainCommit)
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
	w := NewWorker(db, fake, time.Hour, func(int64) (string, error) { return dir, nil }, t.TempDir())
	_ = w.Scan(t.Context())
	got, _ := svc.Get(t.Context(), r.ID)
	if len(got.ArtifactHash) != 64 {
		t.Fatalf("hash=%q", got.ArtifactHash)
	}
}

func TestTestStepWaitsForProjectGitLock(t *testing.T) {
	db := testutil.OpenDB(t)
	service := NewService(db)
	run, err := service.Start(t.Context(), StartInput{
		ProjectID: 1,
		Definition: Definition{Steps: []StepDefinition{{
			ID: "test", Kind: "test", Command: "go", Args: []string{"test", "./..."},
			TimeoutSeconds: 2, MaxAttempts: 1, Reversible: true,
		}}},
		IdempotencyKey: "test-step-project-lock",
		RequestedBy:    "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	release, err := gitx.AcquireProjectLock(t.Context(), dataDir, 1)
	if err != nil {
		t.Fatal(err)
	}
	called := make(chan struct{}, 1)
	projectDir := t.TempDir()
	worker := NewWorker(db, signalingRunner{called: called}, time.Hour,
		func(int64) (string, error) { return projectDir, nil }, dataDir)
	done := make(chan error, 1)
	go func() {
		done <- worker.Scan(t.Context())
	}()

	deadline := time.After(2 * time.Second)
	for {
		var status string
		if queryErr := db.QueryRow(`select status from pipeline_steps where run_id=?`, run.ID).Scan(&status); queryErr != nil {
			release()
			t.Fatal(queryErr)
		}
		if status == "running" {
			break
		}
		select {
		case <-deadline:
			release()
			t.Fatalf("step did not reach running state: %s", status)
		case <-time.After(10 * time.Millisecond):
		}
	}
	select {
	case <-called:
		release()
		t.Fatal("test runner executed before the project git lock was released")
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pipeline did not resume after the project git lock was released")
	}
	select {
	case <-called:
	default:
		t.Fatal("test runner was not executed after the project git lock was released")
	}
}

func TestRollbackRefusesAdvancedMainHead(t *testing.T) {
	db := testutil.OpenDB(t)
	dir := t.TempDir()
	git := func(args ...string) string {
		command := exec.Command("git", args...)
		command.Dir = dir
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "-b", "main")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "test")
	git("commit", "--allow-empty", "-m", "safe")
	safe := git("rev-parse", "HEAD")
	git("commit", "--allow-empty", "-m", "released")
	released := git("rev-parse", "HEAD")
	git("commit", "--allow-empty", "-m", "later merge")
	advanced := git("rev-parse", "HEAD")
	if _, err := db.Exec(`insert into projects(id,slug,dir,status,main_commit,remote_url,created_at)
		values(1,'advanced-rollback',?,'active',?,'','now')`, dir, advanced); err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	run, err := service.Start(t.Context(), StartInput{
		ProjectID: 1,
		Definition: Definition{Steps: []StepDefinition{{
			ID: "build", Kind: "build", Command: "go", Args: []string{"build", "./..."},
			TimeoutSeconds: 2, MaxAttempts: 1, Reversible: true,
		}}},
		SafeCommit: safe, IdempotencyKey: "advanced-rollback", RequestedBy: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`update pipeline_steps set status='failed' where run_id=?`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`insert into pipeline_rollbacks(run_id,safe_commit,expected_head,status,created_at)
		values(?,?,?,'pending','now')`, run.ID, safe, released); err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(db, &fakeRunner{results: []Result{{}}}, time.Hour,
		func(int64) (string, error) { return dir, nil }, t.TempDir())
	if err = worker.Scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status, lastError, mainCommit string
	if err = db.QueryRow(`select status,last_error from pipeline_rollbacks where run_id=?`, run.ID).Scan(&status, &lastError); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`select main_commit from projects where id=1`).Scan(&mainCommit); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || !strings.Contains(lastError, "advanced") ||
		git("rev-parse", "HEAD") != advanced || mainCommit != advanced {
		t.Fatalf("status=%s error=%q head=%s main_commit=%s", status, lastError, git("rev-parse", "HEAD"), mainCommit)
	}
}

func TestRollbackCanResumeAfterGitResetAlreadyCompleted(t *testing.T) {
	db := testutil.OpenDB(t)
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer health.Close()
	dir := t.TempDir()
	git := func(args ...string) string {
		command := exec.Command("git", args...)
		command.Dir = dir
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "-b", "main")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("app.bin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".gitignore")
	git("commit", "-m", "safe")
	safe := git("rev-parse", "HEAD")
	git("commit", "--allow-empty", "-m", "released")
	released := git("rev-parse", "HEAD")
	if _, err := db.Exec(`insert into projects(id,slug,dir,status,main_commit,remote_url,created_at)
		values(1,'resume-rollback',?,'active',?,'','now')`, dir, released); err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	run, err := service.Start(t.Context(), StartInput{
		ProjectID: 1,
		Definition: Definition{Steps: []StepDefinition{
			{
				ID: "build", Kind: "build", Command: "go", Args: []string{"build", "./..."},
				Artifact: "app.bin", TimeoutSeconds: 2, MaxAttempts: 1, Reversible: true,
			},
			{
				ID: "release", Kind: "release", Command: "pm2", Args: []string{"restart", "app"},
				HealthURL: health.URL + "/health", TimeoutSeconds: 2, MaxAttempts: 1, Reversible: true,
			},
		}},
		SafeCommit: safe, IdempotencyKey: "resume-rollback", RequestedBy: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`update pipeline_steps set status='failed' where run_id=?`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`insert into pipeline_rollbacks(run_id,safe_commit,expected_head,status,created_at)
		values(?,?,?,'pending','now')`, run.ID, safe, released); err != nil {
		t.Fatal(err)
	}
	fake := &fakeRunner{results: []Result{{}}}
	worker := NewWorker(db, fake, time.Hour, func(int64) (string, error) { return dir, nil }, t.TempDir())
	worker.pm2Path = func(context.Context, string, string) (string, error) {
		return filepath.Join(dir, "app.bin"), nil
	}
	if err = worker.Scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status, lastError, mainCommit string
	if err = db.QueryRow(`select status,last_error from pipeline_rollbacks where run_id=?`, run.ID).Scan(&status, &lastError); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`select main_commit from projects where id=1`).Scan(&mainCommit); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || !strings.Contains(lastError, "backup unavailable") ||
		git("rev-parse", "HEAD") != safe || mainCommit != safe || fake.calls != 0 {
		t.Fatalf("first attempt status=%s error=%q head=%s main_commit=%s calls=%d",
			status, lastError, git("rev-parse", "HEAD"), mainCommit, fake.calls)
	}

	backup := filepath.Join(t.TempDir(), "app.bin")
	if err = os.WriteFile(backup, []byte("safe-binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	backupHash, err := hashArtifact(backup)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`update pipeline_runs set backup_path=?,backup_hash=? where id=?`, backup, backupHash, run.ID); err != nil {
		t.Fatal(err)
	}
	if err = service.ConfirmRollback(t.Context(), run.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if err = worker.Scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	var restored []byte
	if restored, err = os.ReadFile(filepath.Join(dir, "app.bin")); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`select status,last_error from pipeline_rollbacks where run_id=?`, run.ID).Scan(&status, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "rolled_back" || lastError != "" || git("rev-parse", "HEAD") != safe ||
		string(restored) != "safe-binary" || fake.calls != 1 {
		t.Fatalf("second attempt status=%s error=%q head=%s artifact=%q calls=%d",
			status, lastError, git("rev-parse", "HEAD"), restored, fake.calls)
	}
}
