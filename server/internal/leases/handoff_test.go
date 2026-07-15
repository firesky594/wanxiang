package leases

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wanxiang-agent/server/internal/gitx"
)

func TestReassignCreatesIndependentWorktreeFromCleanCheckpoint(t *testing.T) {
	svc, conn, clock, taskID, stepID := leaseFixture(t)
	repo, base := checkpointRepo(t)
	original := repo
	_, _ = conn.Exec(`update project_workspaces set worktree_path=?,base_commit=?,provision_commit=?,branch_name='agent/agent-a/lease',write_scope_json='["."]' where step_id=?`, original, base, base, stepID)
	_, _ = conn.Exec(`update projects set dir=? where id=(select project_id from tasks where id=?)`, repo, taskID)
	_, _ = conn.Exec(`insert into agent_registry(name,role,dir,status) values('agent-b','backend','/tmp/agent-b','online')`)
	lease, err := svc.Acquire(t.Context(), taskID, stepID, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	commit := commitCheckpoint(t, original, stepID)
	input := validCheckpointInput(commit)
	input.BranchName = "agent/agent-a/lease"
	checkpoint, err := svc.CreateCheckpoint(t.Context(), lease.LeaseRef, input)
	if err != nil {
		t.Fatal(err)
	}
	dirty := filepath.Join(original, "unfinished.txt")
	if err := os.WriteFile(dirty, []byte("preserve"), 0o644); err != nil {
		t.Fatal(err)
	}
	clock.Advance(LeaseTTL)
	_, _ = svc.InterruptExpired(t.Context())
	clock.Advance(ResumeWindow)
	newLease, err := svc.Reassign(t.Context(), ReassignInput{TaskID: taskID, StepID: stepID, NewAgent: "agent-b", CheckpointID: checkpoint.ID, Reason: "原 Agent 超时"}, "manager")
	if err != nil {
		t.Fatal(err)
	}
	if newLease.AgentName != "agent-b" || newLease.LeaseVersion != lease.LeaseVersion+1 || newLease.LeaseID == lease.LeaseID {
		t.Fatalf("new lease=%+v old=%+v", newLease, lease)
	}
	content, err := os.ReadFile(dirty)
	if err != nil || string(content) != "preserve" {
		t.Fatalf("original dirty file changed content=%q err=%v", content, err)
	}
	var branch, worktree string
	if err := conn.QueryRow(`select branch_name,worktree_path from project_workspaces where step_id=?`, stepID).Scan(&branch, &worktree); err != nil {
		t.Fatal(err)
	}
	if worktree == original || !strings.Contains(branch, "resume-2") {
		t.Fatalf("branch=%s worktree=%s", branch, worktree)
	}
	head, _ := gitx.Run(t.Context(), worktree, "rev-parse", "HEAD")
	if strings.TrimSpace(head) != commit {
		t.Fatalf("recovery head=%q want=%q", head, commit)
	}
	if err := svc.Authorize(t.Context(), lease.LeaseRef, "work.go"); !errors.Is(err, ErrConflict) {
		t.Fatalf("old lease write err=%v", err)
	}
	var historyBranch, historyWorktree string
	if err := conn.QueryRow(`select from_branch,from_worktree from step_reassignments where step_id=?`, stepID).Scan(&historyBranch, &historyWorktree); err != nil {
		t.Fatal(err)
	}
	if historyBranch != "agent/agent-a/lease" || historyWorktree != original {
		t.Fatalf("history branch=%s worktree=%s", historyBranch, historyWorktree)
	}
}

func TestReassignRequiresDeadlineUnlessImmediate(t *testing.T) {
	svc, conn, clock, taskID, stepID := leaseFixture(t)
	_, _ = conn.Exec(`insert into agent_registry(name,role,dir,status) values('agent-b','backend','/tmp/agent-b','online')`)
	lease, _ := svc.Acquire(t.Context(), taskID, stepID, "agent-a")
	clock.Advance(LeaseTTL)
	_, _ = svc.InterruptExpired(t.Context())
	if _, err := svc.Reassign(t.Context(), ReassignInput{TaskID: taskID, StepID: stepID, NewAgent: "agent-b"}, "manager"); !errors.Is(err, ErrConflict) {
		t.Fatalf("early reassign err=%v", err)
	}
	if _, err := svc.Reassign(t.Context(), ReassignInput{TaskID: taskID, StepID: stepID, NewAgent: "agent-b", Immediate: true}, "manager"); !errors.Is(err, ErrRecoveryReview) {
		t.Fatalf("missing checkpoint err=%v", err)
	}
	var status string
	_ = conn.QueryRow(`select status from task_steps where id=?`, stepID).Scan(&status)
	if status != "blocked" {
		t.Fatalf("step status=%s lease=%s", status, lease.LeaseID)
	}
}

func TestReassignBlocksBranchConflictAndInvalidBaseline(t *testing.T) {
	for _, scenario := range []string{"branch conflict", "invalid baseline"} {
		t.Run(scenario, func(t *testing.T) {
			svc, conn, clock, taskID, stepID := leaseFixture(t)
			repo, base := checkpointRepo(t)
			_, _ = conn.Exec(`update project_workspaces set worktree_path=?,base_commit=?,provision_commit=?,branch_name='agent/agent-a/lease',write_scope_json='["."]' where step_id=?`, repo, base, base, stepID)
			_, _ = conn.Exec(`update projects set dir=? where id=(select project_id from tasks where id=?)`, repo, taskID)
			_, _ = conn.Exec(`insert into agent_registry(name,role,dir,status) values('agent-b','backend','/tmp/agent-b','online')`)
			lease, _ := svc.Acquire(t.Context(), taskID, stepID, "agent-a")
			commit := commitCheckpoint(t, repo, stepID)
			input := validCheckpointInput(commit)
			input.BranchName = "agent/agent-a/lease"
			checkpoint, err := svc.CreateCheckpoint(t.Context(), lease.LeaseRef, input)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "branch conflict" {
				branch := "agent/agent-b/" + itoa64(taskID) + "-step-" + itoa64(stepID) + "-resume-2"
				mustCheckpointGit(t, repo, "branch", branch, commit)
			} else {
				_, _ = conn.Exec(`update task_checkpoints set git_commit='0000000000000000000000000000000000000000' where id=?`, checkpoint.ID)
			}
			clock.Advance(LeaseTTL)
			_, _ = svc.InterruptExpired(t.Context())
			_, err = svc.Reassign(t.Context(), ReassignInput{TaskID: taskID, StepID: stepID, NewAgent: "agent-b", CheckpointID: checkpoint.ID, Immediate: true}, "manager")
			if !errors.Is(err, ErrRecoveryReview) {
				t.Fatalf("scenario=%s err=%v", scenario, err)
			}
			var status, currentWorktree string
			_ = conn.QueryRow(`select status from task_steps where id=?`, stepID).Scan(&status)
			_ = conn.QueryRow(`select worktree_path from project_workspaces where step_id=?`, stepID).Scan(&currentWorktree)
			if status != "blocked" || currentWorktree != repo {
				t.Fatalf("status=%s currentWorktree=%s", status, currentWorktree)
			}
		})
	}
}
