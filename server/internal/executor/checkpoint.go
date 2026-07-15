package executor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"wanxiang-agent/server/internal/gitx"
	"wanxiang-agent/server/internal/leases"
)

type CheckpointCreator interface {
	CreateCheckpoint(context.Context, leases.LeaseRef, leases.CheckpointInput) (leases.Checkpoint, error)
}
type WorkerSummary struct {
	Completed                  []string
	NextAction                 string
	Decisions, Blockers, Risks []string
	Tests                      []leases.CheckpointTest
}
type CheckpointRunner struct {
	db         *sql.DB
	authorizer LeaseAuthorizer
	creator    CheckpointCreator
}

func NewCheckpointRunner(db *sql.DB, authorizer LeaseAuthorizer, creator CheckpointCreator) *CheckpointRunner {
	return &CheckpointRunner{db: db, authorizer: authorizer, creator: creator}
}

func (r *CheckpointRunner) CreateGitCheckpoint(ctx context.Context, ref leases.LeaseRef, summary WorkerSummary) (leases.Checkpoint, error) {
	if len(summary.Completed) == 0 || strings.TrimSpace(summary.NextAction) == "" {
		return leases.Checkpoint{}, errors.New("checkpoint summary is incomplete")
	}
	var root, branch string
	if err := r.db.QueryRowContext(ctx, `select worktree_path,branch_name from project_workspaces where task_id=? and step_id=? and agent_name=? and status='ready'`, ref.TaskID, ref.StepID, ref.AgentName).Scan(&root, &branch); err != nil {
		return leases.Checkpoint{}, leases.ErrConflict
	}
	status, err := gitx.Run(ctx, root, "status", "--porcelain")
	if err != nil || strings.TrimSpace(status) == "" {
		return leases.Checkpoint{}, errors.New("checkpoint has no controlled changes")
	}
	var changed []string
	for _, line := range strings.Split(strings.TrimRight(status, "\r\n"), "\n") {
		if len(line) < 4 {
			return leases.Checkpoint{}, errors.New("invalid git status")
		}
		path := strings.TrimSpace(line[3:])
		if strings.Contains(path, " -> ") {
			path = strings.TrimSpace(strings.SplitN(path, " -> ", 2)[1])
		}
		if _, err := validateWorkerPath(path); err != nil {
			return leases.Checkpoint{}, err
		}
		if err := r.authorizer.Authorize(ctx, ref, path); err != nil {
			return leases.Checkpoint{}, fmt.Errorf("checkpoint path %s: %w", path, leases.ErrConflict)
		}
		changed = append(changed, path)
	}
	args := append([]string{"add", "--"}, changed...)
	if out, err := gitx.Run(ctx, root, args...); err != nil {
		return leases.Checkpoint{}, fmt.Errorf("stage checkpoint: %s", Redact(out))
	}
	message := fmt.Sprintf("checkpoint(%d): %s", ref.StepID, strings.TrimSpace(summary.Completed[0]))
	if out, err := gitx.Run(ctx, root, "commit", "-m", message); err != nil {
		return leases.Checkpoint{}, fmt.Errorf("commit checkpoint: %s", Redact(out))
	}
	head, err := gitx.Run(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return leases.Checkpoint{}, err
	}
	head = strings.TrimSpace(head)
	input := leases.CheckpointInput{IdempotencyKey: "executor-" + head, GitCommit: head, BranchName: branch, Clean: true, Files: changed, Tests: summary.Tests, Summary: leases.RecoverySummary{Completed: summary.Completed, NextAction: summary.NextAction, FilesChanged: changed, Decisions: summary.Decisions, Blockers: summary.Blockers, Risks: summary.Risks}}
	return r.creator.CreateCheckpoint(ctx, ref, input)
}
