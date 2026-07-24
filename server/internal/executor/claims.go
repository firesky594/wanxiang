package executor

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"wanxiang-agent/server/internal/gitx"
	"wanxiang-agent/server/internal/leases"
)

const executionClaimTTL = 30 * time.Second

var errExecutionClaimHeld = errors.New("executor claim is held")

type executionClaim struct {
	Token             string
	Lease             leases.Lease
	LaunchCount       int
	ContinuationCount int
}

func newExecutionOwner() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return fmt.Sprintf("supervisor-%d-%d", os.Getpid(), time.Now().UnixNano())
	}
	return "supervisor-" + hex.EncodeToString(value)
}

func newExecutionClaimToken() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "claim_" + hex.EncodeToString(value), nil
}

func (s *Supervisor) claimExecution(ctx context.Context, lease leases.Lease) (executionClaim, error) {
	token, err := newExecutionClaimToken()
	if err != nil {
		return executionClaim{}, err
	}
	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	expiresText := now.Add(executionClaimTTL).Format(time.RFC3339Nano)

	var (
		existingToken, existingExpiry  string
		existingStatus                 string
		pid                            sql.NullInt64
		pidStart                       int64
		launchCount, continuationCount int
	)
	err = s.db.QueryRowContext(ctx, `select claim_token,coalesce(claim_expires_at,''),status,pid,pid_start_ticks,launch_count,continuation_count
		from executor_runs where lease_id=?`, lease.LeaseID).
		Scan(&existingToken, &existingExpiry, &existingStatus, &pid, &pidStart, &launchCount, &continuationCount)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		result, insertErr := s.db.ExecContext(ctx, `insert into executor_runs(
				task_id,step_id,agent_name,lease_id,lease_version,status,claim_token,claim_owner,claim_expires_at,
				created_at,updated_at
			) values(?,?,?,?,?,'starting',?,?,?,?,?)
			on conflict(lease_id) do nothing`,
			lease.TaskID, lease.StepID, lease.AgentName, lease.LeaseID, lease.LeaseVersion,
			token, s.ownerID, expiresText, nowText, nowText)
		if insertErr != nil {
			return executionClaim{}, insertErr
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return executionClaim{}, errExecutionClaimHeld
		}
		launchCount, continuationCount = 0, 0
	case err != nil:
		return executionClaim{}, err
	default:
		if existingToken == "" && (existingStatus == "starting" || existingStatus == "running") &&
			pid.Valid && executionProcessAlive(int(pid.Int64), pidStart) {
			return executionClaim{}, errExecutionClaimHeld
		}
		if existingToken != "" && claimStillOwned(existingExpiry, pid, pidStart, now) {
			return executionClaim{}, errExecutionClaimHeld
		}
		if launchCount > 0 && continuationCount >= maxLeaseContinuations {
			return executionClaim{}, errContinuationBlocked
		}
		result, updateErr := s.db.ExecContext(ctx, `update executor_runs
			set task_id=?,step_id=?,agent_name=?,lease_version=?,status='starting',
				claim_token=?,claim_owner=?,claim_expires_at=?,pid=null,pid_start_ticks=0,
				exit_code=null,error_summary='',exited_at=null,updated_at=?
			where lease_id=? and claim_token=?`,
			lease.TaskID, lease.StepID, lease.AgentName, lease.LeaseVersion,
			token, s.ownerID, expiresText, nowText, lease.LeaseID, existingToken)
		if updateErr != nil {
			return executionClaim{}, updateErr
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return executionClaim{}, errExecutionClaimHeld
		}
	}
	return executionClaim{Token: token, Lease: lease, LaunchCount: launchCount, ContinuationCount: continuationCount}, nil
}

func (s *Supervisor) prepareClaimedLease(ctx context.Context, lease leases.Lease) (leases.Lease, error) {
	if err := s.validateExecutionRecovery(ctx, lease); err != nil {
		if freezeErr := s.freezeExecution(ctx, lease.TaskID, lease.StepID, lease.LeaseID, "executor_recovery_checkpoint_invalid"); freezeErr != nil {
			return leases.Lease{}, errors.Join(err, freezeErr)
		}
		return leases.Lease{}, errContinuationBlocked
	}
	for attempt := 0; attempt < 3; attempt++ {
		current, err := s.loadExecutionLease(ctx, lease.LeaseRef)
		if err != nil {
			return leases.Lease{}, err
		}
		now := time.Now().UTC()
		switch current.Status {
		case leases.LeaseActive:
			if !now.Before(current.ExpiresAt) {
				if _, err := s.leases.InterruptExpired(ctx); err != nil {
					return leases.Lease{}, err
				}
				continue
			}
			renewed, err := s.leases.Heartbeat(ctx, current.LeaseRef)
			if err == nil {
				return renewed, nil
			}
			if !errors.Is(err, leases.ErrConflict) {
				return leases.Lease{}, err
			}
		case leases.LeaseInterrupted:
			resumed, err := s.leases.Resume(ctx, current.LeaseRef)
			if err == nil {
				return resumed, nil
			}
			if !errors.Is(err, leases.ErrConflict) {
				return leases.Lease{}, err
			}
			reloaded, loadErr := s.loadExecutionLease(ctx, lease.LeaseRef)
			if loadErr == nil && reloaded.Status == leases.LeaseActive {
				return reloaded, nil
			}
			if freezeErr := s.freezeExecution(ctx, lease.TaskID, lease.StepID, lease.LeaseID, "executor_resume_validation_failed"); freezeErr != nil {
				return leases.Lease{}, errors.Join(err, freezeErr)
			}
			return leases.Lease{}, errContinuationBlocked
		default:
			return leases.Lease{}, errContinuationBlocked
		}
	}
	return leases.Lease{}, errors.New("executor lease state changed during recovery")
}

func (s *Supervisor) validateExecutionRecovery(ctx context.Context, lease leases.Lease) error {
	var (
		worktree, branch, base, provision string
		checkpointID                      sql.NullInt64
		checkpointCommit                  sql.NullString
		checkpointBranch                  sql.NullString
		checkpointClean                   sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, `select pw.worktree_path,pw.branch_name,pw.base_commit,pw.provision_commit,
			cp.id,cp.git_commit,cp.branch_name,cp.clean
		from task_steps ts
		join project_workspaces pw on pw.task_id=ts.task_id and pw.step_id=ts.id and pw.agent_name=ts.agent_name
		left join task_checkpoints cp on cp.id=ts.checkpoint_id and cp.task_id=ts.task_id and cp.step_id=ts.id
		where ts.task_id=? and ts.id=? and ts.agent_name=? and pw.status='ready'`,
		lease.TaskID, lease.StepID, lease.AgentName).
		Scan(&worktree, &branch, &base, &provision, &checkpointID, &checkpointCommit, &checkpointBranch, &checkpointClean)
	if err != nil {
		return err
	}
	if checkpointID.Valid {
		if !checkpointCommit.Valid || checkpointCommit.String == "" || !checkpointBranch.Valid ||
			checkpointBranch.String != branch || !checkpointClean.Valid || checkpointClean.Int64 != 1 {
			return errors.New("executor recovery checkpoint is invalid")
		}
		return validateCompletionWorktree(ctx, worktree, branch, checkpointCommit.String, lease.StepID, checkpointID.Int64)
	}
	expected := provision
	if expected == "" {
		expected = base
	}
	currentBranch, err := gitx.Run(ctx, worktree, "branch", "--show-current")
	if err != nil || strings.TrimSpace(currentBranch) != branch {
		return errors.New("executor recovery branch mismatch")
	}
	head, err := gitx.Run(ctx, worktree, "rev-parse", "HEAD")
	if err != nil || expected == "" || strings.TrimSpace(head) != expected {
		return errors.New("executor recovery baseline mismatch")
	}
	status, err := gitx.Run(ctx, worktree, "status", "--porcelain", "--untracked-files=all")
	if err != nil || completionWorktreeDirty(status, lease.StepID, 0) {
		return errors.New("executor recovery baseline is dirty")
	}
	return nil
}

func (s *Supervisor) loadExecutionLease(ctx context.Context, ref leases.LeaseRef) (leases.Lease, error) {
	var status, acquired, expires string
	err := s.db.QueryRowContext(ctx, `select status,acquired_at,expires_at
		from task_step_leases
		where task_id=? and step_id=? and agent_name=? and lease_id=? and lease_version=?`,
		ref.TaskID, ref.StepID, ref.AgentName, ref.LeaseID, ref.LeaseVersion).
		Scan(&status, &acquired, &expires)
	if err != nil {
		return leases.Lease{}, err
	}
	acquiredAt, err := time.Parse(time.RFC3339Nano, acquired)
	if err != nil {
		return leases.Lease{}, err
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return leases.Lease{}, err
	}
	return leases.Lease{LeaseRef: ref, Status: leases.LeaseStatus(status), AcquiredAt: acquiredAt, ExpiresAt: expiresAt}, nil
}

func claimStillOwned(expires string, pid sql.NullInt64, startTicks int64, now time.Time) bool {
	if pid.Valid && pid.Int64 > 0 && executionProcessAlive(int(pid.Int64), startTicks) {
		return true
	}
	deadline, err := time.Parse(time.RFC3339Nano, expires)
	return err == nil && now.Before(deadline)
}

func (s *Supervisor) confirmExecutionLaunch(ctx context.Context, claim executionClaim, process WorkerProcess) (int, error) {
	pid := process.PID()
	startTicks, _, _ := readProcessIdentity(pid)
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var launchCount, continuationCount int
	if err := tx.QueryRowContext(ctx, `select launch_count,continuation_count from executor_runs
		where lease_id=? and claim_token=? and claim_owner=? and status='starting'`,
		claim.Lease.LeaseID, claim.Token, s.ownerID).Scan(&launchCount, &continuationCount); err != nil {
		return 0, err
	}
	isContinuation := launchCount > 0
	if isContinuation && continuationCount >= maxLeaseContinuations {
		return 0, errContinuationBlocked
	}
	nextContinuation := continuationCount
	if isContinuation {
		nextContinuation++
	}
	result, err := tx.ExecContext(ctx, `update executor_runs
		set pid=?,pid_start_ticks=?,status='running',started_at=?,updated_at=?,
			claim_expires_at=?,launch_count=?,continuation_count=?
		where lease_id=? and claim_token=? and claim_owner=? and status='starting'`,
		pid, startTicks, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		claim.Lease.ExpiresAt.UTC().Format(time.RFC3339Nano), launchCount+1, nextContinuation,
		claim.Lease.LeaseID, claim.Token, s.ownerID)
	if err != nil {
		return 0, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return 0, errExecutionClaimHeld
	}
	if isContinuation {
		payload, _ := json.Marshal(map[string]any{
			"step_id":       claim.Lease.StepID,
			"lease_id":      claim.Lease.LeaseID,
			"lease_version": claim.Lease.LeaseVersion,
			"continuation":  nextContinuation,
		})
		if _, err := tx.ExecContext(ctx, `insert into runtime_events(task_id,event_type,actor,payload_json,created_at)
			values(?,'task.executor.continued','system',?,?)`,
			claim.Lease.TaskID, string(payload), now.Format(time.RFC3339Nano)); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return nextContinuation, nil
}

func (s *Supervisor) releaseExecutionClaim(ctx context.Context, claim executionClaim, status, summary string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `update executor_runs
		set status=?,claim_token='',claim_owner='',claim_expires_at=null,pid=null,pid_start_ticks=0,
			error_summary=?,updated_at=?
		where lease_id=? and claim_token=? and claim_owner=?`,
		status, Redact(summary), now, claim.Lease.LeaseID, claim.Token, s.ownerID)
	return err
}

func executionProcessAlive(pid int, expectedStart int64) bool {
	start, state, err := readProcessIdentity(pid)
	if err != nil || state == "Z" || state == "X" {
		return false
	}
	return expectedStart <= 0 || start == expectedStart
}

func readProcessIdentity(pid int) (int64, string, error) {
	if pid <= 0 {
		return 0, "", errors.New("invalid process id")
	}
	content, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, "", err
	}
	value := string(content)
	closeIndex := strings.LastIndex(value, ")")
	if closeIndex < 0 || closeIndex+2 >= len(value) {
		return 0, "", errors.New("invalid process stat")
	}
	fields := strings.Fields(value[closeIndex+2:])
	if len(fields) <= 19 {
		return 0, "", errors.New("incomplete process stat")
	}
	start, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return 0, "", err
	}
	return start, fields[0], nil
}
