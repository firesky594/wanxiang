package leases

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

type Timeline struct {
	TaskID        int64                `json:"task_id"`
	Steps         []StepRecovery       `json:"steps"`
	Leases        []Lease              `json:"leases"`
	Checkpoints   []TimelineCheckpoint `json:"checkpoints"`
	Reassignments []Reassignment       `json:"reassignments"`
}

type StepRecovery struct {
	StepID          int64      `json:"step_id"`
	AgentName       string     `json:"agent_name"`
	Status          string     `json:"status"`
	LeaseVersion    int64      `json:"lease_version"`
	CheckpointID    *int64     `json:"checkpoint_id,omitempty"`
	Attempt         int64      `json:"attempt"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at,omitempty"`
	LeaseExpiresAt  *time.Time `json:"lease_expires_at,omitempty"`
	ResumeDeadline  *time.Time `json:"resume_deadline,omitempty"`
}

type TimelineCheckpoint struct {
	Checkpoint
	Summary RecoverySummary  `json:"summary"`
	Files   []string         `json:"files"`
	Tests   []CheckpointTest `json:"tests"`
}

type Reassignment struct {
	ID           int64     `json:"id"`
	StepID       int64     `json:"step_id"`
	FromAgent    string    `json:"from_agent"`
	ToAgent      string    `json:"to_agent"`
	CheckpointID *int64    `json:"checkpoint_id,omitempty"`
	Attempt      int64     `json:"attempt"`
	Reason       string    `json:"reason"`
	Status       string    `json:"status"`
	FromBranch   string    `json:"from_branch"`
	FromWorktree string    `json:"from_worktree"`
	ToBranch     string    `json:"to_branch"`
	ToWorktree   string    `json:"to_worktree"`
	CreatedBy    string    `json:"created_by"`
	CreatedAt    time.Time `json:"created_at"`
}

func (s *Service) GetCheckpointDetail(ctx context.Context, checkpointID int64) (TimelineCheckpoint, error) {
	var item TimelineCheckpoint
	var clean, highRisk int
	var created, summaryJSON, filesJSON, testsJSON string
	err := s.db.QueryRowContext(ctx, `select id,task_id,step_id,lease_id,idempotency_key,git_commit,branch_name,clean,summary_hash,high_risk,created_at,summary_json,files_json,tests_json from task_checkpoints where id=?`, checkpointID).
		Scan(&item.ID, &item.TaskID, &item.StepID, &item.LeaseID, &item.IdempotencyKey, &item.GitCommit, &item.BranchName, &clean, &item.SummaryHash, &highRisk, &created, &summaryJSON, &filesJSON, &testsJSON)
	if err != nil {
		return TimelineCheckpoint{}, err
	}
	item.Clean = clean == 1
	item.HighRisk = highRisk == 1
	item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	_ = json.Unmarshal([]byte(summaryJSON), &item.Summary)
	_ = json.Unmarshal([]byte(filesJSON), &item.Files)
	_ = json.Unmarshal([]byte(testsJSON), &item.Tests)
	return item, nil
}

func (s *Service) CurrentForAgent(ctx context.Context, taskID, stepID int64, agent string) (Lease, error) {
	var leaseID string
	if err := s.db.QueryRowContext(ctx, `select lease_id from task_steps where task_id=? and id=? and agent_name=?`, taskID, stepID, agent).Scan(&leaseID); err != nil || leaseID == "" {
		return Lease{}, ErrConflict
	}
	lease, err := loadLease(ctx, s.db, leaseID)
	if err != nil || lease.AgentName != agent {
		return Lease{}, ErrConflict
	}
	return lease, nil
}

func (s *Service) Timeline(ctx context.Context, taskID int64) (Timeline, error) {
	result := Timeline{TaskID: taskID, Steps: []StepRecovery{}, Leases: []Lease{}, Checkpoints: []TimelineCheckpoint{}, Reassignments: []Reassignment{}}
	rows, err := s.db.QueryContext(ctx, `select id,agent_name,status,lease_version,checkpoint_id,attempt,last_heartbeat_at,lease_expires_at,resume_deadline from task_steps where task_id=? order by id`, taskID)
	if err != nil {
		return Timeline{}, err
	}
	for rows.Next() {
		var item StepRecovery
		var checkpoint sql.NullInt64
		var heartbeat, expires, deadline sql.NullString
		if err := rows.Scan(&item.StepID, &item.AgentName, &item.Status, &item.LeaseVersion, &checkpoint, &item.Attempt, &heartbeat, &expires, &deadline); err != nil {
			rows.Close()
			return Timeline{}, err
		}
		if checkpoint.Valid {
			item.CheckpointID = &checkpoint.Int64
		}
		item.LastHeartbeatAt, _ = parseNullableTime(heartbeat)
		item.LeaseExpiresAt, _ = parseNullableTime(expires)
		item.ResumeDeadline, _ = parseNullableTime(deadline)
		result.Steps = append(result.Steps, item)
	}
	rows.Close()
	rows, err = s.db.QueryContext(ctx, `select lease_id from task_step_leases where task_id=? order by id desc`, taskID)
	if err != nil {
		return Timeline{}, err
	}
	var leaseIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return Timeline{}, err
		}
		leaseIDs = append(leaseIDs, id)
	}
	rows.Close()
	for _, id := range leaseIDs {
		lease, err := loadLease(ctx, s.db, id)
		if err != nil {
			return Timeline{}, err
		}
		result.Leases = append(result.Leases, lease)
	}
	rows, err = s.db.QueryContext(ctx, `select id,task_id,step_id,lease_id,idempotency_key,git_commit,branch_name,clean,summary_hash,high_risk,created_at,summary_json,files_json,tests_json from task_checkpoints where task_id=? order by id desc`, taskID)
	if err != nil {
		return Timeline{}, err
	}
	for rows.Next() {
		var item TimelineCheckpoint
		var clean, highRisk int
		var created, summaryJSON, filesJSON, testsJSON string
		if err := rows.Scan(&item.ID, &item.TaskID, &item.StepID, &item.LeaseID, &item.IdempotencyKey, &item.GitCommit, &item.BranchName, &clean, &item.SummaryHash, &highRisk, &created, &summaryJSON, &filesJSON, &testsJSON); err != nil {
			rows.Close()
			return Timeline{}, err
		}
		item.Clean = clean == 1
		item.HighRisk = highRisk == 1
		item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		_ = json.Unmarshal([]byte(summaryJSON), &item.Summary)
		_ = json.Unmarshal([]byte(filesJSON), &item.Files)
		_ = json.Unmarshal([]byte(testsJSON), &item.Tests)
		result.Checkpoints = append(result.Checkpoints, item)
	}
	rows.Close()
	rows, err = s.db.QueryContext(ctx, `select id,step_id,from_agent,to_agent,checkpoint_id,attempt,reason,status,from_branch,from_worktree,to_branch,to_worktree,created_by,created_at from step_reassignments where task_id=? order by id desc`, taskID)
	if err != nil {
		return Timeline{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var item Reassignment
		var checkpoint sql.NullInt64
		var created string
		if err := rows.Scan(&item.ID, &item.StepID, &item.FromAgent, &item.ToAgent, &checkpoint, &item.Attempt, &item.Reason, &item.Status, &item.FromBranch, &item.FromWorktree, &item.ToBranch, &item.ToWorktree, &item.CreatedBy, &created); err != nil {
			return Timeline{}, err
		}
		if checkpoint.Valid {
			item.CheckpointID = &checkpoint.Int64
		}
		item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		result.Reassignments = append(result.Reassignments, item)
	}
	return result, rows.Err()
}
