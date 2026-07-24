package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"wanxiang-agent/server/internal/leases"
)

func TestGitCheckpointCommitsScopedFilesWithChineseMessage(t *testing.T) {
	tools, ref, root := fileToolsFixture(t)
	os.MkdirAll(filepath.Join(root, "src"), 0o755)
	os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package src\n"), 0o644)
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "初始化")
	base := strings.TrimSpace(git(t, root, "rev-parse", "HEAD"))
	if _, err := tools.db.Exec(`update project_workspaces set base_commit=?,provision_commit=? where step_id=?`, base, base, ref.StepID); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package src\n// 完成\n"), 0o644)
	creator := &recordingCheckpointCreator{}
	checkpoint, err := NewCheckpointRunner(tools.db, tools.leases, creator, t.TempDir()).CreateGitCheckpoint(t.Context(), ref, WorkerSummary{Completed: []string{"完成实现"}, NextAction: "继续测试", Tests: []leases.CheckpointTest{{Command: "go test ./...", Result: "passed"}}})
	if err != nil || checkpoint.GitCommit == "" {
		t.Fatalf("checkpoint=%+v err=%v", checkpoint, err)
	}
	if subject := strings.TrimSpace(git(t, root, "log", "-1", "--pretty=%s")); !strings.Contains(subject, "完成实现") {
		t.Fatalf("subject=%q", subject)
	}
}

type recordingCheckpointCreator struct{ input leases.CheckpointInput }

func (r *recordingCheckpointCreator) CreateCheckpoint(_ context.Context, ref leases.LeaseRef, input leases.CheckpointInput) (leases.Checkpoint, error) {
	r.input = input
	return leases.Checkpoint{TaskID: ref.TaskID, StepID: ref.StepID, GitCommit: input.GitCommit}, nil
}

func TestGitCheckpointRejectsSensitiveUnknownFile(t *testing.T) {
	tools, ref, root := fileToolsFixture(t)
	os.WriteFile(filepath.Join(root, "README.md"), []byte("base"), 0o644)
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "初始化")
	base := strings.TrimSpace(git(t, root, "rev-parse", "HEAD"))
	tools.db.Exec(`update project_workspaces set base_commit=?,provision_commit=? where step_id=?`, base, base, ref.StepID)
	os.WriteFile(filepath.Join(root, ".env"), []byte("API_KEY=secret"), 0o600)
	svc := tools.leases.(*leases.Service)
	if _, err := NewCheckpointRunner(tools.db, tools.leases, svc, t.TempDir()).CreateGitCheckpoint(t.Context(), ref, WorkerSummary{Completed: []string{"完成"}, NextAction: "继续"}); err == nil {
		t.Fatal("expected rejection")
	}
}

type failingCheckpointCreator struct{}

func (*failingCheckpointCreator) CreateCheckpoint(context.Context, leases.LeaseRef, leases.CheckpointInput) (leases.Checkpoint, error) {
	return leases.Checkpoint{}, errors.New("database checkpoint failed")
}

func TestGitCheckpointRollsBackCommitWhenPersistenceFails(t *testing.T) {
	tools, ref, root := fileToolsFixture(t)
	path := filepath.Join(root, "src", "main.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package src\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stagedPath := filepath.Join(root, "src", "staged.go")
	if err := os.WriteFile(stagedPath, []byte("package src\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "--", "src/staged.go")
	originalHead := strings.TrimSpace(git(t, root, "rev-parse", "HEAD"))
	originalIndex := strings.TrimSpace(git(t, root, "write-tree"))
	runner := NewCheckpointRunner(tools.db, tools.leases, &failingCheckpointCreator{}, t.TempDir())
	if _, err := runner.CreateGitCheckpoint(t.Context(), ref, WorkerSummary{Completed: []string{"失败补偿"}, NextAction: "重试"}); err == nil {
		t.Fatal("expected persistence failure")
	}
	if head := strings.TrimSpace(git(t, root, "rev-parse", "HEAD")); head != originalHead {
		t.Fatalf("checkpoint commit was not rolled back: head=%q want=%q", head, originalHead)
	}
	if index := strings.TrimSpace(git(t, root, "write-tree")); index != originalIndex {
		t.Fatalf("checkpoint index was not restored: got=%q want=%q", index, originalIndex)
	}
	if staged := strings.TrimSpace(git(t, root, "diff", "--cached", "--name-only")); staged != "src/staged.go" {
		t.Fatalf("original staged state was not restored: %q", staged)
	}
	if status := git(t, root, "status", "--porcelain", "--untracked-files=all"); !strings.Contains(status, "src/main.go") {
		t.Fatalf("working change was lost: %q", status)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return string(out)
}
