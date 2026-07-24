package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopyTestEnvCopiesWithoutOverwriting(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "manager", "env")
	target := filepath.Join(root, "worker", "env")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	secret := "AGENT_API_KEY=test-secret-value\nAGENT_MODEL=test-model\n"
	if err := os.WriteFile(source, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CopyTestEnv(source, target); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != secret {
		t.Fatalf("content=%q err=%v", content, err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v err=%v", info.Mode().Perm(), err)
	}

	if err := os.WriteFile(target, []byte("preserve\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = CopyTestEnv(source, target)
	if err == nil {
		t.Fatal("existing target was overwritten")
	}
	if strings.Contains(err.Error(), "test-secret-value") {
		t.Fatalf("error leaked source content: %v", err)
	}
	content, _ = os.ReadFile(target)
	if string(content) != "preserve\n" {
		t.Fatalf("existing target changed: %q", content)
	}
}

func TestCopyTestEnvRejectsSymlinkSource(t *testing.T) {
	root := t.TempDir()
	realSource := filepath.Join(root, "real-env")
	linkSource := filepath.Join(root, "manager-env")
	if err := os.WriteFile(realSource, []byte("AGENT_API_KEY=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realSource, linkSource); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := CopyTestEnv(linkSource, filepath.Join(root, "worker", "env")); err == nil {
		t.Fatal("symlink source was accepted")
	}
}
