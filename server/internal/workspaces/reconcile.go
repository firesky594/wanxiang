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

	"wanxiang-agent/server/internal/gitx"
	"wanxiang-agent/server/internal/planning"
)

type RepairDirection string

const (
	RepairFromDatabase    RepairDirection = "database"
	RepairFromGitSnapshot RepairDirection = "git_snapshot"
)

// ReconcileTask 核对数据库、快照与 Worktree 漂移。
func (s *Service) ReconcileTask(ctx context.Context, taskID int64) (TaskWorkspace, error) {
	workspace, err := s.GetTask(ctx, taskID)
	if err != nil {
		return TaskWorkspace{}, err
	}
	var projectDir string
	if err = s.db.QueryRowContext(ctx, `select p.dir from tasks t join projects p on p.id=t.project_id where t.id=?`, taskID).Scan(&projectDir); err != nil {
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
			if hex.EncodeToString(sum[:]) != item.MetadataHash {
				reasons = append(reasons, "snapshot_hash_mismatch")
			}
			metadata, decodeErr := DecodeAssignment(content)
			if decodeErr != nil {
				reasons = append(reasons, "snapshot_invalid")
			} else if metadata.TaskID != taskID || metadata.StepID != item.StepID || metadata.AssignmentID != item.AssignmentID || metadata.AgentName != item.AgentName || metadata.ReportsTo != item.ReportsTo || metadata.BranchName != item.BranchName || metadata.BaseCommit != item.BaseCommit {
				reasons = append(reasons, "database_snapshot_mismatch")
			}
		}
		branch, branchErr := runTrim(ctx, item.WorktreePath, "branch", "--show-current")
		if branchErr != nil || branch != item.BranchName {
			reasons = append(reasons, "worktree_branch_mismatch")
		}
		head, headErr := runTrim(ctx, item.WorktreePath, "rev-parse", "HEAD")
		if headErr != nil || head != item.ProvisionCommit {
			reasons = append(reasons, "worktree_head_mismatch")
		}
		if len(reasons) > 0 {
			drifted = true
			message := strings.Join(reasons, ",")
			_, _ = s.db.ExecContext(ctx, `update project_workspaces set status='drifted',last_error=?,updated_at=? where id=?`, message, timestamp(), item.ID)
		} else {
			_, _ = s.db.ExecContext(ctx, `update project_workspaces set status='ready',last_error='',updated_at=? where id=?`, timestamp(), item.ID)
		}
	}
	if drifted {
		_, _ = s.db.ExecContext(ctx, `update tasks set status='workspace_drifted' where id=?`, taskID)
		_ = s.bus.PublishJSON(ctx, &taskID, "task.workspace.drifted", "system", map[string]any{"task_id": taskID})
	} else {
		_, _ = s.db.ExecContext(ctx, `update tasks set status='workspace_ready' where id=?`, taskID)
	}
	return s.GetTask(ctx, taskID)
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
