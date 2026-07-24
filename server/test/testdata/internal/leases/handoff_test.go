package leases

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/gitx"
	"wanxiang-agent/server/internal/workspaces"
)

func TestReassignCreatesIndependentWorktreeFromCleanCheckpoint(t *testing.T) {
	svc, conn, clock, taskID, stepID := leaseFixture(t)
	repo, base := checkpointRepo(t)
	original := repo
	_, _ = conn.Exec(`update project_workspaces set worktree_path=?,base_commit=?,provision_commit=?,branch_name='agent/agent-a/lease',write_scope_json='["."]' where step_id=?`, original, base, base, stepID)
	_, _ = conn.Exec(`update projects set dir=? where id=(select project_id from tasks where id=?)`, repo, taskID)
	registerHandoffAgent(t, conn, "agent-b", "lease-demo")
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
	registerHandoffAgent(t, conn, "agent-b", "lease-demo")
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
			registerHandoffAgent(t, conn, "agent-b", "lease-demo")
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
			var status, taskStatus, currentWorktree string
			_ = conn.QueryRow(`select status from task_steps where id=?`, stepID).Scan(&status)
			_ = conn.QueryRow(`select status from tasks where id=?`, taskID).Scan(&taskStatus)
			_ = conn.QueryRow(`select worktree_path from project_workspaces where step_id=?`, stepID).Scan(&currentWorktree)
			if status != "blocked" || taskStatus != "workspace_ready" || currentWorktree != repo {
				t.Fatalf("status=%s taskStatus=%s currentWorktree=%s", status, taskStatus, currentWorktree)
			}
		})
	}
}

func TestReassignUsesOnlyCheckpointBoundToCurrentLease(t *testing.T) {
	svc, conn, clock, _, taskID, steps := realHandoffFixture(t, []handoffMember{
		{Agent: "agent-a", Key: "selected"},
	}, "")
	stepID := steps[0]
	registerHandoffAgent(t, conn, "agent-b", "lease-demo")
	registerHandoffAgent(t, conn, "agent-c", "lease-demo")

	firstLease, firstCheckpoint := handoffCheckpoint(t, svc, conn, taskID, stepID, "agent-a", "first")
	interruptForHandoff(t, svc, clock)
	secondLease, err := svc.Reassign(t.Context(), ReassignInput{
		TaskID: taskID, StepID: stepID, NewAgent: "agent-b", CheckpointID: firstCheckpoint.ID, Immediate: true,
	}, "manager")
	if err != nil {
		t.Fatal(err)
	}
	if secondLease.LeaseID == firstLease.LeaseID {
		t.Fatal("first handoff reused old lease")
	}
	_, secondCheckpoint := handoffCheckpointForLease(t, svc, conn, secondLease, "second")
	interruptForHandoff(t, svc, clock)

	if _, err := svc.Reassign(t.Context(), ReassignInput{
		TaskID: taskID, StepID: stepID, NewAgent: "agent-c", CheckpointID: firstCheckpoint.ID, Immediate: true,
	}, "manager"); !errors.Is(err, ErrRecoveryReview) {
		t.Fatalf("stale lease checkpoint err=%v", err)
	}
	thirdLease, err := svc.Reassign(t.Context(), ReassignInput{
		TaskID: taskID, StepID: stepID, NewAgent: "agent-c", Immediate: true,
	}, "manager")
	if err != nil {
		t.Fatal(err)
	}
	if thirdLease.LeaseID == secondLease.LeaseID {
		t.Fatal("second handoff reused interrupted lease")
	}
	var worktree string
	if err := conn.QueryRow(`select worktree_path from project_workspaces where task_id=? and step_id=?`, taskID, stepID).Scan(&worktree); err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(mustCheckpointGit(t, worktree, "rev-parse", "HEAD"))
	if head != secondCheckpoint.GitCommit {
		t.Fatalf("handoff head=%s want current lease checkpoint=%s", head, secondCheckpoint.GitCommit)
	}
}

func TestReassignMigratesProjectLeadHierarchyAndSnapshots(t *testing.T) {
	svc, conn, clock, workspaceSvc, taskID, steps := realHandoffFixture(t, []handoffMember{
		{Agent: "agent-a", Key: "selected"},
		{Agent: "agent-a", Key: "old-lead-extra"},
		{Agent: "agent-b", ReportsTo: "agent-a", Key: "new-lead-existing"},
		{Agent: "agent-c", ReportsTo: "agent-a", Key: "worker"},
	}, "agent-a")
	selectedStep := steps[0]
	projectID := projectIDForTask(t, conn, taskID)
	if _, err := conn.Exec(`insert into merge_requests(
			project_id,task_id,step_id,title,source_branch,target_branch,status,project_lead,created_by,created_at
		) values(?,?,?,'pending','agent/agent-c/pending','main','pending_review','agent-a','agent-c','now')`,
		projectID, taskID, steps[3]); err != nil {
		t.Fatal(err)
	}
	_, checkpoint := handoffCheckpoint(t, svc, conn, taskID, selectedStep, "agent-a", "lead")
	interruptForHandoff(t, svc, clock)
	if _, err := svc.Reassign(t.Context(), ReassignInput{
		TaskID: taskID, StepID: selectedStep, NewAgent: "agent-b", CheckpointID: checkpoint.ID, Immediate: true,
	}, "manager"); err != nil {
		t.Fatal(err)
	}

	var lead, pendingLead string
	if err := conn.QueryRow(`select project_lead from team_decisions where task_id=? and plan_version=1`, taskID).Scan(&lead); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(`select project_lead from merge_requests where task_id=?`, taskID).Scan(&pendingLead); err != nil {
		t.Fatal(err)
	}
	if lead != "agent-b" || pendingLead != "agent-b" {
		t.Fatalf("lead=%q pending MR lead=%q", lead, pendingLead)
	}
	want := map[int64]struct {
		Agent, ReportsTo string
	}{
		steps[0]: {"agent-b", ""},
		steps[1]: {"agent-a", "agent-b"},
		steps[2]: {"agent-b", ""},
		steps[3]: {"agent-c", "agent-b"},
	}
	var projectDir string
	if err := conn.QueryRow(`select dir from projects where id=?`, projectID).Scan(&projectDir); err != nil {
		t.Fatal(err)
	}
	for stepID, expected := range want {
		var assignmentAgent, assignmentReports, workspaceAgent, workspaceReports, metadataHash string
		if err := conn.QueryRow(`select agent_name,coalesce(reports_to,'') from task_assignments where task_id=? and step_id=?`, taskID, stepID).
			Scan(&assignmentAgent, &assignmentReports); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRow(`select agent_name,coalesce(reports_to,''),metadata_hash from project_workspaces where task_id=? and step_id=?`, taskID, stepID).
			Scan(&workspaceAgent, &workspaceReports, &metadataHash); err != nil {
			t.Fatal(err)
		}
		if assignmentAgent != expected.Agent || workspaceAgent != expected.Agent ||
			assignmentReports != expected.ReportsTo || workspaceReports != expected.ReportsTo {
			t.Fatalf("step %d assignment=%s/%s workspace=%s/%s want=%s/%s", stepID,
				assignmentAgent, assignmentReports, workspaceAgent, workspaceReports, expected.Agent, expected.ReportsTo)
		}
		content, err := os.ReadFile(filepath.Join(projectDir, ".wanxiang", "assignments", itoa64(taskID)+"-"+itoa64(stepID)+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		metadata, err := workspaces.DecodeAssignment(content)
		if err != nil {
			t.Fatal(err)
		}
		if metadata.AgentName != expected.Agent || metadata.ReportsTo != expected.ReportsTo {
			t.Fatalf("step %d snapshot=%s/%s", stepID, metadata.AgentName, metadata.ReportsTo)
		}
		if hashContent(content) != metadataHash {
			t.Fatalf("step %d metadata hash mismatch", stepID)
		}
	}
	projectMetadata, err := os.ReadFile(filepath.Join(projectDir, ".wanxiang", "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(projectMetadata), `project_lead: "agent-b"`) {
		t.Fatalf("project metadata=%s", projectMetadata)
	}
	reconciled, err := workspaceSvc.ReconcileTask(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.Status != "ready" {
		t.Fatalf("reconciled=%+v", reconciled)
	}
}

func TestReassignBlocksProjectLeadTakeoverWithApprovedMR(t *testing.T) {
	svc, conn, clock, _, taskID, steps := realHandoffFixture(t, []handoffMember{
		{Agent: "agent-a", Key: "selected"},
	}, "agent-a")
	registerHandoffAgent(t, conn, "agent-b", "lease-demo")
	projectID := projectIDForTask(t, conn, taskID)
	if _, err := conn.Exec(`insert into merge_requests(
			project_id,task_id,step_id,title,source_branch,target_branch,status,project_lead,created_by,created_at
		) values(?,?,?,'approved','agent/agent-a/approved','main','approved','agent-a','agent-a','now')`,
		projectID, taskID, steps[0]); err != nil {
		t.Fatal(err)
	}
	_, checkpoint := handoffCheckpoint(t, svc, conn, taskID, steps[0], "agent-a", "approved")
	interruptForHandoff(t, svc, clock)
	if _, err := svc.Reassign(t.Context(), ReassignInput{
		TaskID: taskID, StepID: steps[0], NewAgent: "agent-b", CheckpointID: checkpoint.ID, Immediate: true,
	}, "manager"); !errors.Is(err, ErrRecoveryReview) {
		t.Fatalf("approved MR handoff err=%v", err)
	}
	var lead string
	if err := conn.QueryRow(`select project_lead from team_decisions where task_id=?`, taskID).Scan(&lead); err != nil {
		t.Fatal(err)
	}
	if lead != "agent-a" {
		t.Fatalf("project lead changed to %q", lead)
	}
}

func TestReassignWaitsForSharedProjectLock(t *testing.T) {
	svc, conn, clock, _, taskID, steps := realHandoffFixture(t, []handoffMember{
		{Agent: "agent-a", Key: "selected"},
	}, "")
	registerHandoffAgent(t, conn, "agent-b", "lease-demo")
	_, checkpoint := handoffCheckpoint(t, svc, conn, taskID, steps[0], "agent-a", "locked")
	interruptForHandoff(t, svc, clock)
	release, err := gitx.AcquireProjectLock(t.Context(), svc.dataDir, projectIDForTask(t, conn, taskID))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 75*time.Millisecond)
	defer cancel()
	if _, err := svc.Reassign(ctx, ReassignInput{
		TaskID: taskID, StepID: steps[0], NewAgent: "agent-b", CheckpointID: checkpoint.ID, Immediate: true,
	}, "manager"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("locked handoff err=%v", err)
	}
	var preparations int
	if err := conn.QueryRow(`select count(*) from step_reassignments where task_id=? and status='preparing'`, taskID).Scan(&preparations); err != nil {
		t.Fatal(err)
	}
	if preparations != 0 {
		t.Fatalf("preparations=%d", preparations)
	}
}

func TestReassignRestoresTaskAndGitAfterDefiniteDatabaseFailure(t *testing.T) {
	svc, conn, clock, _, taskID, steps := realHandoffFixture(t, []handoffMember{
		{Agent: "agent-a", Key: "selected"},
	}, "")
	registerHandoffAgent(t, conn, "agent-b", "lease-demo")
	_, checkpoint := handoffCheckpoint(t, svc, conn, taskID, steps[0], "agent-a", "database-failure")
	interruptForHandoff(t, svc, clock)
	var projectDir string
	if err := conn.QueryRow(`select dir from projects where id=?`, projectIDForTask(t, conn, taskID)).Scan(&projectDir); err != nil {
		t.Fatal(err)
	}
	originalHead := strings.TrimSpace(mustCheckpointGit(t, projectDir, "rev-parse", "HEAD"))
	if _, err := conn.Exec(`create trigger fail_reassign before update of status on task_step_leases
		when new.status='revoked' begin select raise(abort,'forced reassign failure'); end`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Reassign(t.Context(), ReassignInput{
		TaskID: taskID, StepID: steps[0], NewAgent: "agent-b", CheckpointID: checkpoint.ID, Immediate: true,
	}, "manager"); err == nil {
		t.Fatal("expected forced database failure")
	}
	var taskStatus string
	if err := conn.QueryRow(`select status from tasks where id=?`, taskID).Scan(&taskStatus); err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(mustCheckpointGit(t, projectDir, "rev-parse", "HEAD"))
	status := strings.TrimSpace(mustCheckpointGit(t, projectDir, "status", "--porcelain", "--untracked-files=all"))
	if taskStatus != "workspace_ready" || head != originalHead || status != "" {
		t.Fatalf("taskStatus=%s head=%s want=%s gitStatus=%q", taskStatus, head, originalHead, status)
	}
}

func TestPrepareHandoffSnapshotsPreflightsBeforeWriting(t *testing.T) {
	repo := t.TempDir()
	mustCheckpointGit(t, repo, "init", "-b", "main")
	mustCheckpointGit(t, repo, "config", "user.name", "Test")
	mustCheckpointGit(t, repo, "config", "user.email", "test@example.com")
	for name, content := range map[string]string{"first.txt": "first-old\n", "second.txt": "second-old\n"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustCheckpointGit(t, repo, "add", ".")
	mustCheckpointGit(t, repo, "commit", "-m", "base")
	head := strings.TrimSpace(mustCheckpointGit(t, repo, "rev-parse", "HEAD"))
	firstOld, _ := os.ReadFile(filepath.Join(repo, "first.txt"))
	_, err := prepareHandoffSnapshots(t.Context(), repo, 1, []handoffMetadataChange{
		{Relative: "first.txt", OldHash: hashContent(firstOld), Content: []byte("first-new\n")},
		{Relative: "second.txt", OldHash: strings.Repeat("0", 64), Content: []byte("second-new\n")},
	})
	if !errors.Is(err, ErrRecoveryReview) {
		t.Fatalf("preflight err=%v", err)
	}
	currentHead := strings.TrimSpace(mustCheckpointGit(t, repo, "rev-parse", "HEAD"))
	status := strings.TrimSpace(mustCheckpointGit(t, repo, "status", "--porcelain", "--untracked-files=all"))
	firstCurrent, _ := os.ReadFile(filepath.Join(repo, "first.txt"))
	if currentHead != head || status != "" || string(firstCurrent) != string(firstOld) {
		t.Fatalf("head=%s status=%q first=%q", currentHead, status, firstCurrent)
	}
}

type handoffMember struct {
	Agent     string
	ReportsTo string
	Key       string
}

func realHandoffFixture(t *testing.T, members []handoffMember, lead string) (*Service, *sql.DB, *FakeClock, *workspaces.Service, int64, []int64) {
	t.Helper()
	if len(members) == 0 {
		t.Fatal("handoff fixture requires members")
	}
	svc, conn, clock, taskID, firstStepID := leaseFixture(t)
	cfg, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.ProjectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	projectDir := filepath.Join(cfg.ProjectDir, "lease-demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustCheckpointGit(t, projectDir, "init", "-b", "main")
	mustCheckpointGit(t, projectDir, "config", "user.name", "Test")
	mustCheckpointGit(t, projectDir, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(projectDir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustCheckpointGit(t, projectDir, "add", "README.md")
	mustCheckpointGit(t, projectDir, "commit", "-m", "初始化")
	base := strings.TrimSpace(mustCheckpointGit(t, projectDir, "rev-parse", "HEAD"))

	projectID := projectIDForTask(t, conn, taskID)
	if _, err := conn.Exec(`delete from project_workspaces where task_id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`delete from task_assignments where task_id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`delete from team_decisions where task_id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`update projects set dir=?,main_commit=? where id=?`, projectDir, base, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`update tasks set status='assigned' where id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	stepIDs := make([]int64, 0, len(members))
	for index, member := range members {
		var stepID int64
		input := `{"key":"` + member.Key + `","title":"work","kind":"backend"}`
		if index == 0 {
			stepID = firstStepID
			if _, err := conn.Exec(`update task_steps set agent_name=?,kind='backend',status='assigned',input=?,lease_id='',lease_version=0,checkpoint_id=null,attempt=0,plan_version=1 where id=?`,
				member.Agent, input, stepID); err != nil {
				t.Fatal(err)
			}
		} else {
			result, err := conn.Exec(`insert into task_steps(task_id,agent_name,kind,status,input,plan_version,created_at)
				values(?,?,'backend','assigned',?,1,'now')`, taskID, member.Agent, input)
			if err != nil {
				t.Fatal(err)
			}
			stepID, _ = result.LastInsertId()
		}
		if _, err := conn.Exec(`insert into task_assignments(task_id,step_id,agent_name,reports_to,status,decision_id,created_at)
			values(?,?,?,?, 'assigned',?,'now')`, taskID, stepID, member.Agent, nullableReportsTo(member.ReportsTo), index+1); err != nil {
			t.Fatal(err)
		}
		stepIDs = append(stepIDs, stepID)
		registerHandoffAgent(t, conn, member.Agent, "lease-demo")
	}
	if lead != "" {
		if _, err := conn.Exec(`insert into team_decisions(task_id,plan_version,project_lead,requires_lead,reason,created_at)
			values(?,1,?,1,'handoff-test','now')`, taskID, lead); err != nil {
			t.Fatal(err)
		}
	}
	workspaceSvc := workspaces.NewService(cfg, conn, nil)
	if _, err := workspaceSvc.ProvisionTask(t.Context(), taskID); err != nil {
		t.Fatal(err)
	}
	svc.dataDir = cfg.DataDir
	svc.workspaces = workspaceSvc
	return svc, conn, clock, workspaceSvc, taskID, stepIDs
}

func handoffCheckpoint(t *testing.T, svc *Service, conn *sql.DB, taskID, stepID int64, agent, key string) (Lease, Checkpoint) {
	t.Helper()
	lease, err := svc.Acquire(t.Context(), taskID, stepID, agent)
	if err != nil {
		t.Fatal(err)
	}
	return handoffCheckpointForLease(t, svc, conn, lease, key)
}

func handoffCheckpointForLease(t *testing.T, svc *Service, conn *sql.DB, lease Lease, key string) (Lease, Checkpoint) {
	t.Helper()
	var worktree, branch string
	if err := conn.QueryRow(`select worktree_path,branch_name from project_workspaces where task_id=? and step_id=?`,
		lease.TaskID, lease.StepID).Scan(&worktree, &branch); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(worktree, key+".go")
	if err := os.WriteFile(path, []byte("package work\n\n// "+key+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustCheckpointGit(t, worktree, "add", "--", key+".go")
	mustCheckpointGit(t, worktree, "commit", "-m", "checkpoint: "+key)
	commit := strings.TrimSpace(mustCheckpointGit(t, worktree, "rev-parse", "HEAD"))
	input := validCheckpointInput(commit)
	input.IdempotencyKey = "checkpoint-" + key
	input.BranchName = branch
	input.Files = []string{key + ".go"}
	input.Summary.FilesChanged = []string{key + ".go"}
	checkpoint, err := svc.CreateCheckpoint(t.Context(), lease.LeaseRef, input)
	if err != nil {
		t.Fatal(err)
	}
	return lease, checkpoint
}

func interruptForHandoff(t *testing.T, svc *Service, clock *FakeClock) {
	t.Helper()
	clock.Advance(LeaseTTL)
	if count, err := svc.InterruptExpired(t.Context()); err != nil || count != 1 {
		t.Fatalf("interrupt count=%d err=%v", count, err)
	}
	clock.Advance(ResumeWindow)
}

func projectIDForTask(t *testing.T, conn *sql.DB, taskID int64) int64 {
	t.Helper()
	var projectID int64
	if err := conn.QueryRow(`select project_id from tasks where id=?`, taskID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	return projectID
}

func hashContent(content []byte) string {
	value := sha256.Sum256(content)
	return hex.EncodeToString(value[:])
}

func registerHandoffAgent(t *testing.T, conn *sql.DB, name, project string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	definition := "role: backend\nmax_concurrency: 4\nproject_access:\n  - " + project + "\n"
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(definition), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`insert into agent_registry(name,role,dir,status) values(?, 'backend', ?, 'online')
		on conflict(name) do update set dir=excluded.dir,status=excluded.status`, name, dir); err != nil {
		t.Fatal(err)
	}
}
