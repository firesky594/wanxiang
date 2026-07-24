package leases

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/workspaces"
)

func TestInterruptExpiredIsIdempotentAndSurvivesServiceRestart(t *testing.T) {
	svc, conn, clock, taskID, stepID := leaseFixture(t)
	lease, err := svc.Acquire(t.Context(), taskID, stepID, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(LeaseTTL)
	count, err := svc.InterruptExpired(t.Context())
	if err != nil || count != 1 {
		t.Fatalf("first scan count=%d err=%v", count, err)
	}
	count, err = svc.InterruptExpired(t.Context())
	if err != nil || count != 0 {
		t.Fatalf("second scan count=%d err=%v", count, err)
	}
	var status, interrupted, deadline string
	if err := conn.QueryRow(`select status,interrupted_at,resume_deadline from task_step_leases where lease_id=?`, lease.LeaseID).Scan(&status, &interrupted, &deadline); err != nil {
		t.Fatal(err)
	}
	if status != string(LeaseInterrupted) || interrupted != formatTime(clock.Now()) || deadline != formatTime(clock.Now().Add(ResumeWindow)) {
		t.Fatalf("status=%s interrupted=%s deadline=%s", status, interrupted, deadline)
	}
	var events int
	_ = conn.QueryRow(`select count(*) from runtime_events where task_id=? and event_type='task.step.interrupted'`, taskID).Scan(&events)
	if events != 1 {
		t.Fatalf("events=%d", events)
	}
	restarted := NewService(conn, clock, workspaces.NewService(config.Config{}, conn, nil))
	loaded, err := loadLease(t.Context(), restarted.db, lease.LeaseID)
	if err != nil || loaded.Status != LeaseInterrupted || loaded.ResumeDeadline == nil {
		t.Fatalf("restarted lease=%+v err=%v", loaded, err)
	}
}

func TestResumeOriginalAgentValidatesDeadlineAndGitState(t *testing.T) {
	svc, conn, clock, taskID, stepID := leaseFixture(t)
	repo, base := checkpointRepo(t)
	_, _ = conn.Exec(`update project_workspaces set worktree_path=?,base_commit=?,provision_commit=?,write_scope_json='["."]' where step_id=?`, repo, base, base, stepID)
	lease, err := svc.Acquire(t.Context(), taskID, stepID, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	commit := commitCheckpoint(t, repo, stepID)
	checkpoint, err := svc.CreateCheckpoint(t.Context(), lease.LeaseRef, validCheckpointInput(commit))
	if err != nil || checkpoint.ID == 0 {
		t.Fatalf("checkpoint=%+v err=%v", checkpoint, err)
	}
	clock.Advance(LeaseTTL)
	if _, err := svc.InterruptExpired(t.Context()); err != nil {
		t.Fatal(err)
	}
	restarted := NewService(conn, clock, workspaces.NewService(config.Config{}, conn, nil))
	resumed, err := restarted.Resume(t.Context(), lease.LeaseRef)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.LeaseID != lease.LeaseID || resumed.LeaseVersion != lease.LeaseVersion || resumed.Status != LeaseActive || !resumed.ExpiresAt.Equal(clock.Now().Add(LeaseTTL)) {
		t.Fatalf("resumed=%+v", resumed)
	}
}

func TestResumeRejectsExpiredWindowNewVersionAndGitDrift(t *testing.T) {
	for _, scenario := range []string{"deadline", "version", "head", "dirty"} {
		t.Run(scenario, func(t *testing.T) {
			svc, conn, clock, taskID, stepID := leaseFixture(t)
			repo, base := checkpointRepo(t)
			_, _ = conn.Exec(`update project_workspaces set worktree_path=?,base_commit=?,provision_commit=?,write_scope_json='["."]' where step_id=?`, repo, base, base, stepID)
			lease, err := svc.Acquire(t.Context(), taskID, stepID, "agent-a")
			if err != nil {
				t.Fatal(err)
			}
			commit := commitCheckpoint(t, repo, stepID)
			if _, err = svc.CreateCheckpoint(t.Context(), lease.LeaseRef, validCheckpointInput(commit)); err != nil {
				t.Fatal(err)
			}
			clock.Advance(LeaseTTL)
			_, _ = svc.InterruptExpired(t.Context())
			probe := lease.LeaseRef
			switch scenario {
			case "deadline":
				clock.Advance(ResumeWindow)
			case "version":
				probe.LeaseVersion++
			case "head":
				mustCheckpointGit(t, repo, "reset", "--hard", base)
			case "dirty":
				if err := os.WriteFile(filepath.Join(repo, "unexpected.txt"), []byte("drift"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := svc.Resume(t.Context(), probe); !errors.Is(err, ErrConflict) {
				t.Fatalf("scenario=%s err=%v", scenario, err)
			}
			var status string
			_ = conn.QueryRow(`select status from task_step_leases where lease_id=?`, lease.LeaseID).Scan(&status)
			if status != string(LeaseInterrupted) {
				t.Fatalf("status=%s", status)
			}
		})
	}
}

func TestResumeDirtyCheckpointPreservesRecordedFiles(t *testing.T) {
	svc, conn, clock, taskID, stepID := leaseFixture(t)
	repo, base := checkpointRepo(t)
	_, _ = conn.Exec(`update project_workspaces set worktree_path=?,base_commit=?,provision_commit=?,write_scope_json='["."]' where step_id=?`, repo, base, base, stepID)
	lease, _ := svc.Acquire(t.Context(), taskID, stepID, "agent-a")
	partial := filepath.Join(repo, "partial.go")
	if err := os.WriteFile(partial, []byte("package partial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	input := CheckpointInput{IdempotencyKey: "dirty-resume", BranchName: "agent/agent-a/lease", Clean: false, Files: []string{"partial.go"}, Summary: RecoverySummary{NextAction: "完成 partial.go"}}
	if _, err := svc.CreateCheckpoint(t.Context(), lease.LeaseRef, input); err != nil {
		t.Fatal(err)
	}
	clock.Advance(LeaseTTL)
	_, _ = svc.InterruptExpired(t.Context())
	if _, err := svc.Resume(t.Context(), lease.LeaseRef); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(partial); err != nil {
		t.Fatalf("partial file changed: %v", err)
	}
}
