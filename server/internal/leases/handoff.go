package leases

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"wanxiang-agent/server/internal/gitx"
)

var ErrRecoveryReview = errors.New("recovery review required")

var safeAgentPart = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type ReassignInput struct {
	TaskID       int64  `json:"task_id"`
	StepID       int64  `json:"step_id"`
	NewAgent     string `json:"new_agent"`
	CheckpointID int64  `json:"checkpoint_id,omitempty"`
	Immediate    bool   `json:"immediate"`
	Reason       string `json:"reason"`
}

// Reassign 基于检查点将中断步骤接管给新 Agent。
func (s *Service) Reassign(ctx context.Context, input ReassignInput, actor string) (Lease, error) {
	if actor == "" || input.TaskID <= 0 || input.StepID <= 0 || !safeAgentPart.MatchString(input.NewAgent) {
		return Lease{}, ErrConflict
	}
	now := s.clock.Now().UTC()
	var oldLeaseID, oldAgent, oldStatus, oldBranch, oldWorktree, projectDir, stepInput string
	var oldVersion, attempt, projectID int64
	var deadline sql.NullString
	err := s.db.QueryRowContext(ctx, `select ts.lease_id,ts.lease_version,ts.agent_name,ts.attempt,ts.input,l.status,l.resume_deadline,pw.project_id,pw.branch_name,pw.worktree_path,p.dir
		from task_steps ts join task_step_leases l on l.lease_id=ts.lease_id
		join project_workspaces pw on pw.task_id=ts.task_id and pw.step_id=ts.id
		join projects p on p.id=pw.project_id where ts.task_id=? and ts.id=?`, input.TaskID, input.StepID).
		Scan(&oldLeaseID, &oldVersion, &oldAgent, &attempt, &stepInput, &oldStatus, &deadline, &projectID, &oldBranch, &oldWorktree, &projectDir)
	if err != nil || oldStatus != string(LeaseInterrupted) {
		return Lease{}, ErrConflict
	}
	if !input.Immediate {
		parsedDeadline, parseErr := parseNullableTime(deadline)
		if parseErr != nil || parsedDeadline == nil || now.Before(*parsedDeadline) {
			return Lease{}, ErrConflict
		}
	}
	var online string
	if err := s.db.QueryRowContext(ctx, `select status from agent_registry where name=?`, input.NewAgent).Scan(&online); err != nil || online != "online" {
		return Lease{}, ErrConflict
	}

	checkpoint, err := s.cleanCheckpointForHandoff(ctx, input)
	if err != nil {
		_ = s.markRecoveryBlocked(ctx, input, oldLeaseID, oldAgent, actor, oldBranch, oldWorktree, err.Error())
		return Lease{}, ErrRecoveryReview
	}
	if out, err := gitx.Run(ctx, projectDir, "cat-file", "-e", checkpoint.GitCommit+"^{commit}"); err != nil {
		_ = s.markRecoveryBlocked(ctx, input, oldLeaseID, oldAgent, actor, oldBranch, oldWorktree, strings.TrimSpace(out))
		return Lease{}, ErrRecoveryReview
	}

	workKey := fmt.Sprintf("step-%d", input.StepID)
	var item struct {
		Key string `json:"key"`
	}
	if json.Unmarshal([]byte(stepInput), &item) == nil && safeAgentPart.MatchString(item.Key) {
		workKey = item.Key
	}
	newAttempt := attempt + 1
	newBranch := fmt.Sprintf("agent/%s/%d-%s-resume-%d", input.NewAgent, input.TaskID, workKey, newAttempt)
	newWorktree := filepath.Join(filepath.Dir(oldWorktree), fmt.Sprintf("step-%d-%s-resume-%d", input.StepID, input.NewAgent, newAttempt))
	if entries, readErr := os.ReadDir(newWorktree); readErr == nil && len(entries) > 0 {
		_ = s.markRecoveryBlocked(ctx, input, oldLeaseID, oldAgent, actor, oldBranch, oldWorktree, "recovery worktree path is not empty")
		return Lease{}, ErrRecoveryReview
	} else if readErr != nil && !os.IsNotExist(readErr) {
		return Lease{}, readErr
	}
	if _, err := gitx.Run(ctx, projectDir, "show-ref", "--verify", "--quiet", "refs/heads/"+newBranch); err == nil {
		_ = s.markRecoveryBlocked(ctx, input, oldLeaseID, oldAgent, actor, oldBranch, oldWorktree, "recovery branch already exists")
		return Lease{}, ErrRecoveryReview
	}
	if err := os.MkdirAll(filepath.Dir(newWorktree), 0o755); err != nil {
		return Lease{}, err
	}
	if out, err := gitx.Run(ctx, projectDir, "worktree", "add", "-b", newBranch, newWorktree, checkpoint.GitCommit); err != nil {
		_ = s.markRecoveryBlocked(ctx, input, oldLeaseID, oldAgent, actor, oldBranch, oldWorktree, strings.TrimSpace(out))
		return Lease{}, ErrRecoveryReview
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = gitx.Run(context.Background(), projectDir, "worktree", "remove", "--force", newWorktree)
			_, _ = gitx.Run(context.Background(), projectDir, "branch", "-D", newBranch)
		}
	}()

	newLeaseID, err := randomLeaseID()
	if err != nil {
		return Lease{}, err
	}
	newVersion := oldVersion + 1
	expires := now.Add(LeaseTTL)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `update task_step_leases set status='revoked',revoked_at=?,revoked_reason=?,updated_at=? where lease_id=? and lease_version=? and status='interrupted'`, formatTime(now), input.Reason, formatTime(now), oldLeaseID, oldVersion)
	if err != nil {
		return Lease{}, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Lease{}, ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `insert into task_step_leases(task_id,step_id,agent_name,lease_id,lease_version,status,branch_name,worktree_path,acquired_at,expires_at,last_heartbeat_at,created_at,updated_at) values(?,?,?,?,?,'active',?,?,?,?,?,?,?)`, input.TaskID, input.StepID, input.NewAgent, newLeaseID, newVersion, newBranch, newWorktree, formatTime(now), formatTime(expires), formatTime(now), formatTime(now), formatTime(now)); err != nil {
		return Lease{}, err
	}
	if _, err = tx.ExecContext(ctx, `update task_assignments set agent_name=?,status='assigned' where task_id=? and step_id=?`, input.NewAgent, input.TaskID, input.StepID); err != nil {
		return Lease{}, err
	}
	if _, err = tx.ExecContext(ctx, `update project_workspaces set agent_name=?,branch_name=?,worktree_path=?,base_commit=?,provision_commit=?,status='ready',last_error='',updated_at=? where task_id=? and step_id=?`, input.NewAgent, newBranch, newWorktree, checkpoint.GitCommit, checkpoint.GitCommit, formatTime(now), input.TaskID, input.StepID); err != nil {
		return Lease{}, err
	}
	result, err = tx.ExecContext(ctx, `update task_steps set agent_name=?,status='in_progress',lease_id=?,lease_version=?,lease_expires_at=?,last_heartbeat_at=?,checkpoint_id=?,attempt=?,interrupted_at=null,resume_deadline=null where task_id=? and id=? and lease_id=? and lease_version=?`, input.NewAgent, newLeaseID, newVersion, formatTime(expires), formatTime(now), checkpoint.ID, newAttempt, input.TaskID, input.StepID, oldLeaseID, oldVersion)
	if err != nil {
		return Lease{}, err
	}
	changed, _ = result.RowsAffected()
	if changed != 1 {
		return Lease{}, ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `insert into step_reassignments(task_id,step_id,from_agent,to_agent,from_lease_id,to_lease_id,checkpoint_id,attempt,reason,status,from_branch,from_worktree,to_branch,to_worktree,created_by,created_at,completed_at) values(?,?,?,?,?,?,?,?,?,'completed',?,?,?,?,?,?,?)`, input.TaskID, input.StepID, oldAgent, input.NewAgent, oldLeaseID, newLeaseID, checkpoint.ID, newAttempt, input.Reason, oldBranch, oldWorktree, newBranch, newWorktree, actor, formatTime(now), formatTime(now)); err != nil {
		return Lease{}, err
	}
	if err := insertAudit(ctx, tx, actor, "lease.reassign", stepTarget(input.TaskID, input.StepID), map[string]any{"old_lease_id": oldLeaseID, "new_lease_id": newLeaseID, "checkpoint_id": checkpoint.ID, "new_agent": input.NewAgent, "immediate": input.Immediate}, now); err != nil {
		return Lease{}, err
	}
	payload, _ := json.Marshal(map[string]any{"step_id": input.StepID, "from_agent": oldAgent, "to_agent": input.NewAgent, "checkpoint_id": checkpoint.ID, "attempt": newAttempt})
	if _, err = tx.ExecContext(ctx, `insert into runtime_events(task_id,event_type,actor,payload_json,created_at) values(?,'task.step.reassigned',?,?,?)`, input.TaskID, actor, string(payload), formatTime(now)); err != nil {
		return Lease{}, err
	}
	if err = tx.Commit(); err != nil {
		return Lease{}, err
	}
	committed = true
	heartbeat := now
	return Lease{LeaseRef: LeaseRef{TaskID: input.TaskID, StepID: input.StepID, AgentName: input.NewAgent, LeaseID: newLeaseID, LeaseVersion: newVersion}, Status: LeaseActive, AcquiredAt: now, ExpiresAt: expires, LastHeartbeatAt: &heartbeat}, nil
}

func (s *Service) cleanCheckpointForHandoff(ctx context.Context, input ReassignInput) (Checkpoint, error) {
	query := `select id,task_id,step_id,lease_id,idempotency_key,git_commit,branch_name,clean,summary_hash,high_risk,created_at from task_checkpoints where task_id=? and step_id=? and clean=1 and git_commit<>''`
	args := []any{input.TaskID, input.StepID}
	if input.CheckpointID > 0 {
		query += ` and id=?`
		args = append(args, input.CheckpointID)
	}
	query += ` order by id desc limit 1`
	return scanCheckpoint(s.db.QueryRowContext(ctx, query, args...))
}

func (s *Service) markRecoveryBlocked(ctx context.Context, input ReassignInput, oldLeaseID, oldAgent, actor, oldBranch, oldWorktree, reason string) error {
	now := s.clock.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `update task_steps set status='blocked' where task_id=? and id=? and lease_id=?`, input.TaskID, input.StepID, oldLeaseID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `insert into step_reassignments(task_id,step_id,from_agent,to_agent,from_lease_id,checkpoint_id,attempt,reason,status,from_branch,from_worktree,created_by,created_at) values(?,?,?,?,?,?,?,?, 'blocked',?,?,?,?)`, input.TaskID, input.StepID, oldAgent, input.NewAgent, oldLeaseID, nullableCheckpoint(input.CheckpointID), 0, reason, oldBranch, oldWorktree, actor, formatTime(now)); err != nil {
		return err
	}
	if err := insertAudit(ctx, tx, actor, "lease.reassign_blocked", stepTarget(input.TaskID, input.StepID), map[string]any{"reason": reason, "checkpoint_id": input.CheckpointID}, now); err != nil {
		return err
	}
	return tx.Commit()
}

func nullableCheckpoint(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}
