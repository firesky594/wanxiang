package executor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"wanxiang-agent/server/internal/gitx"
	"wanxiang-agent/server/internal/leases"
)

var errNoControlledChanges = errors.New("checkpoint has no controlled changes")

type CheckpointCreator interface {
	CreateCheckpoint(context.Context, leases.LeaseRef, leases.CheckpointInput) (leases.Checkpoint, error)
}
type projectLockedCheckpointCreator interface {
	CreateCheckpointUnderProjectLock(context.Context, int64, leases.LeaseRef, leases.CheckpointInput) (leases.Checkpoint, error)
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
	dataDir    string
}

// NewCheckpointRunner 创建 Git 检查点执行器。
func NewCheckpointRunner(db *sql.DB, authorizer LeaseAuthorizer, creator CheckpointCreator, dataDirs ...string) *CheckpointRunner {
	dataDir := ""
	if len(dataDirs) > 0 {
		dataDir = dataDirs[0]
	}
	return &CheckpointRunner{db: db, authorizer: authorizer, creator: creator, dataDir: dataDir}
}

// CreateGitCheckpoint 校验租约与工作区后创建 Git 检查点。
func (r *CheckpointRunner) CreateGitCheckpoint(ctx context.Context, ref leases.LeaseRef, summary WorkerSummary) (leases.Checkpoint, error) {
	if len(summary.Completed) == 0 || strings.TrimSpace(summary.NextAction) == "" {
		return leases.Checkpoint{}, errors.New("checkpoint summary is incomplete")
	}
	var projectID int64
	if err := r.db.QueryRowContext(ctx, `select project_id from tasks where id=? and project_id is not null`, ref.TaskID).Scan(&projectID); err != nil {
		return leases.Checkpoint{}, leases.ErrConflict
	}
	if strings.TrimSpace(r.dataDir) == "" {
		return leases.Checkpoint{}, errors.New("checkpoint project lock is unavailable")
	}
	releaseProjectLock, err := gitx.AcquireProjectLock(ctx, r.dataDir, projectID)
	if err != nil {
		return leases.Checkpoint{}, err
	}
	defer releaseProjectLock()

	var root, branch string
	var taskProjectID, workspaceProjectID int64
	if err := r.db.QueryRowContext(ctx, `select t.project_id,pw.project_id,pw.worktree_path,pw.branch_name
		from tasks t join project_workspaces pw on pw.task_id=t.id
		where t.id=? and pw.step_id=? and pw.agent_name=? and pw.status='ready'`,
		ref.TaskID, ref.StepID, ref.AgentName).
		Scan(&taskProjectID, &workspaceProjectID, &root, &branch); err != nil ||
		taskProjectID != projectID || workspaceProjectID != projectID {
		return leases.Checkpoint{}, leases.ErrConflict
	}
	status, err := gitx.Run(ctx, root, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return leases.Checkpoint{}, err
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
		if checkpointMirrorPath(path, ref.StepID) {
			continue
		}
		if _, err := validateWorkerPath(path); err != nil {
			return leases.Checkpoint{}, err
		}
		if err := r.authorizer.Authorize(ctx, ref, path); err != nil {
			return leases.Checkpoint{}, fmt.Errorf("checkpoint path %s: %w", path, leases.ErrConflict)
		}
		changed = append(changed, path)
	}
	if len(changed) == 0 {
		return leases.Checkpoint{}, errNoControlledChanges
	}
	originalHead, err := gitx.Run(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return leases.Checkpoint{}, err
	}
	originalHead = strings.TrimSpace(originalHead)
	originalIndex, err := gitx.Run(ctx, root, "write-tree")
	if err != nil {
		return leases.Checkpoint{}, err
	}
	originalIndex = strings.TrimSpace(originalIndex)
	args := append([]string{"--literal-pathspecs", "add", "--"}, changed...)
	if out, err := gitx.Run(ctx, root, args...); err != nil {
		_ = restoreCheckpointIndexBounded(root, originalIndex)
		return leases.Checkpoint{}, fmt.Errorf("stage checkpoint: %s", Redact(out))
	}
	message := fmt.Sprintf("checkpoint(%d): %s", ref.StepID, strings.TrimSpace(summary.Completed[0]))
	if out, err := gitx.Run(ctx, root, "commit", "-m", message); err != nil {
		rollbackErr := rollbackCheckpointGitBounded(root, originalHead, "", originalIndex)
		if rollbackErr != nil {
			return leases.Checkpoint{}, errors.Join(fmt.Errorf("commit checkpoint: %s", Redact(out)), rollbackErr)
		}
		return leases.Checkpoint{}, fmt.Errorf("commit checkpoint: %s", Redact(out))
	}
	head, err := gitx.Run(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return leases.Checkpoint{}, err
	}
	head = strings.TrimSpace(head)
	input := leases.CheckpointInput{IdempotencyKey: "executor-" + head, GitCommit: head, BranchName: branch, Clean: true, Files: changed, Tests: summary.Tests, Summary: leases.RecoverySummary{Completed: summary.Completed, NextAction: summary.NextAction, FilesChanged: changed, Decisions: summary.Decisions, Blockers: summary.Blockers, Risks: summary.Risks}}
	var checkpoint leases.Checkpoint
	var createErr error
	if creator, ok := r.creator.(projectLockedCheckpointCreator); ok {
		checkpoint, createErr = creator.CreateCheckpointUnderProjectLock(ctx, projectID, ref, input)
	} else {
		checkpoint, createErr = r.creator.CreateCheckpoint(ctx, ref, input)
	}
	if createErr == nil {
		return checkpoint, nil
	}
	persisted, found, persistenceErr := r.confirmCheckpointPersistence(ref.LeaseID, input.IdempotencyKey)
	if found {
		return persisted, nil
	}
	if persistenceErr != nil {
		return leases.Checkpoint{}, errors.Join(createErr, fmt.Errorf("checkpoint persistence state is uncertain: %w", persistenceErr))
	}
	if rollbackErr := rollbackCheckpointGitBounded(root, originalHead, head, originalIndex); rollbackErr != nil {
		return leases.Checkpoint{}, errors.Join(createErr, rollbackErr)
	}
	return leases.Checkpoint{}, createErr
}

func (r *CheckpointRunner) confirmCheckpointPersistence(leaseID, idempotencyKey string) (leases.Checkpoint, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	absentReads := 0
	for {
		checkpoint, err := r.persistedCheckpoint(ctx, leaseID, idempotencyKey)
		if err == nil {
			return checkpoint, true, nil
		}
		if errors.Is(err, sql.ErrNoRows) {
			absentReads++
			if absentReads >= 2 {
				return leases.Checkpoint{}, false, nil
			}
		} else if !sqliteBusy(err) {
			return leases.Checkpoint{}, false, err
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return leases.Checkpoint{}, false, ctx.Err()
		case <-timer.C:
		}
	}
}

func (r *CheckpointRunner) persistedCheckpoint(ctx context.Context, leaseID, idempotencyKey string) (leases.Checkpoint, error) {
	var checkpoint leases.Checkpoint
	var clean, highRisk int
	var createdAt string
	err := r.db.QueryRowContext(ctx, `select id,task_id,step_id,lease_id,idempotency_key,git_commit,branch_name,clean,summary_hash,high_risk,created_at
		from task_checkpoints where lease_id=? and idempotency_key=?`, leaseID, idempotencyKey).
		Scan(&checkpoint.ID, &checkpoint.TaskID, &checkpoint.StepID, &checkpoint.LeaseID, &checkpoint.IdempotencyKey,
			&checkpoint.GitCommit, &checkpoint.BranchName, &clean, &checkpoint.SummaryHash, &highRisk, &createdAt)
	if err != nil {
		return leases.Checkpoint{}, err
	}
	checkpoint.Clean = clean == 1
	checkpoint.HighRisk = highRisk == 1
	checkpoint.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	return checkpoint, err
}

func rollbackCheckpointGitBounded(root, originalHead, expectedHead, originalIndex string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return rollbackCheckpointGit(ctx, root, originalHead, expectedHead, originalIndex)
}

func rollbackCheckpointGit(ctx context.Context, root, originalHead, expectedHead, originalIndex string) error {
	currentHead, err := gitx.Run(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("inspect checkpoint rollback head: %w", err)
	}
	currentHead = strings.TrimSpace(currentHead)
	if currentHead != originalHead {
		if expectedHead != "" && currentHead != expectedHead {
			return fmt.Errorf("checkpoint rollback head changed: got %s want %s", currentHead, expectedHead)
		}
		if _, err := gitx.Run(ctx, root, "reset", "--mixed", originalHead); err != nil {
			return fmt.Errorf("rollback checkpoint commit: %w", err)
		}
	}
	return restoreCheckpointIndex(ctx, root, originalIndex)
}

func restoreCheckpointIndexBounded(root, originalIndex string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return restoreCheckpointIndex(ctx, root, originalIndex)
}

func restoreCheckpointIndex(ctx context.Context, root, originalIndex string) error {
	if strings.TrimSpace(originalIndex) == "" {
		return errors.New("checkpoint index snapshot is missing")
	}
	if _, err := gitx.Run(ctx, root, "read-tree", originalIndex); err != nil {
		return fmt.Errorf("restore checkpoint index: %w", err)
	}
	return nil
}

func checkpointMirrorPath(path string, stepID int64) bool {
	path = filepath.ToSlash(strings.TrimSpace(path))
	prefix := filepath.ToSlash(filepath.Join(".wanxiang", "checkpoints", fmt.Sprintf("%d", stepID))) + "/"
	return strings.HasPrefix(path, prefix) && strings.HasSuffix(strings.ToLower(path), ".yaml")
}
