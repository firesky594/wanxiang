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
	"strings"

	"wanxiang-agent/server/internal/gitx"
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

// Reassign 基于检查点将中断步骤接管给新 Agent。
func (s *Service) Reassign(ctx context.Context, input ReassignInput, actor string) (Lease, error) {
	if actor == "" || input.TaskID <= 0 || input.StepID <= 0 || !safeAgentPart.MatchString(input.NewAgent) {
		return Lease{}, ErrConflict
	}
	now := s.clock.Now().UTC()
	var oldLeaseID, oldAgent, oldStatus, oldBranch, oldWorktree, projectDir, recordedMainCommit, stepInput string
	var reportsTo, scopeJSON, oldMetadataHash, workspaceStatus string
	var oldVersion, attempt, projectID, assignmentID int64
	var deadline sql.NullString
	err := s.db.QueryRowContext(ctx, `select ts.lease_id,ts.lease_version,ts.agent_name,ts.attempt,ts.input,l.status,l.resume_deadline,
			pw.project_id,pw.branch_name,pw.worktree_path,p.dir,coalesce(p.main_commit,''),ta.id,coalesce(ta.reports_to,''),pw.write_scope_json,pw.metadata_hash,pw.status
		from task_steps ts join task_step_leases l on l.lease_id=ts.lease_id
		join project_workspaces pw on pw.task_id=ts.task_id and pw.step_id=ts.id
		join task_assignments ta on ta.task_id=ts.task_id and ta.step_id=ts.id
		join projects p on p.id=pw.project_id where ts.task_id=? and ts.id=?`, input.TaskID, input.StepID).
		Scan(&oldLeaseID, &oldVersion, &oldAgent, &attempt, &stepInput, &oldStatus, &deadline,
			&projectID, &oldBranch, &oldWorktree, &projectDir, &recordedMainCommit, &assignmentID, &reportsTo, &scopeJSON, &oldMetadataHash, &workspaceStatus)
	if err != nil || oldStatus != string(LeaseInterrupted) {
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
	metadataCommit := checkpoint.GitCommit
	if _, mainErr := gitValue(ctx, projectDir, "show-ref", "--verify", "refs/heads/main"); mainErr == nil {
		metadataCommit, err = prepareHandoffSnapshot(ctx, projectDir, input.TaskID, input.StepID, oldMetadataHash, encoded)
		if err != nil {
			return Lease{}, err
		}
		metadataCommit, err = resolveHandoffMainCommit(
			ctx, projectDir, input.TaskID, input.StepID, metadataCommit, recordedMainCommit, encoded,
		)
		if err != nil {
			return Lease{}, err
		}
	} else if filepath.Clean(projectDir) == filepath.Clean(oldWorktree) {
		// 兼容尚未迁移到独立 main 工作区的旧记录；新项目必须走可提交的 assignment snapshot。
		metadataHash = oldMetadataHash
	} else {
		return Lease{}, ErrRecoveryReview
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
	result, err = tx.ExecContext(ctx, `update task_assignments set agent_name=?,status='assigned'
		where id=? and task_id=? and step_id=? and agent_name=?`,
		input.NewAgent, assignmentID, input.TaskID, input.StepID, oldAgent)
	if err != nil {
		return Lease{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return Lease{}, ErrConflict
	}
	result, err = tx.ExecContext(ctx, `update project_workspaces
		set agent_name=?,branch_name=?,worktree_path=?,base_commit=?,provision_commit=?,metadata_hash=?,status='ready',last_error='',updated_at=?
		where task_id=? and step_id=? and agent_name=? and branch_name=? and worktree_path=? and metadata_hash=? and status='ready'`,
		input.NewAgent, newBranch, newWorktree, checkpoint.GitCommit, checkpoint.GitCommit, metadataHash, formatTime(now),
		input.TaskID, input.StepID, oldAgent, oldBranch, oldWorktree, oldMetadataHash)
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
	if err := insertAudit(ctx, tx, actor, "lease.reassign", stepTarget(input.TaskID, input.StepID), map[string]any{"old_lease_id": oldLeaseID, "new_lease_id": newLeaseID, "checkpoint_id": checkpoint.ID, "new_agent": input.NewAgent, "immediate": input.Immediate}, now); err != nil {
		return Lease{}, err
	}
	payload, _ := json.Marshal(map[string]any{"step_id": input.StepID, "from_agent": oldAgent, "to_agent": input.NewAgent, "checkpoint_id": checkpoint.ID, "attempt": newAttempt})
	if _, err = tx.ExecContext(ctx, `insert into runtime_events(task_id,event_type,actor,payload_json,created_at) values(?,'task.step.reassigned',?,?,?)`, input.TaskID, actor, string(payload), formatTime(now)); err != nil {
		return Lease{}, err
	}
	if err = tx.Commit(); err != nil {
		if recovered, recoverErr := s.reassignedLease(ctx, preparation.ID, newLeaseID, input.NewAgent); recoverErr == nil {
			return recovered, nil
		}
		return Lease{}, err
	}
	heartbeat := now
	return Lease{LeaseRef: LeaseRef{TaskID: input.TaskID, StepID: input.StepID, AgentName: input.NewAgent, LeaseID: newLeaseID, LeaseVersion: newVersion}, Status: LeaseActive, AcquiredAt: now, ExpiresAt: expires, LastHeartbeatAt: &heartbeat}, nil
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

func prepareHandoffSnapshot(ctx context.Context, projectDir string, taskID, stepID int64, oldHash string, encoded []byte) (string, error) {
	if branch, err := gitValue(ctx, projectDir, "branch", "--show-current"); err != nil || branch != "main" {
		return "", ErrRecoveryReview
	}
	relative := filepath.ToSlash(filepath.Join(".wanxiang", "assignments", fmt.Sprintf("%d-%d.yaml", taskID, stepID)))
	status, err := gitValue(ctx, projectDir, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(status, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) < 4 || filepath.ToSlash(strings.TrimSpace(line[3:])) != relative {
			return "", errors.New("project main has unrelated changes during handoff")
		}
	}
	path := filepath.Join(projectDir, filepath.FromSlash(relative))
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	current, readErr := os.ReadFile(path)
	if readErr != nil && !os.IsNotExist(readErr) {
		return "", readErr
	}
	if !bytes.Equal(current, encoded) {
		currentHash := sha256.Sum256(current)
		if readErr != nil || hex.EncodeToString(currentHash[:]) != oldHash {
			return "", ErrRecoveryReview
		}
		if err = writeHandoffSnapshotAtomic(path, encoded); err != nil {
			return "", err
		}
	}
	headContent, showErr := gitx.Run(ctx, projectDir, "show", "HEAD:"+relative)
	pathStatus, statusErr := gitValue(ctx, projectDir, "status", "--porcelain", "--", relative)
	if showErr == nil && bytes.Equal([]byte(headContent), encoded) && statusErr == nil && pathStatus == "" {
		return gitValue(ctx, projectDir, "rev-parse", "HEAD")
	}
	if _, err = gitx.Run(ctx, projectDir, "add", "--", relative); err != nil {
		return "", err
	}
	if _, err = gitx.Run(ctx, projectDir, "commit", "-m", fmt.Sprintf("元数据：任务 %d 步骤 %d 转交", taskID, stepID)); err != nil {
		headContent, showErr = gitx.Run(ctx, projectDir, "show", "HEAD:"+relative)
		pathStatus, statusErr = gitValue(ctx, projectDir, "status", "--porcelain", "--", relative)
		if showErr != nil || !bytes.Equal([]byte(headContent), encoded) || statusErr != nil || pathStatus != "" {
			return "", err
		}
	}
	status, err = gitValue(ctx, projectDir, "status", "--porcelain", "--untracked-files=all")
	if err != nil || status != "" {
		return "", errors.New("project main changed concurrently during handoff")
	}
	return gitValue(ctx, projectDir, "rev-parse", "HEAD")
}

func resolveHandoffMainCommit(ctx context.Context, projectDir string, taskID, stepID int64, snapshotCommit, recordedCommit string, encoded []byte) (string, error) {
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
	relative := filepath.ToSlash(filepath.Join(".wanxiang", "assignments", fmt.Sprintf("%d-%d.yaml", taskID, stepID)))
	content, err := gitx.Run(ctx, projectDir, "show", current+":"+relative)
	if err != nil || !bytes.Equal([]byte(content), encoded) {
		return "", ErrRecoveryReview
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
	if err := s.db.QueryRowContext(ctx, `select status from step_reassignments where id=? and to_lease_id=?`, preparationID, leaseID).Scan(&status); err != nil || status != "completed" {
		return Lease{}, ErrConflict
	}
	lease, err := loadLease(ctx, s.db, leaseID)
	if err != nil || lease.AgentName != agent || lease.Status != LeaseActive {
		return Lease{}, ErrConflict
	}
	return lease, nil
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
