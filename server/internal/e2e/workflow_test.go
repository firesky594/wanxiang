package e2e

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
	"wanxiang-agent/server/internal/pipelines"
	"wanxiang-agent/server/internal/testutil"
)

type sequenceRunner struct {
	calls   int
	results []pipelines.Result
}

func (r *sequenceRunner) Run(context.Context, string, pipelines.Step) pipelines.Result {
	x := r.results[r.calls]
	r.calls++
	return x
}
func TestLocalPipelineConfirmationRetryAndRollbackTimeline(t *testing.T) {
	db := testutil.OpenDB(t)
	svc := pipelines.NewService(db)
	definition := pipelines.Definition{Steps: []pipelines.StepDefinition{{ID: "test", Kind: "test", Command: "go", Args: []string{"test", "./..."}, TimeoutSeconds: 5, MaxAttempts: 2, Reversible: true}, {ID: "build", Kind: "build", Command: "go", Args: []string{"build", "./..."}, Artifact: "app.bin", TimeoutSeconds: 5, MaxAttempts: 1, Reversible: true}, {ID: "release", Kind: "release", Command: "pm2", Args: []string{"restart", "demo"}, HealthURL: "http://127.0.0.1:1/health", TimeoutSeconds: 5, MaxAttempts: 1, Reversible: true}}}
	run, err := svc.Start(t.Context(), pipelines.StartInput{ProjectID: 1, Definition: definition, SafeCommit: "safe123", IdempotencyKey: "e2e", RequestedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "app.bin"), []byte("old"), 0644)
	runner := &sequenceRunner{results: []pipelines.Result{{FailureClass: "environment_failure", Err: errors.New("temporary")}, {}, {}}}
	worker := pipelines.NewWorker(db, runner, time.Hour, func(int64) (string, error) { return dir, nil })
	_ = worker.Scan(t.Context())
	_, _ = db.Exec(`update pipeline_steps set next_retry_at='2000-01-01' where run_id=?`, run.ID)
	_ = worker.Scan(t.Context())
	_ = worker.Scan(t.Context())
	if runner.calls != 3 {
		t.Fatalf("release ran without confirmation calls=%d", runner.calls)
	}
	if _, err = svc.Confirm(t.Context(), run.ID, "release", "admin"); err != nil {
		t.Fatal(err)
	}
	_ = worker.Scan(t.Context())
	var safe, status string
	if err = db.QueryRow(`select safe_commit,status from pipeline_rollbacks where run_id=?`, run.ID).Scan(&safe, &status); err != nil || safe != "safe123" || status != "awaiting_confirmation" {
		t.Fatalf("safe=%s status=%s err=%v", safe, status, err)
	}
}
