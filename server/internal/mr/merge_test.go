package mr

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wanxiang-agent/server/internal/gitx"
	"wanxiang-agent/server/internal/issues"
)

func TestMergeRequiresApprovalAndProjectLead(t *testing.T) {
	fixture := newReportFixture(t)
	created, err := fixture.service.SubmitReport(t.Context(), Principal{Name: "worker", Role: "worker"}, fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	input := MergeInput{AgentName: "lead", Role: "project_lead"}
	if _, err := fixture.service.Merge(t.Context(), Principal{Name: "lead", Role: "project_lead"}, created.MergeRequest.ID, input); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("unapproved err=%v", err)
	}
	if _, err := fixture.service.Review(t.Context(), Principal{Name: "lead", Role: "project_lead"}, created.MergeRequest.ID, ReviewInput{AgentName: "lead", Role: "project_lead", Status: MRApproved}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Merge(t.Context(), Principal{Name: "worker", Role: "worker"}, created.MergeRequest.ID, MergeInput{AgentName: "worker", Role: "worker"}); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("worker err=%v", err)
	}
}

func TestMergeCreatesNoFFCommitManagerNotificationAndEvent(t *testing.T) {
	fixture := newReportFixture(t)
	created, err := fixture.service.SubmitReport(t.Context(), Principal{Name: "worker", Role: "worker"}, fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Review(t.Context(), Principal{Name: "lead", Role: "project_lead"}, created.MergeRequest.ID, ReviewInput{AgentName: "lead", Role: "project_lead", Status: MRApproved}); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.service.Merge(t.Context(), Principal{Name: "lead", Role: "project_lead"}, created.MergeRequest.ID, MergeInput{AgentName: "lead", Role: "project_lead"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != MRMerged || result.MergeCommit == "" {
		t.Fatalf("result=%+v", result)
	}
	parents, err := gitx.Run(t.Context(), fixture.service.cfg.ProjectDir+"/report-project", "show", "-s", "--format=%P", result.MergeCommit)
	if err != nil || len(strings.Fields(parents)) != 2 {
		t.Fatalf("parents=%q err=%v", parents, err)
	}
	var notifications, events, completed int
	_ = fixture.db.QueryRow(`select count(*) from manager_notifications where mr_id=? and main_commit=?`, created.MergeRequest.ID, result.MergeCommit).Scan(&notifications)
	_ = fixture.db.QueryRow(`select count(*) from runtime_events where event_type='mr.merged' and task_id=?`, fixture.input.TaskID).Scan(&events)
	_ = fixture.db.QueryRow(`select count(*) from task_steps where id=? and status='completed'`, fixture.input.StepID).Scan(&completed)
	if notifications != 1 || events != 1 || completed != 1 {
		t.Fatalf("notification=%d events=%d completed=%d", notifications, events, completed)
	}
	if _, err := fixture.service.Merge(t.Context(), Principal{Name: "lead", Role: "project_lead"}, created.MergeRequest.ID, MergeInput{AgentName: "lead", Role: "project_lead"}); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("repeat err=%v", err)
	}
}

func TestMergeRejectsBlockingIssue(t *testing.T) {
	fixture := newReportFixture(t)
	created, err := fixture.service.SubmitReport(t.Context(), Principal{Name: "worker", Role: "worker"}, fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Review(t.Context(), Principal{Name: "lead", Role: "project_lead"}, created.MergeRequest.ID, ReviewInput{AgentName: "lead", Role: "project_lead", Status: MRApproved}); err != nil {
		t.Fatal(err)
	}
	taskID := fixture.input.TaskID
	issueService := issues.NewService(fixture.db)
	if _, err := issueService.Create(t.Context(), issues.CreateIssueInput{TaskID: &taskID, MRID: created.MergeRequest.ID, Title: "阻塞", Body: "等待用户", Blocking: true, CreatedBy: "admin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Merge(t.Context(), Principal{Name: "lead", Role: "project_lead"}, created.MergeRequest.ID, MergeInput{AgentName: "lead", Role: "project_lead"}); !errors.Is(err, ErrMergeBlocked) {
		t.Fatalf("err=%v", err)
	}
}

func TestReconcileMergePersistsExistingGitMergeWithoutMergingAgain(t *testing.T) {
	fixture := newReportFixture(t)
	created, err := fixture.service.SubmitReport(t.Context(), Principal{Name: "worker", Role: "worker"}, fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Review(t.Context(), Principal{Name: "lead", Role: "project_lead"}, created.MergeRequest.ID, ReviewInput{AgentName: "lead", Role: "project_lead", Status: MRApproved}); err != nil {
		t.Fatal(err)
	}
	repo := fixture.service.cfg.ProjectDir + "/report-project"
	mustReportGit(t, repo, "checkout", "main")
	mustReportGit(t, repo, "merge", "--no-ff", "--no-edit", fixture.input.SourceBranch)
	mainBefore := strings.TrimSpace(mustReportGit(t, repo, "rev-parse", "HEAD"))
	result, err := fixture.service.ReconcileMerge(t.Context(), created.MergeRequest.ID)
	if err != nil {
		t.Fatal(err)
	}
	mainAfter := strings.TrimSpace(mustReportGit(t, repo, "rev-parse", "HEAD"))
	if result.MergeCommit != mainBefore || mainAfter != mainBefore {
		t.Fatalf("result=%+v before=%s after=%s", result, mainBefore, mainAfter)
	}
}

func TestMergeConflictAbortsAndKeepsApproved(t *testing.T) {
	fixture := newReportFixture(t)
	repo := fixture.service.cfg.ProjectDir + "/report-project"
	conflict := filepath.Join(repo, "conflict.txt")
	if err := os.WriteFile(conflict, []byte("branch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustReportGit(t, repo, "add", "conflict.txt")
	mustReportGit(t, repo, "commit", "-m", "分支冲突")
	source := strings.TrimSpace(mustReportGit(t, repo, "rev-parse", "HEAD"))
	fixture.input.HeadCommit, fixture.input.CheckpointCommit = source, source
	_, _ = fixture.db.Exec(`update task_checkpoints set git_commit=? where lease_id=?`, source, fixture.input.LeaseID)
	mustReportGit(t, repo, "checkout", "main")
	if err := os.WriteFile(conflict, []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustReportGit(t, repo, "add", "conflict.txt")
	mustReportGit(t, repo, "commit", "-m", "主线冲突")
	mustReportGit(t, repo, "checkout", fixture.input.SourceBranch)
	created, err := fixture.service.SubmitReport(t.Context(), Principal{Name: "worker", Role: "worker"}, fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Review(t.Context(), Principal{Name: "lead", Role: "project_lead"}, created.MergeRequest.ID, ReviewInput{AgentName: "lead", Role: "project_lead", Status: MRApproved}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Merge(t.Context(), Principal{Name: "lead", Role: "project_lead"}, created.MergeRequest.ID, MergeInput{AgentName: "lead", Role: "project_lead"}); err == nil {
		t.Fatal("conflicting merge succeeded")
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "MERGE_HEAD")); !os.IsNotExist(err) {
		t.Fatalf("MERGE_HEAD remains err=%v", err)
	}
	var status string
	if err := fixture.db.QueryRow(`select status from merge_requests where id=?`, created.MergeRequest.ID).Scan(&status); err != nil || status != MRApproved {
		t.Fatalf("status=%s err=%v", status, err)
	}
}
