package config

import (
	"path/filepath"
	"testing"
)

func TestLoadDefaultsFromRoot(t *testing.T) {
	root := t.TempDir()
	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.RootDir != root {
		t.Fatalf("RootDir = %q, want %q", cfg.RootDir, root)
	}
	if cfg.DataDir != filepath.Join(root, "data") {
		t.Fatalf("DataDir = %q", cfg.DataDir)
	}
	if cfg.AgentDir != filepath.Join(root, "agents") {
		t.Fatalf("AgentDir = %q", cfg.AgentDir)
	}
	if cfg.ProjectDir != filepath.Join(root, "projects") {
		t.Fatalf("ProjectDir = %q", cfg.ProjectDir)
	}
	if cfg.RemoteURL != "https://github.com/firesky594/wanxiang.git" {
		t.Fatalf("RemoteURL = %q", cfg.RemoteURL)
	}
	if cfg.HTTPAddr != ":8088" {
		t.Fatalf("HTTPAddr = %q", cfg.HTTPAddr)
	}
}
