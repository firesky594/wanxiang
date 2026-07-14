package matching

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefinitionReadsNonSecretCapabilitiesAndResources(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "backend-dev")
	mustMkdir(t, filepath.Join(dir, "skills", "go"))
	mustMkdir(t, filepath.Join(dir, "mcps", "github"))
	mustMkdir(t, filepath.Join(dir, "memory", "summaries"))
	mustWrite(t, filepath.Join(dir, "agent.yaml"), "role: backend\ncapabilities:\n  - go\n  - sqlite\nmax_concurrency: 2\nproject_access:\n  - project-a\n")
	mustWrite(t, filepath.Join(dir, "memory", "summaries", "auth.md"), "Go authentication and SQLite migrations")
	got, err := LoadDefinition(root, "backend-dev")
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != "backend" || got.MaxConcurrency != 2 || strings.Join(got.Capabilities, ",") != "go,sqlite" || strings.Join(got.Skills, ",") != "go" || strings.Join(got.MCPs, ",") != "github" || !strings.Contains(got.MemorySummary, "authentication") {
		t.Fatalf("definition=%+v", got)
	}
}

func TestLoadDefinitionRejectsUnsafeDefinitions(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "worker")
	mustMkdir(t, dir)
	mustWrite(t, filepath.Join(dir, "agent.yaml"), "role: backend\napi_key: exposed\n")
	if _, err := LoadDefinition(root, "worker"); err == nil || !strings.Contains(err.Error(), "secret field") {
		t.Fatalf("err=%v", err)
	}
	if _, err := LoadDefinition(root, "../worker"); err == nil {
		t.Fatal("expected invalid name")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDefinition(root, "linked"); err == nil {
		t.Fatal("expected symlink rejection")
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}
func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
