package pipelines

import (
	"os"
	"path/filepath"
	"testing"
	"wanxiang-agent/server/internal/testutil"
)

func TestDefinitionAndStartAreStrictIdempotentAndConfirmRelease(t *testing.T) {
	db := testutil.OpenDB(t)
	svc := NewService(db)
	d := Definition{Steps: []StepDefinition{{ID: "test", Kind: "test", Command: "go", Args: []string{"test", "./..."}, TimeoutSeconds: 30, MaxAttempts: 2, Reversible: true}, {ID: "build", Kind: "build", Command: "go", Args: []string{"build", "./..."}, Artifact: "app.bin", TimeoutSeconds: 30, MaxAttempts: 1, Reversible: true}, {ID: "release", Kind: "release", Command: "pm2", Args: []string{"restart", "app"}, HealthURL: "http://127.0.0.1:30188/api/health", TimeoutSeconds: 30, MaxAttempts: 1, Reversible: true}}}
	r, e := svc.Start(t.Context(), StartInput{ProjectID: 1, Definition: d, SafeCommit: "abc", IdempotencyKey: "one", RequestedBy: "admin"})
	if e != nil || len(r.Steps) != 3 || r.Steps[2].Status != "awaiting_confirmation" {
		t.Fatalf("%+v %v", r, e)
	}
	again, _ := svc.Start(t.Context(), StartInput{ProjectID: 1, Definition: d, IdempotencyKey: "one", RequestedBy: "admin"})
	if again.ID != r.ID {
		t.Fatal("not idempotent")
	}
	if _, e = svc.Confirm(t.Context(), r.ID, "release", "admin"); e != nil {
		t.Fatal(e)
	}
}
func TestLoadDefinitionRejectsShellAndEscape(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".wanxiang"), 0755)
	_ = os.WriteFile(filepath.Join(dir, ".wanxiang", "pipeline.json"), []byte(`{"steps":[{"id":"x","kind":"test","command":"go","args":["test","../x;rm"],"timeout_seconds":1,"max_attempts":1,"reversible":true}]}`), 0644)
	if _, e := LoadDefinition(dir); e == nil {
		t.Fatal("unsafe accepted")
	}
}
func TestValidateRejectsUniversalExecutionAndSecretArguments(t *testing.T) {
	for _, s := range []StepDefinition{{ID: "x", Kind: "test", Command: "node", Args: []string{"-e", "process.exit()"}, TimeoutSeconds: 1, MaxAttempts: 1, Reversible: true}, {ID: "x", Kind: "test", Command: "npm", Args: []string{"exec", "evil"}, TimeoutSeconds: 1, MaxAttempts: 1, Reversible: true}, {ID: "x", Kind: "test", Command: "go", Args: []string{"run", "./evil.go"}, TimeoutSeconds: 1, MaxAttempts: 1, Reversible: true}, {ID: "x", Kind: "test", Command: "go", Args: []string{"test", "./...", "--token=secret"}, TimeoutSeconds: 1, MaxAttempts: 1, Reversible: true}, {ID: "x", Kind: "test", Command: "pm2", Args: []string{"kill"}, TimeoutSeconds: 1, MaxAttempts: 1, Reversible: true}} {
		if Validate(Definition{Steps: []StepDefinition{s}}) == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
}
