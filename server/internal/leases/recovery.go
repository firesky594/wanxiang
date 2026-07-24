package leases

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// InterruptExpired 中断已过期租约并记录恢复窗口。
func (s *Service) InterruptExpired(ctx context.Context) (int, error) {
	now := s.clock.Now().UTC()
	rows, err := s.db.QueryContext(ctx, `select l.lease_id
		from task_step_leases l
		join task_steps ts on ts.task_id=l.task_id and ts.id=l.step_id and ts.lease_id=l.lease_id and ts.lease_version=l.lease_version
		where l.status='active' and l.expires_at<=? and ts.status in ('in_progress','checkpointed')
		order by l.id`, formatTime(now))
	if err != nil {
		return 0, err
	}
	var leaseIDs []string
	for rows.Next() {
		var leaseID string
		if err := rows.Scan(&leaseID); err != nil {
			rows.Close()
			return 0, err
		}
		leaseIDs = append(leaseIDs, leaseID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}

	count := 0
	for _, leaseID := range leaseIDs {
		tx, beginErr := s.db.BeginTx(ctx, nil)
		if beginErr != nil {
			return count, beginErr
		}
		var taskID, stepID, version int64
		var agent string
		result, updateErr := tx.ExecContext(ctx, `update task_step_leases set status='interrupted',interrupted_at=?,resume_deadline=?,updated_at=? where lease_id=? and status='active' and expires_at<=?`, formatTime(now), formatTime(now.Add(ResumeWindow)), formatTime(now), leaseID, formatTime(now))
		if updateErr != nil {
			tx.Rollback()
			return count, updateErr
		}
		changed, _ := result.RowsAffected()
		if changed == 0 {
			tx.Rollback()
			continue
		}
		if err := tx.QueryRowContext(ctx, `select task_id,step_id,agent_name,lease_version from task_step_leases where lease_id=?`, leaseID).Scan(&taskID, &stepID, &agent, &version); err != nil {
			tx.Rollback()
			return count, err
		}
		result, err = tx.ExecContext(ctx, `update task_steps set status='interrupted',interrupted_at=?,resume_deadline=? where task_id=? and id=? and lease_id=? and lease_version=? and status in ('in_progress','checkpointed')`, formatTime(now), formatTime(now.Add(ResumeWindow)), taskID, stepID, leaseID, version)
		if err != nil {
			tx.Rollback()
			return count, err
		}
		changed, _ = result.RowsAffected()
		if changed != 1 {
			tx.Rollback()
			continue
		}
		payload, _ := json.Marshal(map[string]any{"step_id": stepID, "lease_id": leaseID, "resume_deadline": formatTime(now.Add(ResumeWindow))})
		if _, err = tx.ExecContext(ctx, `insert into runtime_events(task_id,event_type,actor,payload_json,created_at) values(?,'task.step.interrupted','system',?,?)`, taskID, string(payload), formatTime(now)); err != nil {
			tx.Rollback()
			return count, err
		}
		if err = tx.Commit(); err != nil {
			return count, err
		}
		_ = agent
		count++
	}
	return count, nil
}

// Resume 校验工作区现场并恢复中断租约。
func (s *Service) Resume(ctx context.Context, ref LeaseRef) (Lease, error) {
	lease, err := loadLease(ctx, s.db, ref.LeaseID)
	now := s.clock.Now().UTC()
	if err != nil || !sameRef(lease.LeaseRef, ref) || lease.Status != LeaseInterrupted || lease.ResumeDeadline == nil || !now.Before(*lease.ResumeDeadline) {
		return Lease{}, ErrConflict
	}
	if err := s.validateRecoveryWorkspace(ctx, ref); err != nil {
		return Lease{}, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback()
	expires := now.Add(LeaseTTL)
	result, err := tx.ExecContext(ctx, `update task_step_leases set status='active',expires_at=?,last_heartbeat_at=?,interrupted_at=null,resume_deadline=null,updated_at=? where lease_id=? and lease_version=? and agent_name=? and status='interrupted' and resume_deadline>?`, formatTime(expires), formatTime(now), formatTime(now), ref.LeaseID, ref.LeaseVersion, ref.AgentName, formatTime(now))
	if err != nil {
		return Lease{}, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Lease{}, ErrConflict
	}
	result, err = tx.ExecContext(ctx, `update task_steps set status='in_progress',lease_expires_at=?,last_heartbeat_at=?,interrupted_at=null,resume_deadline=null where task_id=? and id=? and agent_name=? and lease_id=? and lease_version=? and status='interrupted'`, formatTime(expires), formatTime(now), ref.TaskID, ref.StepID, ref.AgentName, ref.LeaseID, ref.LeaseVersion)
	if err != nil {
		return Lease{}, err
	}
	changed, _ = result.RowsAffected()
	if changed != 1 {
		return Lease{}, ErrConflict
	}
	payload, _ := json.Marshal(map[string]any{"step_id": ref.StepID, "lease_id": ref.LeaseID, "lease_version": ref.LeaseVersion})
	if _, err = tx.ExecContext(ctx, `insert into runtime_events(task_id,event_type,actor,payload_json,created_at) values(?,'task.step.resumed',?,?,?)`, ref.TaskID, ref.AgentName, string(payload), formatTime(now)); err != nil {
		return Lease{}, err
	}
	if err = tx.Commit(); err != nil {
		return Lease{}, err
	}
	heartbeat := now
	lease.Status = LeaseActive
	lease.ExpiresAt = expires
	lease.LastHeartbeatAt = &heartbeat
	lease.InterruptedAt = nil
	lease.ResumeDeadline = nil
	return lease, nil
}

func (s *Service) validateRecoveryWorkspace(ctx context.Context, ref LeaseRef) error {
	var path, branch, base, provision, status, owner string
	err := s.db.QueryRowContext(ctx, `select worktree_path,branch_name,base_commit,provision_commit,status,agent_name from project_workspaces where task_id=? and step_id=?`, ref.TaskID, ref.StepID).Scan(&path, &branch, &base, &provision, &status, &owner)
	if err != nil || status != "ready" || owner != ref.AgentName {
		return ErrConflict
	}
	currentBranch, err := gitValue(ctx, path, "branch", "--show-current")
	if err != nil || currentBranch != branch {
		return errors.New("recovery branch drift")
	}
	var checkpointID int64
	var commit, filesJSON string
	var clean int
	err = s.db.QueryRowContext(ctx, `select id,git_commit,clean,files_json from task_checkpoints where step_id=? and lease_id=? order by id desc limit 1`, ref.StepID, ref.LeaseID).Scan(&checkpointID, &commit, &clean, &filesJSON)
	if errors.Is(err, sql.ErrNoRows) {
		commit = provision
		if commit == "" {
			commit = base
		}
		clean = 1
		filesJSON = "[]"
	} else if err != nil {
		return err
	}
	head, err := gitValue(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if clean == 1 && (commit == "" || head != commit) {
		return errors.New("recovery HEAD drift")
	}
	statusOutput, err := gitValue(ctx, path, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return err
	}
	actual := recoveryDirtyPaths(statusOutput, ref.StepID)
	if clean == 1 {
		if len(actual) != 0 {
			return fmt.Errorf("recovery worktree has unexpected changes: %v", actual)
		}
		return nil
	}
	var expected []string
	if json.Unmarshal([]byte(filesJSON), &expected) != nil {
		return errors.New("invalid checkpoint files")
	}
	sort.Strings(expected)
	sort.Strings(actual)
	if strings.Join(expected, "\x00") != strings.Join(actual, "\x00") {
		return fmt.Errorf("dirty checkpoint drift: expected=%v actual=%v", expected, actual)
	}
	return nil
}

func recoveryDirtyPaths(output string, stepID int64) []string {
	result := []string{}
	mirrorPrefix := filepath.ToSlash(filepath.Join(".wanxiang", "checkpoints", fmt.Sprintf("%d", stepID))) + "/"
	for _, line := range strings.Split(output, "\n") {
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		path = filepath.ToSlash(path)
		if strings.HasPrefix(path, mirrorPrefix) && strings.HasSuffix(path, ".yaml") {
			continue
		}
		result = append(result, path)
	}
	return result
}
