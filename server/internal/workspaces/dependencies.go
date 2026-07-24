package workspaces

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"wanxiang-agent/server/internal/gitx"
)

type dependencySyncResult struct {
	Item        WorkspaceItem
	State       stepControl
	Waiting     bool
	DriftReason string
}

type dependencyMerge struct {
	SourceCommit string
	MergeCommit  string
}

func (s *Service) syncDependenciesBeforeFirstLease(ctx context.Context, projectDir string, taskID int64, item WorkspaceItem, state stepControl) (dependencySyncResult, error) {
	result := dependencySyncResult{Item: item, State: state}
	if item.Status != "waiting_dependencies" && item.Status != "dependency_syncing" {
		return result, nil
	}
	if state.Status != "assigned" || state.LeaseID != "" || state.LeaseVersion != 0 || state.Attempt != 0 || state.CheckpointID.Valid {
		result.DriftReason = "dependency_step_already_started"
		return result, nil
	}

	branch, err := runTrim(ctx, item.WorktreePath, "branch", "--show-current")
	if err != nil || branch != item.BranchName {
		result.DriftReason = "worktree_branch_mismatch"
		return result, nil
	}
	worktreeStatus, err := runTrim(ctx, item.WorktreePath, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return result, err
	}
	if worktreeStatus != "" {
		result.DriftReason = "dependency_worktree_dirty"
		return result, nil
	}
	head, err := runTrim(ctx, item.WorktreePath, "rev-parse", "HEAD")
	if err != nil {
		return result, err
	}
	if item.ProvisionCommit == "" {
		result.DriftReason = "dependency_sync_baseline_missing"
		return result, nil
	}
	if item.Status == "dependency_syncing" {
		if _, ancestorErr := gitx.Run(ctx, item.WorktreePath, "merge-base", "--is-ancestor", item.ProvisionCommit, head); ancestorErr != nil {
			result.DriftReason = "dependency_worktree_uncontrolled_head"
			return result, nil
		}
	} else if head != item.ProvisionCommit {
		result.DriftReason = "dependency_worktree_uncontrolled_head"
		return result, nil
	}

	dependencyMerges, ready, err := s.mergedDependencyCommits(ctx, taskID, item.StepID, state.PlanVersion)
	if err != nil {
		return result, err
	}
	if !ready {
		result.Waiting = true
		return result, nil
	}

	mainBranch, err := runTrim(ctx, projectDir, "branch", "--show-current")
	if err != nil || mainBranch != "main" {
		result.Waiting = true
		return result, nil
	}
	mainStatus, err := runTrim(ctx, projectDir, "status", "--porcelain", "--untracked-files=all")
	if err != nil || mainStatus != "" {
		result.Waiting = true
		return result, nil
	}
	mainCommit, err := runTrim(ctx, projectDir, "rev-parse", "main")
	if err != nil {
		return result, err
	}
	for _, dependency := range dependencyMerges {
		if _, ancestorErr := gitx.Run(ctx, projectDir, "merge-base", "--is-ancestor", dependency.SourceCommit, dependency.MergeCommit); ancestorErr != nil {
			result.Waiting = true
			return result, nil
		}
		if _, ancestorErr := gitx.Run(ctx, projectDir, "merge-base", "--is-ancestor", dependency.MergeCommit, mainCommit); ancestorErr != nil {
			result.Waiting = true
			return result, nil
		}
	}

	if item.Status == "dependency_syncing" {
		if _, ancestorErr := gitx.Run(ctx, item.WorktreePath, "merge-base", "--is-ancestor", head, mainCommit); ancestorErr != nil {
			result.DriftReason = "dependency_worktree_non_fast_forward"
			return result, nil
		}
	} else {
		if _, ancestorErr := gitx.Run(ctx, item.WorktreePath, "merge-base", "--is-ancestor", head, mainCommit); ancestorErr != nil {
			result.DriftReason = "dependency_worktree_non_fast_forward"
			return result, nil
		}
		update, updateErr := s.db.ExecContext(ctx, `update project_workspaces
			set status='dependency_syncing',last_error='',updated_at=?
			where id=? and status='waiting_dependencies' and provision_commit=?`,
			timestamp(), item.ID, item.ProvisionCommit)
		if updateErr != nil {
			return result, updateErr
		}
		changed, _ := update.RowsAffected()
		if changed != 1 {
			return result, errors.New("dependency workspace changed concurrently")
		}
		item.Status = "dependency_syncing"
		result.Item = item
	}

	if head != mainCommit {
		if out, mergeErr := gitx.Run(ctx, item.WorktreePath, "merge", "--ff-only", mainCommit); mergeErr != nil {
			_, _ = s.db.ExecContext(ctx, `update project_workspaces set last_error=?,updated_at=? where id=? and status='dependency_syncing'`,
				trimWorkspaceError(fmt.Sprintf("dependency fast-forward failed: %s", strings.TrimSpace(out))), timestamp(), item.ID)
			return result, fmt.Errorf("fast-forward dependency workspace: %w", mergeErr)
		}
	}
	verifiedHead, err := runTrim(ctx, item.WorktreePath, "rev-parse", "HEAD")
	if err != nil {
		return result, err
	}
	if verifiedHead != mainCommit {
		result.DriftReason = "dependency_sync_head_mismatch"
		return result, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	update, err := tx.ExecContext(ctx, `update project_workspaces
		set status='ready',provision_commit=?,last_error='',updated_at=?
		where id=? and status='dependency_syncing' and provision_commit=?`,
		mainCommit, timestamp(), item.ID, item.ProvisionCommit)
	if err != nil {
		return result, err
	}
	if changed, _ := update.RowsAffected(); changed != 1 {
		return result, errors.New("dependency workspace finalization conflict")
	}
	if _, err = tx.ExecContext(ctx, `update projects set main_commit=?
		where id=(select project_id from project_workspaces where id=?)`, mainCommit, item.ID); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}

	item.Status = "ready"
	item.ProvisionCommit = mainCommit
	result.Item = item
	result.State = state
	return result, nil
}

func (s *Service) markDependentWorkspacesWaiting(ctx context.Context, taskID int64) error {
	_, err := s.db.ExecContext(ctx, `update project_workspaces
		set status='waiting_dependencies',last_error='',updated_at=?
		where task_id=? and status='ready'
			and exists(
				select 1
				from task_steps ts
				join workflow_edges e on e.task_id=ts.task_id and e.to_step_id=ts.id and e.plan_version=ts.plan_version
				where ts.task_id=project_workspaces.task_id and ts.id=project_workspaces.step_id
					and ts.status='assigned' and ts.lease_id='' and ts.lease_version=0
					and ts.attempt=0 and ts.checkpoint_id is null
			)`, timestamp(), taskID)
	return err
}

func (s *Service) mergedDependencyCommits(ctx context.Context, taskID, stepID, planVersion int64) ([]dependencyMerge, bool, error) {
	rows, err := s.db.QueryContext(ctx, `select dep.status,
			coalesce(mr.source_commit,''),coalesce(mr.merge_commit,'')
		from workflow_edges e
		join task_steps dep on dep.task_id=e.task_id and dep.id=e.from_step_id
		left join merge_requests mr on mr.id=(
			select latest.id from merge_requests latest
			where latest.task_id=e.task_id and latest.step_id=e.from_step_id
				and latest.status='merged' and latest.source_commit<>'' and latest.merge_commit<>''
			order by latest.id desc limit 1
		)
		where e.task_id=? and e.to_step_id=? and e.plan_version=?
		order by e.from_step_id`, taskID, stepID, planVersion)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	commits := make([]dependencyMerge, 0)
	for rows.Next() {
		var status string
		var dependency dependencyMerge
		if err := rows.Scan(&status, &dependency.SourceCommit, &dependency.MergeCommit); err != nil {
			return nil, false, err
		}
		if status != "completed" || strings.TrimSpace(dependency.SourceCommit) == "" || strings.TrimSpace(dependency.MergeCommit) == "" {
			return nil, false, nil
		}
		commits = append(commits, dependency)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(commits) == 0 {
		return nil, false, nil
	}
	return commits, true, nil
}

func trimWorkspaceError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 500 {
		return value[:500]
	}
	return value
}
