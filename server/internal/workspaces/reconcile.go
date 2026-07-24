package workspaces

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"wanxiang-agent/server/internal/files"
	"wanxiang-agent/server/internal/gitx"
	"wanxiang-agent/server/internal/planning"
)

type RepairDirection string

const (
	RepairFromDatabase    RepairDirection = "database"
	RepairFromGitSnapshot RepairDirection = "git_snapshot"
)

// ReconcileTask 核对数据库、快照与 Worktree 漂移，并在首次租约前同步已合并依赖。
func (s *Service) ReconcileTask(ctx context.Context, taskID int64) (TaskWorkspace, error) {
	var projectID int64
	var projectDir, taskStatus string
	if err := s.db.QueryRowContext(ctx, `select p.id,p.dir,t.status from tasks t join projects p on p.id=t.project_id where t.id=?`, taskID).
		Scan(&projectID, &projectDir, &taskStatus); err != nil {
		return TaskWorkspace{}, err
	}
	if taskStatus != "workspace_ready" {
		return s.GetTask(ctx, taskID)
	}
	lock := s.projectLock(projectID)
	lock.Lock()
	defer lock.Unlock()
	if err := s.db.QueryRowContext(ctx, `select p.dir,t.status from tasks t join projects p on p.id=t.project_id where t.id=?`, taskID).
		Scan(&projectDir, &taskStatus); err != nil {
		return TaskWorkspace{}, err
	}
	if taskStatus != "workspace_ready" {
		return s.GetTask(ctx, taskID)
	}
	safeProjectDir, err := files.UnderRoot(s.cfg.ProjectDir, projectDir)
	if err != nil {
		return TaskWorkspace{}, fmt.Errorf("unsafe project path: %w", err)
	}
	projectDir = safeProjectDir
	if err := s.markDependentWorkspacesWaiting(ctx, taskID); err != nil {
		return TaskWorkspace{}, err
	}
	workspace, err := s.GetTask(ctx, taskID)
	if err != nil {
		return TaskWorkspace{}, err
	}
	drifted := false
	for _, item := range workspace.Items {
		reasons := []string{}
		path := filepath.Join(projectDir, ".wanxiang", "assignments", fmt.Sprintf("%d-%d.yaml", taskID, item.StepID))
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			reasons = append(reasons, "snapshot_missing")
		} else {
			sum := sha256.Sum256(content)
			contentHash := hex.EncodeToString(sum[:])
			metadata, decodeErr := DecodeAssignment(content)
			if decodeErr != nil {
				reasons = append(reasons, "snapshot_invalid")
			} else {
				preparing := s.pendingHandoffSnapshotControlled(ctx, taskID, item, metadata)
				if contentHash != item.MetadataHash && !preparing {
					reasons = append(reasons, "snapshot_hash_mismatch")
				}
				if (metadata.TaskID != taskID || metadata.StepID != item.StepID || metadata.AssignmentID != item.AssignmentID || metadata.AgentName != item.AgentName || metadata.ReportsTo != item.ReportsTo || metadata.BranchName != item.BranchName || metadata.BaseCommit != item.BaseCommit) && !preparing {
					reasons = append(reasons, "database_snapshot_mismatch")
				}
			}
		}
		branch, branchErr := runTrim(ctx, item.WorktreePath, "branch", "--show-current")
		if branchErr != nil || branch != item.BranchName {
			reasons = append(reasons, "worktree_branch_mismatch")
		}
		state, stateErr := s.loadStepControl(ctx, taskID, item.StepID)
		if stateErr != nil {
			return TaskWorkspace{}, stateErr
		}
		waiting := false
		if len(reasons) == 0 && (item.Status == "waiting_dependencies" || item.Status == "dependency_syncing") {
			syncResult, syncErr := s.syncDependenciesBeforeFirstLease(ctx, projectDir, taskID, item, state)
			if syncErr != nil {
				return TaskWorkspace{}, syncErr
			}
			item = syncResult.Item
			state = syncResult.State
			waiting = syncResult.Waiting
			if syncResult.DriftReason != "" {
				reasons = append(reasons, syncResult.DriftReason)
			}
		}
		head, headErr := runTrim(ctx, item.WorktreePath, "rev-parse", "HEAD")
		if headErr != nil {
			reasons = append(reasons, "worktree_head_missing")
		} else if len(reasons) == 0 {
			if reason := s.controlledHeadReason(ctx, projectDir, taskID, item, state, head); reason != "" {
				reasons = append(reasons, reason)
			}
		}
		if len(reasons) > 0 {
			drifted = true
			message := strings.Join(reasons, ",")
			if err := s.updateWorkspaceStateCAS(ctx, taskID, item, "drifted", message); err != nil {
				return TaskWorkspace{}, err
			}
		} else if !waiting {
			if err := s.updateWorkspaceStateCAS(ctx, taskID, item, "ready", ""); err != nil {
				return TaskWorkspace{}, err
			}
		}
	}
	nextTaskStatus := "workspace_ready"
	if drifted {
		nextTaskStatus = "workspace_drifted"
	}
	result, err := s.db.ExecContext(ctx, `update tasks set status=? where id=? and status='workspace_ready'`, nextTaskStatus, taskID)
	if err != nil {
		return TaskWorkspace{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return TaskWorkspace{}, errors.New("task changed concurrently during workspace reconciliation")
	}
	if drifted {
		_ = s.bus.PublishJSON(ctx, &taskID, "task.workspace.drifted", "system", map[string]any{"task_id": taskID})
	}
	return s.GetTask(ctx, taskID)
}

func (s *Service) updateWorkspaceStateCAS(ctx context.Context, taskID int64, item WorkspaceItem, status, lastError string) error {
	result, err := s.db.ExecContext(ctx, `update project_workspaces set status=?,last_error=?,updated_at=?
		where id=? and task_id=? and step_id=? and assignment_id=? and agent_name=? and branch_name=?
			and worktree_path=? and base_commit=? and provision_commit=? and metadata_hash=? and status=?`,
		status, lastError, timestamp(), item.ID, taskID, item.StepID, item.AssignmentID, item.AgentName,
		item.BranchName, item.WorktreePath, item.BaseCommit, item.ProvisionCommit, item.MetadataHash, item.Status)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("workspace changed concurrently during reconciliation")
	}
	return nil
}

// RepairTask 按指定可信源修复工作区漂移。
func (s *Service) RepairTask(ctx context.Context, taskID int64, direction RepairDirection, actor string) (TaskWorkspace, error) {
	if direction != RepairFromDatabase && direction != RepairFromGitSnapshot {
		return TaskWorkspace{}, errors.New("invalid repair direction")
	}
	workspace, err := s.GetTask(ctx, taskID)
	if err != nil {
		return TaskWorkspace{}, err
	}
	var projectDir string
	if err = s.db.QueryRowContext(ctx, `select p.dir from tasks t join projects p on p.id=t.project_id where t.id=?`, taskID).Scan(&projectDir); err != nil {
		return TaskWorkspace{}, err
	}
	for _, item := range workspace.Items {
		path := filepath.Join(projectDir, ".wanxiang", "assignments", fmt.Sprintf("%d-%d.yaml", taskID, item.StepID))
		if direction == RepairFromDatabase {
			var input string
			if err = s.db.QueryRowContext(ctx, `select input from task_steps where id=? and task_id=?`, item.StepID, taskID).Scan(&input); err != nil {
				return TaskWorkspace{}, err
			}
			var work planning.WorkItem
			if err = json.Unmarshal([]byte(input), &work); err != nil {
				return TaskWorkspace{}, err
			}
			encoded, hash, encodeErr := EncodeAssignment(AssignmentMetadata{MetadataVersion: 1, TaskID: taskID, StepID: item.StepID, AssignmentID: item.AssignmentID, WorkItemKey: work.Key, AgentName: item.AgentName, ReportsTo: item.ReportsTo, BranchName: item.BranchName, WorktreeID: fmt.Sprintf("task-%d-step-%d", taskID, item.StepID), BaseCommit: item.BaseCommit, WriteScope: item.WriteScope, Status: "ready"})
			if encodeErr != nil {
				return TaskWorkspace{}, encodeErr
			}
			if err = os.WriteFile(path, encoded, 0o644); err != nil {
				return TaskWorkspace{}, err
			}
			_, _ = s.db.ExecContext(ctx, `update project_workspaces set metadata_hash=?,status='ready',last_error='',updated_at=? where id=?`, hash, timestamp(), item.ID)
		} else {
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				return TaskWorkspace{}, readErr
			}
			metadata, decodeErr := DecodeAssignment(content)
			if decodeErr != nil {
				return TaskWorkspace{}, decodeErr
			}
			if metadata.TaskID != taskID || metadata.StepID != item.StepID || metadata.AssignmentID != item.AssignmentID {
				return TaskWorkspace{}, errors.New("snapshot identity does not match workspace")
			}
			sum := sha256.Sum256(content)
			scope, _ := json.Marshal(metadata.WriteScope)
			_, err = s.db.ExecContext(ctx, `update project_workspaces set agent_name=?,reports_to=?,branch_name=?,base_commit=?,write_scope_json=?,metadata_hash=?,status='ready',last_error='',updated_at=? where id=?`, metadata.AgentName, nullable(metadata.ReportsTo), metadata.BranchName, metadata.BaseCommit, string(scope), hex.EncodeToString(sum[:]), timestamp(), item.ID)
			if err != nil {
				return TaskWorkspace{}, err
			}
			if _, err = s.db.ExecContext(ctx, `update task_assignments set agent_name=?,reports_to=? where id=? and task_id=? and step_id=?`, metadata.AgentName, nullable(metadata.ReportsTo), metadata.AssignmentID, taskID, metadata.StepID); err != nil {
				return TaskWorkspace{}, err
			}
			if _, err = s.db.ExecContext(ctx, `update task_steps set agent_name=? where id=? and task_id=?`, metadata.AgentName, metadata.StepID, taskID); err != nil {
				return TaskWorkspace{}, err
			}
		}
	}
	if direction == RepairFromDatabase {
		if out, addErr := gitx.Run(ctx, projectDir, "add", ".wanxiang/assignments"); addErr != nil {
			return TaskWorkspace{}, fmt.Errorf("stage repaired metadata: %w: %s", addErr, out)
		}
		if _, cleanErr := gitx.Run(ctx, projectDir, "diff", "--cached", "--quiet"); cleanErr != nil {
			if out, commitErr := gitx.Run(ctx, projectDir, "commit", "-m", fmt.Sprintf("元数据：修复任务 %d 分配快照", taskID)); commitErr != nil {
				return TaskWorkspace{}, fmt.Errorf("commit repaired metadata: %w: %s", commitErr, out)
			}
		}
	}
	_, _ = s.db.ExecContext(ctx, `update tasks set status='workspace_ready' where id=?`, taskID)
	payload, _ := json.Marshal(map[string]any{"task_id": taskID, "direction": direction})
	_, _ = s.db.ExecContext(ctx, `insert into audit_logs(actor,action,target,payload_json,created_at) values(?,'workspace.repair',?,?,?)`, actor, fmt.Sprintf("task:%d", taskID), string(payload), timestamp())
	return s.ReconcileTask(ctx, taskID)
}

// RequestCleanup 校验条件并登记工作区清理请求。
func (s *Service) RequestCleanup(ctx context.Context, taskID int64, confirmed bool, actor string) (TaskWorkspace, error) {
	var taskStatus string
	if err := s.db.QueryRowContext(ctx, `select status from tasks where id=?`, taskID).Scan(&taskStatus); err != nil {
		return TaskWorkspace{}, err
	}
	terminal := taskStatus == "completed" || taskStatus == "merged"
	if !terminal && !confirmed {
		return TaskWorkspace{}, errors.New("non-terminal workspace cleanup requires explicit confirmation")
	}
	if _, err := s.db.ExecContext(ctx, `update project_workspaces set status='cleanup_pending',updated_at=? where task_id=?`, timestamp(), taskID); err != nil {
		return TaskWorkspace{}, err
	}
	s.audit(ctx, actor, "workspace.cleanup.request", taskID, map[string]any{"confirmed": confirmed, "task_status": taskStatus})
	return s.GetTask(ctx, taskID)
}

// ConfirmCleanup 复核现场后移除任务 Worktree。
func (s *Service) ConfirmCleanup(ctx context.Context, taskID int64, actor string) (TaskWorkspace, error) {
	workspace, err := s.GetTask(ctx, taskID)
	if err != nil {
		return TaskWorkspace{}, err
	}
	var projectDir string
	if err = s.db.QueryRowContext(ctx, `select p.dir from tasks t join projects p on p.id=t.project_id where t.id=?`, taskID).Scan(&projectDir); err != nil {
		return TaskWorkspace{}, err
	}
	for _, item := range workspace.Items {
		if item.Status != "cleanup_pending" {
			return TaskWorkspace{}, errors.New("workspace cleanup was not requested")
		}
		branch, branchErr := runTrim(ctx, item.WorktreePath, "branch", "--show-current")
		if branchErr != nil || branch != item.BranchName {
			return TaskWorkspace{}, errors.New("worktree ownership check failed")
		}
		if out, removeErr := gitx.Run(ctx, projectDir, "worktree", "remove", item.WorktreePath); removeErr != nil {
			return TaskWorkspace{}, fmt.Errorf("remove worktree: %w: %s", removeErr, out)
		}
		_, _ = s.db.ExecContext(ctx, `update project_workspaces set status='cleaned',updated_at=?,cleaned_at=? where id=?`, timestamp(), timestamp(), item.ID)
	}
	s.audit(ctx, actor, "workspace.cleanup.confirm", taskID, map[string]any{})
	return s.GetTask(ctx, taskID)
}
func (s *Service) audit(ctx context.Context, actor, action string, taskID int64, payload any) {
	encoded, _ := json.Marshal(payload)
	_, _ = s.db.ExecContext(ctx, `insert into audit_logs(actor,action,target,payload_json,created_at) values(?,?,?,?,?)`, actor, action, fmt.Sprintf("task:%d", taskID), string(encoded), timestamp())
}
