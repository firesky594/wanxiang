package workspaces

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"wanxiang-agent/server/internal/gitx"
)

type stepControl struct {
	Status       string
	LeaseID      string
	LeaseVersion int64
	Attempt      int64
	PlanVersion  int64
	CheckpointID sql.NullInt64
}

type checkpointControl struct {
	Commit       string
	Branch       string
	LeaseID      string
	LeaseStatus  string
	LeaseVersion int64
	Clean        bool
}

func (s *Service) loadStepControl(ctx context.Context, taskID, stepID int64) (stepControl, error) {
	var result stepControl
	err := s.db.QueryRowContext(ctx, `select status,lease_id,lease_version,attempt,plan_version,checkpoint_id
		from task_steps where task_id=? and id=?`, taskID, stepID).
		Scan(&result.Status, &result.LeaseID, &result.LeaseVersion, &result.Attempt, &result.PlanVersion, &result.CheckpointID)
	return result, err
}

func (s *Service) controlledHeadReason(ctx context.Context, projectDir string, taskID int64, item WorkspaceItem, state stepControl, head string) string {
	baseline := item.ProvisionCommit
	if baseline == "" {
		baseline = item.BaseCommit
	}
	if baseline == "" {
		return "worktree_baseline_missing"
	}
	if out, err := gitx.Run(ctx, item.WorktreePath, "merge-base", "--is-ancestor", baseline, head); err != nil {
		_ = out
		return "worktree_head_not_provision_descendant"
	}
	if item.Status == "dependency_syncing" {
		if _, err := gitx.Run(ctx, projectDir, "merge-base", "--is-ancestor", head, "main"); err == nil {
			return ""
		}
		return "dependency_worktree_non_fast_forward"
	}
	if head == baseline {
		switch state.Status {
		case "assigned":
			if state.LeaseID == "" {
				return ""
			}
		case "in_progress":
			if s.currentLeaseStatusControlled(ctx, taskID, item, state, "active") {
				return ""
			}
		case "interrupted":
			if s.currentLeaseStatusControlled(ctx, taskID, item, state, "interrupted") {
				return ""
			}
		case "blocked":
			if s.currentLeaseStatusControlled(ctx, taskID, item, state, "frozen", "interrupted", "revoked") {
				return ""
			}
		}
	}

	checkpoint, err := s.loadCurrentCheckpointControl(ctx, taskID, item.StepID, item.AgentName, state)
	if err == nil && checkpoint.Clean && checkpoint.Commit == head && checkpoint.Branch == item.BranchName {
		if checkpoint.LeaseID != state.LeaseID || checkpoint.LeaseVersion != state.LeaseVersion {
			if checkpoint.Commit == head && checkpoint.Branch == item.BranchName &&
				s.inheritedFrozenCheckpointControlled(ctx, taskID, item, state, checkpoint) {
				return ""
			}
			return "worktree_checkpoint_lease_mismatch"
		}
		switch state.Status {
		case "in_progress", "checkpointed":
			if checkpoint.LeaseStatus == "active" {
				return ""
			}
		case "interrupted":
			if checkpoint.LeaseStatus == "interrupted" {
				return ""
			}
		case "blocked":
			if checkpoint.LeaseStatus == "frozen" || checkpoint.LeaseStatus == "interrupted" || checkpoint.LeaseStatus == "revoked" {
				return ""
			}
		case "review":
			if checkpoint.LeaseStatus == "review" && s.reviewCommitControlled(ctx, taskID, item.StepID, state.LeaseID, head) {
				return ""
			}
		case "completed":
			if checkpoint.LeaseStatus == "completed" && s.mergedCommitControlled(ctx, projectDir, taskID, item.StepID, state.LeaseID, head) {
				return ""
			}
		}
	}

	if state.Status == "interrupted" && state.CheckpointID.Valid {
		if s.priorCleanCheckpointControlled(ctx, taskID, item.StepID, state, head) {
			return ""
		}
	}
	if state.Status == "assigned" && state.LeaseID == "" && state.Attempt > 0 {
		if s.changesRequestedCommitControlled(ctx, taskID, item.StepID, state.LeaseVersion, item.BranchName, head) {
			return ""
		}
	}
	if state.Status == "in_progress" {
		if s.changesRequestedLeaseContinuationControlled(ctx, taskID, item, state, head) {
			return ""
		}
	}
	if state.Status == "in_progress" || state.Status == "checkpointed" {
		if s.transientActiveCommitControlled(ctx, taskID, item, state, head) {
			return ""
		}
	}
	return "worktree_head_uncontrolled"
}

func (s *Service) loadCurrentCheckpointControl(ctx context.Context, taskID, stepID int64, agent string, state stepControl) (checkpointControl, error) {
	if !state.CheckpointID.Valid {
		return checkpointControl{}, sql.ErrNoRows
	}
	var result checkpointControl
	var clean int
	err := s.db.QueryRowContext(ctx, `select cp.git_commit,cp.branch_name,cp.lease_id,cp.clean,l.status,l.lease_version
		from task_checkpoints cp
		join task_step_leases l on l.lease_id=cp.lease_id
			and l.task_id=cp.task_id and l.step_id=cp.step_id
		where cp.id=? and cp.task_id=? and cp.step_id=? and l.agent_name=?`,
		state.CheckpointID.Int64, taskID, stepID, agent).
		Scan(&result.Commit, &result.Branch, &result.LeaseID, &clean, &result.LeaseStatus, &result.LeaseVersion)
	result.Clean = clean == 1
	return result, err
}

func (s *Service) currentLeaseStatusControlled(ctx context.Context, taskID int64, item WorkspaceItem, state stepControl, statuses ...string) bool {
	if state.LeaseID == "" || len(statuses) == 0 {
		return false
	}
	var status string
	err := s.db.QueryRowContext(ctx, `select status from task_step_leases
		where task_id=? and step_id=? and agent_name=? and lease_id=? and lease_version=?`,
		taskID, item.StepID, item.AgentName, state.LeaseID, state.LeaseVersion).Scan(&status)
	if err != nil {
		return false
	}
	for _, allowed := range statuses {
		if status == allowed {
			return true
		}
	}
	return false
}

func (s *Service) priorCleanCheckpointControlled(ctx context.Context, taskID, stepID int64, state stepControl, head string) bool {
	var count int
	err := s.db.QueryRowContext(ctx, `select count(*)
		from task_checkpoints cp
		join task_step_leases l on l.lease_id=cp.lease_id
		where cp.task_id=? and cp.step_id=? and cp.lease_id=? and cp.id<?
			and cp.clean=1 and cp.git_commit=? and l.lease_version=? and l.status='interrupted'`,
		taskID, stepID, state.LeaseID, state.CheckpointID.Int64, head, state.LeaseVersion).Scan(&count)
	return err == nil && count == 1
}

func (s *Service) reviewCommitControlled(ctx context.Context, taskID, stepID int64, leaseID, head string) bool {
	var count int
	err := s.db.QueryRowContext(ctx, `select count(*) from merge_requests
		where task_id=? and step_id=? and lease_id=? and source_commit=?
			and status in ('pending_review','approved')`,
		taskID, stepID, leaseID, head).Scan(&count)
	return err == nil && count == 1
}

func (s *Service) mergedCommitControlled(ctx context.Context, projectDir string, taskID, stepID int64, leaseID, head string) bool {
	var mergeCommit string
	err := s.db.QueryRowContext(ctx, `select merge_commit from merge_requests
		where task_id=? and step_id=? and lease_id=? and source_commit=? and status='merged'
		order by id desc limit 1`,
		taskID, stepID, leaseID, head).Scan(&mergeCommit)
	if err != nil || strings.TrimSpace(mergeCommit) == "" {
		return false
	}
	if _, err = gitx.Run(ctx, projectDir, "merge-base", "--is-ancestor", head, mergeCommit); err != nil {
		return false
	}
	_, err = gitx.Run(ctx, projectDir, "merge-base", "--is-ancestor", mergeCommit, "main")
	return err == nil
}

func (s *Service) changesRequestedCommitControlled(ctx context.Context, taskID, stepID, leaseVersion int64, branch, head string) bool {
	var count int
	err := s.db.QueryRowContext(ctx, `select count(*)
		from merge_requests mr
		join completion_reports cr on cr.id=mr.report_id
		join task_step_leases l on l.lease_id=mr.lease_id
		where mr.task_id=? and mr.step_id=? and mr.status='changes_requested'
			and mr.source_branch=? and mr.source_commit=?
			and cr.checkpoint_commit=? and cr.lease_version=?
			and l.lease_version=? and l.status='revoked'`,
		taskID, stepID, branch, head, head, leaseVersion, leaseVersion).Scan(&count)
	return err == nil && count == 1
}

func (s *Service) changesRequestedLeaseContinuationControlled(ctx context.Context, taskID int64, item WorkspaceItem, state stepControl, head string) bool {
	if state.LeaseID == "" || state.LeaseVersion < 2 || state.Attempt < 2 || state.CheckpointID.Valid {
		return false
	}
	var count int
	err := s.db.QueryRowContext(ctx, `select count(*)
		from task_step_leases current
		join merge_requests mr on mr.task_id=current.task_id and mr.step_id=current.step_id
		join completion_reports cr on cr.id=mr.report_id and cr.lease_id=mr.lease_id
		join task_step_leases previous on previous.lease_id=mr.lease_id
		where current.task_id=? and current.step_id=? and current.agent_name=?
			and current.lease_id=? and current.lease_version=? and current.status='active'
			and mr.status='changes_requested' and mr.source_branch=? and mr.source_commit=?
			and cr.checkpoint_commit=? and cr.lease_version=?
			and previous.lease_version=? and previous.status='revoked'`,
		taskID, item.StepID, item.AgentName, state.LeaseID, state.LeaseVersion,
		item.BranchName, head, head, state.LeaseVersion-1, state.LeaseVersion-1).Scan(&count)
	return err == nil && count == 1
}

func (s *Service) transientActiveCommitControlled(ctx context.Context, taskID int64, item WorkspaceItem, state stepControl, head string) bool {
	if state.LeaseID == "" {
		return false
	}
	var count int
	err := s.db.QueryRowContext(ctx, `select count(*) from task_step_leases
		where task_id=? and step_id=? and agent_name=? and lease_id=? and lease_version=?
			and status='active' and expires_at>?`,
		taskID, item.StepID, item.AgentName, state.LeaseID, state.LeaseVersion, timestamp()).Scan(&count)
	if err != nil || count != 1 {
		return false
	}
	// Git 提交先于检查点入库；仅给有效活动租约的近期提交留出短暂原子窗口。
	raw, err := gitx.Run(ctx, item.WorktreePath, "show", "-s", "--format=%cI", head)
	if err != nil {
		return false
	}
	committedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	now := time.Now().UTC()
	return !committedAt.After(now.Add(5*time.Minute)) && !committedAt.Before(now.Add(-2*time.Minute))
}

func (s *Service) inheritedFrozenCheckpointControlled(ctx context.Context, taskID int64, item WorkspaceItem, state stepControl, checkpoint checkpointControl) bool {
	if state.Status != "in_progress" || state.LeaseID == "" || state.LeaseVersion != checkpoint.LeaseVersion+1 || checkpoint.LeaseStatus != "frozen" {
		return false
	}
	var count int
	err := s.db.QueryRowContext(ctx, `select count(*)
		from task_step_leases current
		join task_step_leases previous on previous.task_id=current.task_id and previous.step_id=current.step_id
		where current.task_id=? and current.step_id=? and current.agent_name=?
			and current.lease_id=? and current.lease_version=? and current.status='active'
			and previous.lease_id=? and previous.lease_version=? and previous.status='frozen'`,
		taskID, item.StepID, item.AgentName, state.LeaseID, state.LeaseVersion,
		checkpoint.LeaseID, checkpoint.LeaseVersion).Scan(&count)
	return err == nil && count == 1
}

func (s *Service) pendingHandoffSnapshotControlled(ctx context.Context, taskID int64, item WorkspaceItem, metadata AssignmentMetadata) bool {
	if metadata.TaskID != taskID || metadata.StepID != item.StepID || metadata.AssignmentID != item.AssignmentID ||
		metadata.ReportsTo != item.ReportsTo || metadata.Status != "ready" ||
		metadata.WorktreeID != fmt.Sprintf("task-%d-step-%d", taskID, item.StepID) ||
		!sameScopes(metadata.WriteScope, item.WriteScope) {
		return false
	}
	var count int
	err := s.db.QueryRowContext(ctx, `select count(*)
		from step_reassignments sr
		join task_checkpoints cp on cp.id=sr.checkpoint_id and cp.task_id=sr.task_id and cp.step_id=sr.step_id
		where sr.task_id=? and sr.step_id=? and sr.status='preparing'
			and sr.from_agent=? and sr.from_branch=? and sr.from_worktree=?
			and sr.to_agent=? and sr.to_branch=? and cp.git_commit=?`,
		taskID, item.StepID, item.AgentName, item.BranchName, item.WorktreePath,
		metadata.AgentName, metadata.BranchName, metadata.BaseCommit).Scan(&count)
	return err == nil && count == 1
}

func sameScopes(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
