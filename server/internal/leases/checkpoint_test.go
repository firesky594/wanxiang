package leases

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wanxiang-agent/server/internal/gitx"
)

func TestCheckpointValidatesGitAndIsIdempotent(t *testing.T) {
	svc, conn, _, taskID, stepID := leaseFixture(t)
	repo, base := checkpointRepo(t)
	if _, err := conn.Exec(`update project_workspaces set worktree_path=?,base_commit=?,provision_commit=?,write_scope_json='["."]' where step_id=?`, repo, base, base, stepID); err != nil {
		t.Fatal(err)
	}
	lease, err := svc.Acquire(t.Context(), taskID, stepID, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	commit := commitCheckpoint(t, repo, stepID)
	input := validCheckpointInput(commit)
	first, err := svc.CreateCheckpoint(t.Context(), lease.LeaseRef, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.CreateCheckpoint(t.Context(), lease.LeaseRef, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == 0 || second.ID != first.ID || first.SummaryHash == "" {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	var checkpoints, events int
	_ = conn.QueryRow(`select count(*) from task_checkpoints where lease_id=?`, lease.LeaseID).Scan(&checkpoints)
	_ = conn.QueryRow(`select count(*) from runtime_events where task_id=? and event_type='task.step.checkpointed'`, taskID).Scan(&events)
	if checkpoints != 1 || events != 1 {
		t.Fatalf("checkpoints=%d events=%d", checkpoints, events)
	}
	mirror := filepath.Join(repo, ".wanxiang", "checkpoints", itoa64(stepID), itoa64(first.ID)+".yaml")
	content, err := os.ReadFile(mirror)
	if err != nil || !strings.Contains(string(content), "next_action") {
		t.Fatalf("mirror content=%q err=%v", content, err)
	}
}

func TestCheckpointRejectsGitMismatch(t *testing.T) {
	for _, scenario := range []string{"branch", "head", "ancestor", "clean"} {
		t.Run(scenario, func(t *testing.T) {
			svc, conn, _, taskID, stepID := leaseFixture(t)
			repo, base := checkpointRepo(t)
			_, _ = conn.Exec(`update project_workspaces set worktree_path=?,base_commit=?,provision_commit=?,write_scope_json='["."]' where step_id=?`, repo, base, base, stepID)
			lease, err := svc.Acquire(t.Context(), taskID, stepID, "agent-a")
			if err != nil {
				t.Fatal(err)
			}
			commit := commitCheckpoint(t, repo, stepID)
			input := validCheckpointInput(commit)
			switch scenario {
			case "branch":
				input.BranchName = "agent/other/branch"
			case "head":
				input.GitCommit = base
			case "ancestor":
				_, _ = conn.Exec(`update project_workspaces set provision_commit='0000000000000000000000000000000000000000' where step_id=?`, stepID)
			case "clean":
				if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := svc.CreateCheckpoint(t.Context(), lease.LeaseRef, input); err == nil {
				t.Fatalf("scenario %s was accepted", scenario)
			}
		})
	}
}

func TestCheckpointValidatesSummaryAndAllowsDirtyContext(t *testing.T) {
	svc, conn, _, taskID, stepID := leaseFixture(t)
	repo, base := checkpointRepo(t)
	_, _ = conn.Exec(`update project_workspaces set worktree_path=?,base_commit=?,provision_commit=?,write_scope_json='["src"]' where step_id=?`, repo, base, base, stepID)
	lease, err := svc.Acquire(t.Context(), taskID, stepID, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "partial.go"), []byte("package partial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	input := CheckpointInput{
		IdempotencyKey: "dirty-1", BranchName: "agent/agent-a/lease", Clean: false, Files: []string{"src/partial.go"},
		Summary: RecoverySummary{Completed: []string{"建立文件"}, NextAction: "补充单元测试"},
	}
	checkpoint, err := svc.CreateCheckpoint(t.Context(), lease.LeaseRef, input)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.Clean || checkpoint.GitCommit != "" {
		t.Fatalf("dirty checkpoint=%+v", checkpoint)
	}
	if _, err := os.Stat(filepath.Join(repo, "src", "partial.go")); err != nil {
		t.Fatalf("dirty file was changed: %v", err)
	}

	for name, mutate := range map[string]func(*CheckpointInput){
		"empty next action": func(value *CheckpointInput) { value.Summary.NextAction = "" },
		"secret":            func(value *CheckpointInput) { value.Summary.Decisions = []string{"api_token=exposed"} },
		"control":           func(value *CheckpointInput) { value.Summary.NextAction = "run\x00test" },
		"outside scope":     func(value *CheckpointInput) { value.Files = []string{"docs/secret.md"} },
		"too long":          func(value *CheckpointInput) { value.Summary.NextAction = strings.Repeat("x", 2001) },
	} {
		t.Run(name, func(t *testing.T) {
			probe := input
			probe.IdempotencyKey = "invalid-" + name
			mutate(&probe)
			if _, err := svc.CreateCheckpoint(t.Context(), lease.LeaseRef, probe); err == nil {
				t.Fatal("invalid summary was accepted")
			}
		})
	}
}

func checkpointRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	mustCheckpointGit(t, repo, "init", "-b", "agent/agent-a/lease")
	mustCheckpointGit(t, repo, "config", "user.name", "Test")
	mustCheckpointGit(t, repo, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustCheckpointGit(t, repo, "add", ".")
	mustCheckpointGit(t, repo, "commit", "-m", "初始化")
	return repo, strings.TrimSpace(mustCheckpointGit(t, repo, "rev-parse", "HEAD"))
}

func commitCheckpoint(t *testing.T, repo string, stepID int64) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, "work.go"), []byte("package work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustCheckpointGit(t, repo, "add", ".")
	mustCheckpointGit(t, repo, "commit", "-m", "checkpoint("+itoa64(stepID)+"): 完成基础实现")
	return strings.TrimSpace(mustCheckpointGit(t, repo, "rev-parse", "HEAD"))
}

func validCheckpointInput(commit string) CheckpointInput {
	return CheckpointInput{
		IdempotencyKey: "checkpoint-1", GitCommit: commit, BranchName: "agent/agent-a/lease", Clean: true,
		Files: []string{"work.go"}, Tests: []CheckpointTest{{Command: "go test ./...", Result: "passed"}},
		Summary: RecoverySummary{Completed: []string{"完成基础实现"}, NextAction: "开始边界测试", FilesChanged: []string{"work.go"}, Decisions: []string{"保持接口最小化"}},
	}
}

func mustCheckpointGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := gitx.Run(t.Context(), repo, args...)
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return out
}

func itoa64(value int64) string {
	return fmt.Sprintf("%d", value)
}
