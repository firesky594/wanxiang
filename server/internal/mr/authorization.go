package mr

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"wanxiang-agent/server/internal/gitx"
)

type submissionContext struct {
	ProjectLead  string
	WorktreePath string
}

func (s *Service) authorizeSubmission(ctx context.Context, principal Principal, input CompletionReportInput) (submissionContext, error) {
	if err := input.Validate(); err != nil {
		return submissionContext{}, err
	}
	if principal.Name != input.AgentName || principal.Role != input.Role || principal.Name == "" || principal.Role == "" {
		return submissionContext{}, ErrIdentityMismatch
	}
	var owner, leaseID, leaseStatus, branch, worktree, checkpointCommit, checkpointBranch, projectLead, expiresAt string
	var leaseVersion, projectID int64
	err := s.db.QueryRowContext(ctx, `select ta.agent_name,l.lease_id,l.lease_version,l.status,l.expires_at,pw.project_id,pw.branch_name,pw.worktree_path,coalesce(cp.git_commit,''),coalesce(cp.branch_name,''),coalesce(td.project_lead,'')
		from task_assignments ta
		join task_step_leases l on l.task_id=ta.task_id and l.step_id=ta.step_id and l.agent_name=ta.agent_name
		join project_workspaces pw on pw.task_id=ta.task_id and pw.step_id=ta.step_id and pw.agent_name=ta.agent_name
		left join task_checkpoints cp on cp.id=(select id from task_checkpoints where task_id=ta.task_id and step_id=ta.step_id and lease_id=l.lease_id order by id desc limit 1)
		left join team_decisions td on td.task_id=ta.task_id
		where ta.task_id=? and ta.step_id=?`, input.TaskID, input.StepID).Scan(&owner, &leaseID, &leaseVersion, &leaseStatus, &expiresAt, &projectID, &branch, &worktree, &checkpointCommit, &checkpointBranch, &projectLead)
	if errors.Is(err, sql.ErrNoRows) || owner != principal.Name {
		return submissionContext{}, ErrIdentityMismatch
	}
	if err != nil {
		return submissionContext{}, err
	}
	expires, parseErr := time.Parse(time.RFC3339Nano, expiresAt)
	if leaseID != input.LeaseID || leaseVersion != input.LeaseVersion || leaseStatus != "active" || parseErr != nil || !time.Now().UTC().Before(expires) {
		return submissionContext{}, ErrLeaseInvalid
	}
	if projectID != input.ProjectID || branch != input.SourceBranch || checkpointBranch != input.SourceBranch {
		return submissionContext{}, ErrBranchOwnership
	}
	if checkpointCommit == "" || checkpointCommit != input.CheckpointCommit || checkpointCommit != input.HeadCommit {
		return submissionContext{}, ErrCheckpointMismatch
	}
	head, gitErr := gitx.Run(ctx, worktree, "rev-parse", "HEAD")
	if gitErr != nil || strings.TrimSpace(head) != input.HeadCommit {
		return submissionContext{}, ErrCheckpointMismatch
	}
	if projectLead == "" {
		return submissionContext{}, ErrIdentityMismatch
	}
	return submissionContext{ProjectLead: projectLead, WorktreePath: worktree}, nil
}
