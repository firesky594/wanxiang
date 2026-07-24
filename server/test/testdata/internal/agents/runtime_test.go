package agents

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/testutil"
)

func TestWriteMemoryStaysInsideAgentMemory(t *testing.T) {
	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	svc := NewService(cfg, conn)
	if _, err := svc.EnsureManager(context.Background()); err != nil {
		t.Fatalf("EnsureManager: %v", err)
	}
	if err := svc.WriteMemory(context.Background(), "manager", "summaries/task-1.md", "summary"); err != nil {
		t.Fatalf("WriteMemory: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(cfg.AgentDir, "manager", "memory", "summaries", "task-1.md"))
	if err != nil {
		t.Fatalf("Read memory: %v", err)
	}
	if string(body) != "summary" {
		t.Fatalf("body=%q", body)
	}
	if err := svc.WriteMemory(context.Background(), "manager", "../env", "bad"); err == nil {
		t.Fatalf("path traversal should fail")
	}
}

func TestWriteMemoryRejectsInvalidAgentName(t *testing.T) {
	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	svc := NewService(cfg, conn)

	if err := svc.WriteMemory(context.Background(), "../../escape", "notes.md", "bad"); err == nil {
		t.Fatalf("invalid agent name should fail")
	}
	if _, err := os.Stat(filepath.Join(root, "escape", "memory", "notes.md")); !os.IsNotExist(err) {
		t.Fatalf("escaped file exists or stat failed: %v", err)
	}
}

func TestWriteMemoryRejectsSymlinkComponent(t *testing.T) {
	root := t.TempDir()
	cfg, _ := config.Load(root)
	conn := testutil.OpenDB(t)
	svc := NewService(cfg, conn)
	if _, err := svc.EnsureManager(context.Background()); err != nil {
		t.Fatalf("EnsureManager: %v", err)
	}

	outside := t.TempDir()
	link := filepath.Join(cfg.AgentDir, "manager", "memory", "linked")
	if err := os.Symlink(outside, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatalf("Symlink: %v", err)
	}

	if err := svc.WriteMemory(context.Background(), "manager", "linked/escape.md", "bad"); err == nil {
		t.Fatalf("symlink write should fail")
	}
	if _, err := os.Stat(filepath.Join(outside, "escape.md")); !os.IsNotExist(err) {
		t.Fatalf("escaped file exists or stat failed: %v", err)
	}
}
