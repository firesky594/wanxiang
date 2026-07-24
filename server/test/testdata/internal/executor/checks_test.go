package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunCheckUsesAllowlistWorktreeAndRedactsOutput(t *testing.T) {
	tools, ref, root := fileToolsFixture(t)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module checkdemo\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main_test.go"), []byte("package src\nimport (\"fmt\"; \"testing\")\nfunc TestSecret(t *testing.T){fmt.Println(\"API_KEY=hidden-value\")}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := NewCheckRunner(tools.db, tools.leases)
	result := runner.RunCheck(t.Context(), ref, CheckRequest{Command: "go", Args: []string{"test", "-v", "./src"}, Timeout: 20 * time.Second})
	if result.ExitCode != 0 || result.TimedOut {
		t.Fatalf("result=%+v", result)
	}
	if strings.Contains(result.Output, "hidden-value") || !strings.Contains(result.Output, "[REDACTED]") {
		t.Fatalf("output=%q", result.Output)
	}
}

func TestRunCheckRejectsShellDangerousAndInvalidLease(t *testing.T) {
	tools, ref, _ := fileToolsFixture(t)
	runner := NewCheckRunner(tools.db, tools.leases)
	requests := []CheckRequest{
		{Command: "bash", Args: []string{"-c", "go test ./..."}},
		{Command: "go", Args: []string{"test", "./...", "|", "cat"}},
		{Command: "go", Args: []string{"test", "./...", ">out"}},
		{Command: "rm", Args: []string{"-rf", "."}},
		{Command: "go", Args: []string{"run", "./cmd/deploy"}},
		{Command: "npm", Args: []string{"run", "deploy"}},
	}
	for _, request := range requests {
		if got := runner.RunCheck(t.Context(), ref, request); got.Error == "" {
			t.Fatalf("accepted %+v", request)
		}
	}
	bad := ref
	bad.LeaseVersion++
	if got := runner.RunCheck(t.Context(), bad, CheckRequest{Command: "go", Args: []string{"test", "./..."}}); got.Error == "" {
		t.Fatal("invalid lease accepted")
	}
}

func TestRunCheckEnforcesTimeoutAndOutputLimit(t *testing.T) {
	tools, ref, root := fileToolsFixture(t)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module timeoutdemo\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := NewCheckRunner(tools.db, tools.leases)
	result := runner.RunCheck(t.Context(), ref, CheckRequest{Command: "go", Args: []string{"test", "./..."}, Timeout: time.Nanosecond})
	if !result.TimedOut {
		t.Fatalf("result=%+v", result)
	}
	if len(result.Output) > maxCheckOutputBytes {
		t.Fatalf("output length=%d", len(result.Output))
	}
}
