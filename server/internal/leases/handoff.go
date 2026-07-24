package leases

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"wanxiang-agent/server/internal/gitx"
	"wanxiang-agent/server/internal/matching"
	"wanxiang-agent/server/internal/workspaces"
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

type handoffPreparation struct {
	ID         int64
	NewLeaseID string
	Agent      string
	Branch     string
	Worktree   string
	Checkpoint int64
	Attempt    int64
	Created    bool
}

type handoffMetadataChange struct {
	WorkspaceID int64
	StepID      int64
	Assignment  int64
	OldAgent    string
	OldReports  string
	NewReports  string
	OldHash     string
	NewHash     string
	Relative    string
	Content     []byte
	Selected    bool
}

// Reassign 基于检查点将中断步骤接管给新 Agent。
func (s *Service) Reassign(ctx context.Context, input ReassignInput, actor string) (Lease, error) {
	if actor == "" || input.TaskID <= 0 || input.StepID <= 0 || !safeAgentPart.MatchString(input.NewAgent) {
		return Lease{}, ErrConflict
	}
	var projectID int64
	if err := s.db.QueryRowContext(ctx, `select project_id from tasks where id=? and project_id is not null`, input.TaskID).Scan(&projectID); err != nil {
		return Lease{}, ErrConflict
	}
	if s.dataDir == "" {
		return Lease{}, errors.New("project lock data directory is unavailable")
	}
	releaseProjectLock, err := gitx.AcquireProjectLock(ctx, s.dataDir, projectID)
	if err != nil {
		return Lease{}, fmt.Errorf("acquire project git lock: %w", err)
	}
	defer releaseProjectLock()
	metadataRollbackHead := ""
	metadataRollbackDir := ""
	metadataRollback := false
	handoffStatusMarked := false
	preserveHandoffStatus := false
	defer func() {
		rollbackSucceeded := true
		if metadataRollback && metadataRollbackHead != "" && metadataRollbackDir != "" {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, rollbackErr := gitx.Run(rollbackCtx, metadataRollbackDir, "reset", "--hard", metadataRollbackHead)
			rollbackSucceeded = rollbackErr == nil
		}
		if handoffStatusMarked && !preserveHandoffStatus && rollbackSucceeded {
			restoreCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, _ = s.db.ExecContext(restoreCtx, `update tasks set status='workspace_ready'
				where id=? and status='handoff_preparing'`, input.TaskID)
		}
	}()

	now := s.clock.Now().UTC()
	var oldLeaseID, oldAgent, oldStatus, oldBranch, oldWorktree, projectDir, projectSlug, recordedMainCommit, stepInput, projectLead string
	var reportsTo, scopeJSON, oldMetadataHash, workspaceStatus string
	var oldVersion, attempt, lockedProjectID, workspaceID, assignmentID, planVersion int64
	var deadline sql.NullString
	err = s.db.QueryRowContext(ctx, `select ts.lease_id,ts.lease_version,ts.agent_name,ts.attempt,ts.input,l.status,l.resume_deadline,
			pw.project_id,pw.id,pw.branch_name,pw.worktree_path,p.dir,p.slug,coalesce(p.main_commit,''),ta.id,coalesce(ta.reports_to,''),pw.write_scope_json,pw.metadata_hash,pw.status,
			ts.plan_version,coalesce(td.project_lead,'')
		from task_steps ts join task_step_leases l on l.lease_id=ts.lease_id
		join project_workspaces pw on pw.task_id=ts.task_id and pw.step_id=ts.id
		join task_assignments ta on ta.task_id=ts.task_id and ta.step_id=ts.id
		join projects p on p.id=pw.project_id
		left join team_decisions td on td.task_id=ts.task_id and td.plan_version=ts.plan_version
		where ts.task_id=? and ts.id=?`, input.TaskID, input.StepID).
		Scan(&oldLeaseID, &oldVersion, &oldAgent, &attempt, &stepInput, &oldStatus, &deadline,
			&lockedProjectID, &workspaceID, &oldBranch, &oldWorktree, &projectDir, &projectSlug, &recordedMainCommit, &assignmentID, &reportsTo, &scopeJSON, &oldMetadataHash, &workspaceStatus,
			&planVersion, &projectLead)
	if err != nil || lockedProjectID != projectID || oldStatus != string(LeaseInterrupted) {
		return Lease{}, ErrConflict
	}
	if workspaceStatus != "ready" {
		return Lease{}, ErrConflict
	}
	if !input.Immediate {
		parsedDeadline, parseErr := parseNullableTime(deadline)
		if parseErr != nil || parsedDeadline == nil || now.Before(*parsedDeadline) {
			return Lease{}, ErrConflict
		}
	}
	var online, agentDir string
	if err := s.db.QueryRowContext(ctx, `select status,dir from agent_registry where name=?`, input.NewAgent).Scan(&online, &agentDir); err != nil || online != "online" {
		return Lease{}, ErrConflict
	}
	if err := validateHandoffProjectAccess(agentDir, input.NewAgent, projectSlug); err != nil {
		return Lease{}, ErrConflict
	}

	checkpoint, err := s.cleanCheckpointForHandoff(ctx, input, oldLeaseID)
	if err != nil {
		_ = s.markRecoveryBlocked(ctx, input, oldLeaseID, oldAgent, actor, oldBranch, oldWorktree, err.Error())
		return Lease{}, ErrRecoveryReview
	}
	if out, err := gitx.Run(ctx, projectDir, "cat-file", "-e", checkpoint.GitCommit+"^{commit}"); err != nil {
		_ = s.markRecoveryBlocked(ctx, input, oldLeaseID, oldAgent, actor, oldBranch, oldWorktree, strings.TrimSpace(out))
		return Lease{}, ErrRecoveryReview
	}
	leaderChanged := projectLead != "" && projectLead == oldAgent && input.NewAgent != oldAgent
	if leaderChanged {
		if reportsTo != "" {
			_ = s.markRecoveryBlocked(ctx, input, oldLeaseID, oldAgent, actor, oldBranch, oldWorktree, "project lead has an invalid reports_to relationship")
			return Lease{}, ErrRecoveryReview
		}
		var approved int
		if err := s.db.QueryRowContext(ctx, `select count(*) from merge_requests
			where task_id=? and project_lead=? and status='approved'`, input.TaskID, oldAgent).Scan(&approved); err != nil {
			return Lease{}, err
		}
		if approved > 0 {
			_ = s.markRecoveryBlocked(ctx, input, oldLeaseID, oldAgent, actor, oldBranch, oldWorktree, "approved merge request requires review before project lead takeover")
			return Lease{}, ErrRecoveryReview
		}
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
	preparation, err := s.loadOrCreateHandoffPreparation(ctx, input, oldLeaseID, oldAgent, oldBranch, oldWorktree,
		newBranch, newWorktree, checkpoint.ID, newAttempt, actor)
	if err != nil {
		return Lease{}, err
	}
	if preparation.Agent != input.NewAgent || preparation.Branch != newBranch || preparation.Worktree != newWorktree ||
		preparation.Checkpoint != checkpoint.ID || preparation.Attempt != newAttempt || preparation.NewLeaseID == "" {
		return Lease{}, ErrConflict
	}
	if preparation.Created && handoffTargetExists(ctx, projectDir, newWorktree, newBranch) {
		_, _ = s.db.ExecContext(ctx, `update step_reassignments set status='blocked' where id=? and status='preparing'`, preparation.ID)
		_ = s.markRecoveryBlocked(ctx, input, oldLeaseID, oldAgent, actor, oldBranch, oldWorktree, "recovery branch or worktree already exists without preparation")
		return Lease{}, ErrRecoveryReview
	}
	if err = ensureHandoffWorktree(ctx, projectDir, newWorktree, newBranch, checkpoint.GitCommit); err != nil {
		return Lease{}, err
	}
	var writeScope []string
	if json.Unmarshal([]byte(scopeJSON), &writeScope) != nil || len(writeScope) == 0 {
		return Lease{}, ErrRecoveryReview
	}
	encoded, metadataHash, err := workspaces.EncodeAssignment(workspaces.AssignmentMetadata{
		MetadataVersion: 1,
		TaskID:          input.TaskID,
		StepID:          input.StepID,
		AssignmentID:    assignmentID,
		WorkItemKey:     workKey,
		AgentName:       input.NewAgent,
		ReportsTo:       reportsTo,
		BranchName:      newBranch,
		WorktreeID:      fmt.Sprintf("task-%d-step-%d", input.TaskID, input.StepID),
		BaseCommit:      checkpoint.GitCommit,
		WriteScope:      writeScope,
		Status:          "ready",
	})
	if err != nil {
		return Lease{}, err
	}
	metadataChanges := []handoffMetadataChange{{
		WorkspaceID: workspaceID,
		StepID:      input.StepID,
		Assignment:  assignmentID,
		OldAgent:    oldAgent,
		OldReports:  reportsTo,
		NewReports:  reportsTo,
		OldHash:     oldMetadataHash,
		NewHash:     metadataHash,
		Relative:    filepath.ToSlash(filepath.Join(".wanxiang", "assignments", fmt.Sprintf("%d-%d.yaml", input.TaskID, input.StepID))),
		Content:     encoded,
		Selected:    true,
	}}
	metadataCommit := checkpoint.GitCommit
	if _, mainErr := gitValue(ctx, projectDir, "show-ref", "--verify", "refs/heads/main"); mainErr == nil {
		metadataRollbackHead, err = gitValue(ctx, projectDir, "rev-parse", "HEAD")
		if err != nil {
			return Lease{}, err
		}
		metadataRollbackDir = projectDir
		metadataChanges, err = s.buildHandoffMetadataChanges(
			ctx, input.TaskID, input.StepID, planVersion, projectSlug, oldAgent, input.NewAgent,
			projectLead, newBranch, checkpoint.GitCommit, metadataChanges[0], leaderChanged,
		)
		if err != nil {
			return Lease{}, err
		}
		for _, change := range metadataChanges {
			if change.Selected {
				metadataHash = change.NewHash
				break
			}
		}
		metadataCommit, err = prepareHandoffSnapshots(ctx, projectDir, input.TaskID, metadataChanges)
		if err != nil {
			return Lease{}, err
		}
		metadataRollback = true
		metadataCommit, err = resolveHandoffMainCommit(
			ctx, projectDir, metadataCommit, recordedMainCommit, metadataChanges,
		)
		if err != nil {
			return Lease{}, err
		}
	} else if filepath.Clean(projectDir) == filepath.Clean(oldWorktree) {
		// 兼容尚未迁移到独立 main 工作区的旧记录；新项目必须走可提交的 assignment snapshot。
		if leaderChanged {
			return Lease{}, ErrRecoveryReview
		}
		metadataHash = oldMetadataHash
	} else {
		return Lease{}, ErrRecoveryReview
	}
	if err := s.markTaskHandoffPreparing(ctx, input.TaskID); err != nil {
		return Lease{}, err
	}
	handoffStatusMarked = true
	selectedMetadata := metadataChanges[0]
	for _, change := range metadataChanges {
		if change.Selected {
			selectedMetadata = change
			break
		}
	}

	newLeaseID := preparation.NewLeaseID
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
	result, err = tx.ExecContext(ctx, `update task_assignments set agent_name=?,reports_to=?,status='assigned'
		where id=? and task_id=? and step_id=? and agent_name=? and coalesce(reports_to,'')=?`,
		input.NewAgent, nullableReportsTo(selectedMetadata.NewReports), assignmentID, input.TaskID, input.StepID, oldAgent, selectedMetadata.OldReports)
	if err != nil {
		return Lease{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return Lease{}, ErrConflict
	}
	if leaderChanged {
		result, err = tx.ExecContext(ctx, `update team_decisions set project_lead=?
			where task_id=? and plan_version=? and project_lead=?`,
			input.NewAgent, input.TaskID, planVersion, oldAgent)
		if err != nil {
			return Lease{}, err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return Lease{}, ErrConflict
		}
		for _, change := range metadataChanges {
			if change.Selected || change.WorkspaceID <= 0 {
				continue
			}
			result, err = tx.ExecContext(ctx, `update task_assignments set reports_to=?
				where id=? and task_id=? and step_id=? and agent_name=? and coalesce(reports_to,'')=?`,
				nullableReportsTo(change.NewReports), change.Assignment, input.TaskID, change.StepID, change.OldAgent, change.OldReports)
			if err != nil {
				return Lease{}, err
			}
			if changed, _ := result.RowsAffected(); changed != 1 {
				return Lease{}, ErrConflict
			}
			result, err = tx.ExecContext(ctx, `update project_workspaces set reports_to=?,metadata_hash=?,updated_at=?
				where id=? and task_id=? and step_id=? and agent_name=? and coalesce(reports_to,'')=? and metadata_hash=?`,
				nullableReportsTo(change.NewReports), change.NewHash, formatTime(now), change.WorkspaceID, input.TaskID, change.StepID,
				change.OldAgent, change.OldReports, change.OldHash)
			if err != nil {
				return Lease{}, err
			}
			if changed, _ := result.RowsAffected(); changed != 1 {
				return Lease{}, ErrConflict
			}
		}
		if _, err = tx.ExecContext(ctx, `update merge_requests set project_lead=?
			where task_id=? and project_lead=? and status='pending_review'`,
			input.NewAgent, input.TaskID, oldAgent); err != nil {
			return Lease{}, err
		}
	}
	result, err = tx.ExecContext(ctx, `update project_workspaces
		set agent_name=?,reports_to=?,branch_name=?,worktree_path=?,base_commit=?,provision_commit=?,metadata_hash=?,status='ready',last_error='',updated_at=?
		where id=? and task_id=? and step_id=? and agent_name=? and coalesce(reports_to,'')=? and branch_name=? and worktree_path=? and metadata_hash=? and status='ready'`,
		input.NewAgent, nullableReportsTo(selectedMetadata.NewReports), newBranch, newWorktree, checkpoint.GitCommit, checkpoint.GitCommit, metadataHash, formatTime(now),
		workspaceID, input.TaskID, input.StepID, oldAgent, selectedMetadata.OldReports, oldBranch, oldWorktree, oldMetadataHash)
	if err != nil {
		return Lease{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return Lease{}, ErrConflict
	}
	result, err = tx.ExecContext(ctx, `update task_steps set agent_name=?,status='in_progress',lease_id=?,lease_version=?,lease_expires_at=?,last_heartbeat_at=?,checkpoint_id=?,attempt=?,interrupted_at=null,resume_deadline=null where task_id=? and id=? and lease_id=? and lease_version=?`, input.NewAgent, newLeaseID, newVersion, formatTime(expires), formatTime(now), checkpoint.ID, newAttempt, input.TaskID, input.StepID, oldLeaseID, oldVersion)
	if err != nil {
		return Lease{}, err
	}
	changed, _ = result.RowsAffected()
	if changed != 1 {
		return Lease{}, ErrConflict
	}
	result, err = tx.ExecContext(ctx, `update step_reassignments set status='completed',completed_at=?
		where id=? and task_id=? and step_id=? and from_lease_id=? and to_lease_id=? and status='preparing'`,
		formatTime(now), preparation.ID, input.TaskID, input.StepID, oldLeaseID, newLeaseID)
	if err != nil {
		return Lease{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return Lease{}, ErrConflict
	}
	result, err = tx.ExecContext(ctx, `update projects set main_commit=?
		where id=? and coalesce(main_commit,'')=?`, metadataCommit, projectID, recordedMainCommit)
	if err != nil {
		return Lease{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return Lease{}, ErrConflict
	}
	result, err = tx.ExecContext(ctx, `update tasks set status='workspace_ready'
		where id=? and status='handoff_preparing'`, input.TaskID)
	if err != nil {
		return Lease{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return Lease{}, ErrConflict
	}
	if err := insertAudit(ctx, tx, actor, "lease.reassign", stepTarget(input.TaskID, input.StepID), map[string]any{"old_lease_id": oldLeaseID, "new_lease_id": newLeaseID, "checkpoint_id": checkpoint.ID, "new_agent": input.NewAgent, "immediate": input.Immediate}, now); err != nil {
		return Lease{}, err
	}
	payload, _ := json.Marshal(map[string]any{"step_id": input.StepID, "from_agent": oldAgent, "to_agent": input.NewAgent, "checkpoint_id": checkpoint.ID, "attempt": newAttempt})
	if _, err = tx.ExecContext(ctx, `insert into runtime_events(task_id,event_type,actor,payload_json,created_at) values(?,'task.step.reassigned',?,?,?)`, input.TaskID, actor, string(payload), formatTime(now)); err != nil {
		return Lease{}, err
	}
	if err = tx.Commit(); err != nil {
		recovered, persisted, confirmErr := s.confirmReassignedLease(preparation.ID, newLeaseID, input.NewAgent)
		if persisted {
			metadataRollback = false
			handoffStatusMarked = false
			return recovered, nil
		}
		if confirmErr != nil {
			metadataRollback = false
			preserveHandoffStatus = true
			return Lease{}, errors.Join(err, fmt.Errorf("handoff commit outcome is uncertain: %w", confirmErr))
		}
		return Lease{}, err
	}
	metadataRollback = false
	handoffStatusMarked = false
	heartbeat := now
	return Lease{LeaseRef: LeaseRef{TaskID: input.TaskID, StepID: input.StepID, AgentName: input.NewAgent, LeaseID: newLeaseID, LeaseVersion: newVersion}, Status: LeaseActive, AcquiredAt: now, ExpiresAt: expires, LastHeartbeatAt: &heartbeat}, nil
}

func (s *Service) markTaskHandoffPreparing(ctx context.Context, taskID int64) error {
	result, err := s.db.ExecContext(ctx, `update tasks set status='handoff_preparing'
		where id=? and status in ('workspace_ready','handoff_preparing')`, taskID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Service) loadOrCreateHandoffPreparation(ctx context.Context, input ReassignInput, oldLeaseID, oldAgent, oldBranch, oldWorktree, newBranch, newWorktree string, checkpointID, attempt int64, actor string) (handoffPreparation, error) {
	load := func() (handoffPreparation, error) {
		var result handoffPreparation
		err := s.db.QueryRowContext(ctx, `select id,to_lease_id,to_agent,to_branch,to_worktree,coalesce(checkpoint_id,0),attempt
			from step_reassignments where task_id=? and step_id=? and from_lease_id=? and status='preparing'
			order by id desc limit 1`, input.TaskID, input.StepID, oldLeaseID).
			Scan(&result.ID, &result.NewLeaseID, &result.Agent, &result.Branch, &result.Worktree, &result.Checkpoint, &result.Attempt)
		return result, err
	}
	if existing, err := load(); err == nil {
		if existing.Agent != input.NewAgent || existing.Branch != newBranch || existing.Worktree != newWorktree ||
			existing.Checkpoint != checkpointID || existing.Attempt != attempt {
			return handoffPreparation{}, ErrConflict
		}
		return existing, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return handoffPreparation{}, err
	}
	newLeaseID, err := randomLeaseID()
	if err != nil {
		return handoffPreparation{}, err
	}
	now := s.clock.Now().UTC()
	result, err := s.db.ExecContext(ctx, `insert into step_reassignments(
			task_id,step_id,from_agent,to_agent,from_lease_id,to_lease_id,checkpoint_id,attempt,reason,status,
			from_branch,from_worktree,to_branch,to_worktree,created_by,created_at
		)
		select ?,?,?,?,?,?,?,?,?, 'preparing',?,?,?,?,?,?
		where not exists(
			select 1 from step_reassignments where task_id=? and step_id=? and from_lease_id=? and status='preparing'
		)`,
		input.TaskID, input.StepID, oldAgent, input.NewAgent, oldLeaseID, newLeaseID, checkpointID, attempt, input.Reason,
		oldBranch, oldWorktree, newBranch, newWorktree, actor, formatTime(now),
		input.TaskID, input.StepID, oldLeaseID)
	if err != nil {
		return handoffPreparation{}, err
	}
	if changed, _ := result.RowsAffected(); changed == 1 {
		id, _ := result.LastInsertId()
		return handoffPreparation{
			ID: id, NewLeaseID: newLeaseID, Agent: input.NewAgent, Branch: newBranch,
			Worktree: newWorktree, Checkpoint: checkpointID, Attempt: attempt, Created: true,
		}, nil
	}
	existing, err := load()
	if err != nil || existing.Agent != input.NewAgent || existing.Branch != newBranch || existing.Worktree != newWorktree ||
		existing.Checkpoint != checkpointID || existing.Attempt != attempt {
		return handoffPreparation{}, ErrConflict
	}
	return existing, nil
}

func handoffTargetExists(ctx context.Context, projectDir, path, branch string) bool {
	if entries, err := os.ReadDir(path); err == nil && len(entries) > 0 {
		return true
	}
	_, err := gitValue(ctx, projectDir, "show-ref", "--verify", "refs/heads/"+branch)
	return err == nil
}

func ensureHandoffWorktree(ctx context.Context, projectDir, path, branch, commit string) error {
	if handoffWorktreeExact(ctx, projectDir, path, branch, commit) {
		return nil
	}
	if entries, err := os.ReadDir(path); err == nil && len(entries) > 0 {
		return ErrRecoveryReview
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if branchHead, err := gitValue(ctx, projectDir, "rev-parse", "refs/heads/"+branch); err == nil {
		if branchHead != commit {
			return ErrRecoveryReview
		}
		if _, err = gitx.Run(ctx, projectDir, "worktree", "add", path, branch); err != nil && !handoffWorktreeExact(ctx, projectDir, path, branch, commit) {
			return err
		}
	} else if _, err = gitx.Run(ctx, projectDir, "worktree", "add", "-b", branch, path, commit); err != nil && !handoffWorktreeExact(ctx, projectDir, path, branch, commit) {
		return err
	}
	if !handoffWorktreeExact(ctx, projectDir, path, branch, commit) {
		return ErrRecoveryReview
	}
	return nil
}

func handoffWorktreeExact(ctx context.Context, projectDir, path, branch, commit string) bool {
	raw, err := gitValue(ctx, projectDir, "worktree", "list", "--porcelain")
	if err != nil {
		return false
	}
	expectedPath := filepath.Clean(path)
	expectedBranch := "refs/heads/" + branch
	found := false
	var listedPath, listedHead, listedBranch string
	check := func() {
		if filepath.Clean(listedPath) == expectedPath && listedHead == commit && listedBranch == expectedBranch {
			found = true
		}
	}
	for _, line := range append(strings.Split(raw, "\n"), "") {
		if line == "" {
			check()
			listedPath, listedHead, listedBranch = "", "", ""
			continue
		}
		key, value, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		switch key {
		case "worktree":
			listedPath = value
		case "HEAD":
			listedHead = value
		case "branch":
			listedBranch = value
		}
	}
	if !found {
		return false
	}
	gotBranch, branchErr := gitValue(ctx, path, "branch", "--show-current")
	gotCommit, commitErr := gitValue(ctx, path, "rev-parse", "HEAD")
	status, statusErr := gitValue(ctx, path, "status", "--porcelain", "--untracked-files=all")
	return branchErr == nil && commitErr == nil && statusErr == nil &&
		gotBranch == branch && gotCommit == commit && status == ""
}

func (s *Service) buildHandoffMetadataChanges(
	ctx context.Context,
	taskID, selectedStepID, planVersion int64,
	projectSlug, oldAgent, newAgent, currentLead, newBranch, checkpointCommit string,
	selected handoffMetadataChange,
	leaderChanged bool,
) ([]handoffMetadataChange, error) {
	rows, err := s.db.QueryContext(ctx, `select pw.id,pw.step_id,ta.id,ta.agent_name,coalesce(ta.reports_to,''),
			ts.input,pw.branch_name,pw.base_commit,pw.write_scope_json,pw.metadata_hash
		from task_assignments ta
		join task_steps ts on ts.task_id=ta.task_id and ts.id=ta.step_id
		join project_workspaces pw on pw.task_id=ta.task_id and pw.step_id=ta.step_id
		where ta.task_id=? and ts.plan_version=? order by pw.step_id`, taskID, planVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	changes := make([]handoffMetadataChange, 0)
	agents := make([]workspaces.ProjectAgent, 0)
	selectedFound := false
	for rows.Next() {
		var workspaceID, stepID, assignmentID int64
		var agent, reportsTo, stepInput, branch, baseCommit, scopeJSON, oldHash string
		if err := rows.Scan(&workspaceID, &stepID, &assignmentID, &agent, &reportsTo, &stepInput, &branch, &baseCommit, &scopeJSON, &oldHash); err != nil {
			return nil, err
		}
		newReportsTo := reportsTo
		if leaderChanged {
			switch {
			case stepID == selectedStepID:
				newReportsTo = ""
			case agent == newAgent:
				newReportsTo = ""
			case agent == oldAgent && reportsTo == "":
				newReportsTo = newAgent
			case reportsTo == oldAgent:
				newReportsTo = newAgent
			}
		}
		if stepID == selectedStepID {
			if workspaceID != selected.WorkspaceID || assignmentID != selected.Assignment ||
				agent != selected.OldAgent || reportsTo != selected.OldReports || oldHash != selected.OldHash {
				return nil, ErrConflict
			}
			selected.NewReports = newReportsTo
			selected.NewHash = ""
			var writeScope []string
			var item struct {
				Key string `json:"key"`
			}
			if json.Unmarshal([]byte(scopeJSON), &writeScope) != nil || len(writeScope) == 0 ||
				json.Unmarshal([]byte(stepInput), &item) != nil {
				return nil, ErrRecoveryReview
			}
			selected.Content, selected.NewHash, err = workspaces.EncodeAssignment(workspaces.AssignmentMetadata{
				MetadataVersion: 1,
				TaskID:          taskID,
				StepID:          stepID,
				AssignmentID:    assignmentID,
				WorkItemKey:     item.Key,
				AgentName:       newAgent,
				ReportsTo:       newReportsTo,
				BranchName:      newBranch,
				WorktreeID:      fmt.Sprintf("task-%d-step-%d", taskID, stepID),
				BaseCommit:      checkpointCommit,
				WriteScope:      writeScope,
				Status:          "ready",
			})
			if err != nil {
				return nil, err
			}
			changes = append(changes, selected)
			agent = newAgent
			selectedFound = true
		} else if newReportsTo != reportsTo {
			var writeScope []string
			var item struct {
				Key string `json:"key"`
			}
			if json.Unmarshal([]byte(scopeJSON), &writeScope) != nil || len(writeScope) == 0 ||
				json.Unmarshal([]byte(stepInput), &item) != nil {
				return nil, ErrRecoveryReview
			}
			encoded, newHash, encodeErr := workspaces.EncodeAssignment(workspaces.AssignmentMetadata{
				MetadataVersion: 1,
				TaskID:          taskID,
				StepID:          stepID,
				AssignmentID:    assignmentID,
				WorkItemKey:     item.Key,
				AgentName:       agent,
				ReportsTo:       newReportsTo,
				BranchName:      branch,
				WorktreeID:      fmt.Sprintf("task-%d-step-%d", taskID, stepID),
				BaseCommit:      baseCommit,
				WriteScope:      writeScope,
				Status:          "ready",
			})
			if encodeErr != nil {
				return nil, encodeErr
			}
			changes = append(changes, handoffMetadataChange{
				WorkspaceID: workspaceID,
				StepID:      stepID,
				Assignment:  assignmentID,
				OldAgent:    agent,
				OldReports:  reportsTo,
				NewReports:  newReportsTo,
				OldHash:     oldHash,
				NewHash:     newHash,
				Relative:    filepath.ToSlash(filepath.Join(".wanxiang", "assignments", fmt.Sprintf("%d-%d.yaml", taskID, stepID))),
				Content:     encoded,
			})
		}
		agents = append(agents, workspaces.ProjectAgent{Name: agent, ReportsTo: newReportsTo})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !selectedFound {
		return nil, ErrConflict
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })
	projectLead := currentLead
	if leaderChanged {
		projectLead = newAgent
	}
	project, err := workspaces.EncodeProject(workspaces.ProjectMetadata{
		MetadataVersion: 1,
		Project:         projectSlug,
		Manager:         "manager",
		ProjectLead:     projectLead,
		Agents:          agents,
		BranchPolicy:    "agent/<agent>/<task>-<work-item>",
		MergeTarget:     "main",
	})
	if err != nil {
		return nil, err
	}
	changes = append(changes, handoffMetadataChange{
		Relative: filepath.ToSlash(filepath.Join(".wanxiang", "project.yaml")),
		Content:  project,
	})
	sort.Slice(changes, func(i, j int) bool { return changes[i].Relative < changes[j].Relative })
	return changes, nil
}

func prepareHandoffSnapshots(ctx context.Context, projectDir string, taskID int64, changes []handoffMetadataChange) (string, error) {
	if branch, err := gitValue(ctx, projectDir, "branch", "--show-current"); err != nil || branch != "main" {
		return "", ErrRecoveryReview
	}
	originalHead, err := gitValue(ctx, projectDir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	allowed := make(map[string]handoffMetadataChange, len(changes))
	relatives := make([]string, 0, len(changes))
	for _, change := range changes {
		if change.Relative == "" || allowed[change.Relative].Relative != "" {
			return "", ErrRecoveryReview
		}
		allowed[change.Relative] = change
		relatives = append(relatives, change.Relative)
	}
	status, err := gitValue(ctx, projectDir, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(status, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) < 4 {
			return "", errors.New("project main has unrelated changes during handoff")
		}
		path := strings.TrimSpace(line[3:])
		if strings.Contains(path, " -> ") {
			path = strings.TrimSpace(strings.SplitN(path, " -> ", 2)[1])
		}
		if _, ok := allowed[filepath.ToSlash(path)]; !ok {
			return "", errors.New("project main has unrelated changes during handoff")
		}
	}
	needsWrite := make(map[string]bool, len(changes))
	for _, change := range changes {
		path := filepath.Join(projectDir, filepath.FromSlash(change.Relative))
		current, readErr := os.ReadFile(path)
		if readErr != nil && !os.IsNotExist(readErr) {
			return "", readErr
		}
		if bytes.Equal(current, change.Content) {
			continue
		}
		if change.OldHash != "" {
			currentHash := sha256.Sum256(current)
			if readErr != nil || hex.EncodeToString(currentHash[:]) != change.OldHash {
				return "", ErrRecoveryReview
			}
		} else {
			headContent, showErr := gitx.Run(ctx, projectDir, "show", "HEAD:"+change.Relative)
			if readErr != nil || showErr != nil || !bytes.Equal([]byte(headContent), current) {
				return "", ErrRecoveryReview
			}
		}
		needsWrite[change.Relative] = true
	}
	rollback := true
	defer func() {
		if rollback {
			_, _ = gitx.Run(context.Background(), projectDir, "reset", "--hard", originalHead)
		}
	}()
	for _, change := range changes {
		if !needsWrite[change.Relative] {
			continue
		}
		path := filepath.Join(projectDir, filepath.FromSlash(change.Relative))
		if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err = writeHandoffSnapshotAtomic(path, change.Content); err != nil {
			return "", err
		}
	}
	addArgs := append([]string{"--literal-pathspecs", "add", "--"}, relatives...)
	if _, err = gitx.Run(ctx, projectDir, addArgs...); err != nil {
		return "", err
	}
	if _, diffErr := gitx.Run(ctx, projectDir, "diff", "--cached", "--quiet"); diffErr != nil {
		if _, err = gitx.Run(ctx, projectDir, "commit", "-m", fmt.Sprintf("元数据：任务 %d 转交", taskID)); err != nil {
			for _, change := range changes {
				headContent, showErr := gitx.Run(ctx, projectDir, "show", "HEAD:"+change.Relative)
				pathStatus, statusErr := gitValue(ctx, projectDir, "status", "--porcelain", "--", change.Relative)
				if showErr != nil || !bytes.Equal([]byte(headContent), change.Content) || statusErr != nil || pathStatus != "" {
					return "", err
				}
			}
		}
	}
	status, err = gitValue(ctx, projectDir, "status", "--porcelain", "--untracked-files=all")
	if err != nil || status != "" {
		return "", errors.New("project main changed concurrently during handoff")
	}
	head, err := gitValue(ctx, projectDir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	rollback = false
	return head, nil
}

func resolveHandoffMainCommit(ctx context.Context, projectDir, snapshotCommit, recordedCommit string, changes []handoffMetadataChange) (string, error) {
	current, err := gitValue(ctx, projectDir, "rev-parse", "main")
	if err != nil {
		return "", err
	}
	if _, err = gitx.Run(ctx, projectDir, "merge-base", "--is-ancestor", snapshotCommit, current); err != nil {
		return "", ErrRecoveryReview
	}
	if recordedCommit != "" {
		if _, err = gitx.Run(ctx, projectDir, "merge-base", "--is-ancestor", recordedCommit, current); err != nil {
			return "", ErrRecoveryReview
		}
	}
	for _, change := range changes {
		content, showErr := gitx.Run(ctx, projectDir, "show", current+":"+change.Relative)
		if showErr != nil || !bytes.Equal([]byte(content), change.Content) {
			return "", ErrRecoveryReview
		}
	}
	status, err := gitValue(ctx, projectDir, "status", "--porcelain", "--untracked-files=all")
	if err != nil || status != "" {
		return "", ErrRecoveryReview
	}
	return current, nil
}

func writeHandoffSnapshotAtomic(path string, content []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".handoff-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(0o644); err == nil {
		_, err = file.Write(content)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}

func (s *Service) reassignedLease(ctx context.Context, preparationID int64, leaseID, agent string) (Lease, error) {
	var status string
	if err := s.db.QueryRowContext(ctx, `select status from step_reassignments where id=? and to_lease_id=?`, preparationID, leaseID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Lease{}, ErrConflict
		}
		return Lease{}, err
	}
	if status != "completed" {
		return Lease{}, ErrConflict
	}
	lease, err := loadLease(ctx, s.db, leaseID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Lease{}, ErrConflict
		}
		return Lease{}, err
	}
	if lease.AgentName != agent || lease.Status != LeaseActive {
		return Lease{}, ErrConflict
	}
	return lease, nil
}

func (s *Service) confirmReassignedLease(preparationID int64, leaseID, agent string) (Lease, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for attempt := 0; attempt < 2; attempt++ {
		lease, err := s.reassignedLease(ctx, preparationID, leaseID, agent)
		if err == nil {
			return lease, true, nil
		}
		if !errors.Is(err, ErrConflict) {
			return Lease{}, false, err
		}
		if attempt == 0 {
			timer := time.NewTimer(25 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return Lease{}, false, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return Lease{}, false, nil
}

func (s *Service) cleanCheckpointForHandoff(ctx context.Context, input ReassignInput, oldLeaseID string) (Checkpoint, error) {
	query := `select cp.id,cp.task_id,cp.step_id,cp.lease_id,cp.idempotency_key,cp.git_commit,cp.branch_name,cp.clean,cp.summary_hash,cp.high_risk,cp.created_at
		from task_steps ts
		join task_checkpoints cp on cp.id=`
	args := []any{}
	if input.CheckpointID > 0 {
		query += `?`
		args = append(args, input.CheckpointID)
	} else {
		query += `ts.checkpoint_id`
	}
	query += ` where ts.task_id=? and ts.id=? and ts.lease_id=? and cp.task_id=ts.task_id and cp.step_id=ts.id
		and cp.lease_id=? and cp.clean=1 and cp.git_commit<>'' limit 1`
	args = append(args, input.TaskID, input.StepID, oldLeaseID, oldLeaseID)
	return scanCheckpoint(s.db.QueryRowContext(ctx, query, args...))
}

func validateHandoffProjectAccess(agentDir, agentName, project string) error {
	agentDir, err := filepath.Abs(agentDir)
	if err != nil || filepath.Base(agentDir) != agentName {
		return errors.New("agent directory does not match identity")
	}
	root := filepath.Dir(agentDir)
	definition, err := matching.LoadDefinition(root, agentName)
	if err != nil {
		return err
	}
	for _, allowed := range definition.ProjectAccess {
		if allowed == "*" || allowed == project {
			return nil
		}
	}
	return errors.New("agent does not have project access")
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

func nullableReportsTo(value string) any {
	if value == "" {
		return nil
	}
	return value
}
