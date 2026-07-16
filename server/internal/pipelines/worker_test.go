package pipelines

import (
	"context"
	"errors"
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
	d := Definition{Steps: []StepDefinition{{ID: "release", Kind: "release", Command: "pm2", Args: []string{"restart", "app"}, TimeoutSeconds: 2, MaxAttempts: 1, Reversible: true}}}
	r, _ := svc.Start(t.Context(), StartInput{ProjectID: 1, Definition: d, SafeCommit: "abc", IdempotencyKey: "rel", RequestedBy: "admin"})
	fake := &fakeRunner{results: []Result{{FailureClass: "code_failure", Err: errors.New("failed")}}}
	w := NewWorker(db, fake, time.Hour, func(int64) (string, error) { return t.TempDir(), nil })
	_ = w.Scan(t.Context())
	if fake.calls != 0 {
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
