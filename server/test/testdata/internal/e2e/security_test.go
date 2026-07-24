package e2e

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"wanxiang-agent/server/internal/pipelines"
	"wanxiang-agent/server/internal/testutil"
)

type leakingRunner struct{}

func (leakingRunner) Run(context.Context, string, pipelines.Step) pipelines.Result {
	return pipelines.Result{Output: "Authorization=Bearer TEST_SECRET", FailureClass: "code_failure", Err: errors.New("token=TEST_SECRET")}
}
func TestPipelineRejectsInjectionAndRedactsPersistentEvidence(t *testing.T) {
	unsafe := pipelines.Definition{Steps: []pipelines.StepDefinition{{ID: "bad", Kind: "test", Command: "go", Args: []string{"test", "./...; rm -rf ."}, TimeoutSeconds: 1, MaxAttempts: 1, Reversible: true}}}
	if pipelines.Validate(unsafe) == nil {
		t.Fatal("command injection accepted")
	}
	db := testutil.OpenDB(t)
	svc := pipelines.NewService(db)
	safe := pipelines.Definition{Steps: []pipelines.StepDefinition{{ID: "test", Kind: "test", Command: "go", Args: []string{"test", "./..."}, TimeoutSeconds: 1, MaxAttempts: 1, Reversible: true}}}
	run, _ := svc.Start(t.Context(), pipelines.StartInput{ProjectID: 1, Definition: safe, IdempotencyKey: "secret", RequestedBy: "admin"})
	w := pipelines.NewWorker(db, leakingRunner{}, time.Hour, func(int64) (string, error) { return t.TempDir(), nil })
	_ = w.Scan(t.Context())
	var output string
	_ = db.QueryRow(`select output_summary from pipeline_steps where run_id=?`, run.ID).Scan(&output)
	if strings.Contains(output, "TEST_SECRET") || !strings.Contains(output, "[REDACTED]") {
		t.Fatalf("secret persisted: %q", output)
	}
}
