package leases

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"wanxiang-agent/server/internal/files"
	"wanxiang-agent/server/internal/gitx"
)

type CheckpointTest struct {
	Command string `json:"command"`
	Result  string `json:"result"`
}

type CheckpointInput struct {
	IdempotencyKey string           `json:"idempotency_key"`
	GitCommit      string           `json:"git_commit"`
	BranchName     string           `json:"branch_name"`
	Clean          bool             `json:"clean"`
	Files          []string         `json:"files"`
	Tests          []CheckpointTest `json:"tests"`
	Summary        RecoverySummary  `json:"summary"`
	HighRisk       bool             `json:"high_risk"`
}

// CreateCheckpoint 校验租约与 Git 状态后持久化检查点。
func (s *Service) CreateCheckpoint(ctx context.Context, ref LeaseRef, input CheckpointInput) (Checkpoint, error) {
	return s.createCheckpoint(ctx, 0, ref, input)
}

// CreateCheckpointUnderProjectLock 在调用方已持有指定项目锁时持久化检查点。
func (s *Service) CreateCheckpointUnderProjectLock(ctx context.Context, projectID int64, ref LeaseRef, input CheckpointInput) (Checkpoint, error) {
	if projectID <= 0 {
		return Checkpoint{}, ErrConflict
	}
	return s.createCheckpoint(ctx, projectID, ref, input)
}

func (s *Service) createCheckpoint(ctx context.Context, lockedProjectID int64, ref LeaseRef, input CheckpointInput) (Checkpoint, error) {
	var projectID int64
	if err := s.db.QueryRowContext(ctx, `select t.project_id from tasks t
		join task_steps ts on ts.task_id=t.id where t.id=? and ts.id=? and t.project_id is not null`,
		ref.TaskID, ref.StepID).Scan(&projectID); err != nil {
		return Checkpoint{}, ErrConflict
	}
	if lockedProjectID == 0 {
		if s.dataDir == "" {
			return Checkpoint{}, errors.New("checkpoint project lock is unavailable")
		}
		releaseProjectLock, err := gitx.AcquireProjectLock(ctx, s.dataDir, projectID)
		if err != nil {
			return Checkpoint{}, fmt.Errorf("acquire project git lock: %w", err)
		}
		defer releaseProjectLock()
		lockedProjectID = projectID
	}
	if err := s.db.QueryRowContext(ctx, `select t.project_id from tasks t
		join task_steps ts on ts.task_id=t.id where t.id=? and ts.id=? and t.project_id is not null`,
		ref.TaskID, ref.StepID).Scan(&projectID); err != nil || projectID != lockedProjectID {
		return Checkpoint{}, ErrConflict
	}
	if err := s.validateActiveRef(ctx, ref); err != nil {
		return Checkpoint{}, err
	}
	if existing, err := s.checkpointByKey(ctx, ref.LeaseID, input.IdempotencyKey); err == nil {
		return existing, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Checkpoint{}, err
	}
	if strings.TrimSpace(input.IdempotencyKey) == "" || len(input.IdempotencyKey) > 200 || hasControlCharacter(input.IdempotencyKey) {
		return Checkpoint{}, errors.New("invalid idempotency key")
	}
	summary, summaryJSON, err := normalizeSummary(input.Summary)
	if err != nil {
		return Checkpoint{}, err
	}
	input.Summary = summary
	if err := validateCheckpointTests(input.Tests); err != nil {
		return Checkpoint{}, err
	}
	for _, path := range append(append([]string{}, input.Files...), summary.FilesChanged...) {
		if err := s.Authorize(ctx, ref, path); err != nil {
			return Checkpoint{}, ErrConflict
		}
	}

	var workspacePath, branchName, baseCommit, provisionCommit, workspaceStatus, owner string
	var workspaceProjectID int64
	err = s.db.QueryRowContext(ctx, `select project_id,worktree_path,branch_name,base_commit,provision_commit,status,agent_name
		from project_workspaces where task_id=? and step_id=?`, ref.TaskID, ref.StepID).
		Scan(&workspaceProjectID, &workspacePath, &branchName, &baseCommit, &provisionCommit, &workspaceStatus, &owner)
	if err != nil || workspaceProjectID != lockedProjectID || workspaceStatus != "ready" || owner != ref.AgentName {
		return Checkpoint{}, ErrConflict
	}
	if err := validateGitCheckpoint(ctx, workspacePath, branchName, baseCommit, provisionCommit, ref.StepID, input); err != nil {
		return Checkpoint{}, err
	}

	filesJSON, _ := json.Marshal(normalizeItems(input.Files))
	testsJSON, _ := json.Marshal(input.Tests)
	hashValue := sha256.Sum256(summaryJSON)
	hash := hex.EncodeToString(hashValue[:])
	now := s.clock.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Checkpoint{}, err
	}
	defer tx.Rollback()
	var currentID, currentAgent, currentStatus string
	var currentVersion int64
	err = tx.QueryRowContext(ctx, `select lease_id,lease_version,agent_name,status from task_step_leases where lease_id=?`, ref.LeaseID).Scan(&currentID, &currentVersion, &currentAgent, &currentStatus)
	if err != nil || currentID != ref.LeaseID || currentVersion != ref.LeaseVersion || currentAgent != ref.AgentName || currentStatus != string(LeaseActive) {
		return Checkpoint{}, ErrConflict
	}
	result, err := tx.ExecContext(ctx, `insert into task_checkpoints(task_id,step_id,lease_id,idempotency_key,git_commit,branch_name,clean,files_json,tests_json,summary_json,summary_hash,high_risk,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?)`, ref.TaskID, ref.StepID, ref.LeaseID, input.IdempotencyKey, input.GitCommit, input.BranchName, boolInt(input.Clean), string(filesJSON), string(testsJSON), string(summaryJSON), hash, boolInt(input.HighRisk), formatTime(now))
	if err != nil {
		return Checkpoint{}, err
	}
	id, _ := result.LastInsertId()
	if _, err = tx.ExecContext(ctx, `update task_steps set checkpoint_id=?,status='checkpointed' where task_id=? and id=? and lease_id=? and lease_version=?`, id, ref.TaskID, ref.StepID, ref.LeaseID, ref.LeaseVersion); err != nil {
		return Checkpoint{}, err
	}
	payload, _ := json.Marshal(map[string]any{"checkpoint_id": id, "step_id": ref.StepID, "clean": input.Clean, "summary_hash": hash})
	if _, err = tx.ExecContext(ctx, `insert into runtime_events(task_id,event_type,actor,payload_json,created_at) values(?,'task.step.checkpointed',?,?,?)`, ref.TaskID, ref.AgentName, string(payload), formatTime(now)); err != nil {
		return Checkpoint{}, err
	}
	mirrorPath, err := files.UnderRoot(workspacePath, filepath.Join(workspacePath, ".wanxiang", "checkpoints", fmt.Sprintf("%d", ref.StepID), fmt.Sprintf("%d.yaml", id)))
	if err != nil {
		return Checkpoint{}, fmt.Errorf("unsafe checkpoint mirror path: %w", err)
	}
	if err = writeSummaryMirror(mirrorPath, ref, id, input, summaryJSON, hash); err != nil {
		return Checkpoint{}, err
	}
	if err = tx.Commit(); err != nil {
		persisted, found, confirmErr := s.confirmCheckpointCommit(ref.LeaseID, input.IdempotencyKey)
		if found {
			return persisted, nil
		}
		if confirmErr != nil {
			return Checkpoint{}, errors.Join(err, fmt.Errorf("checkpoint commit outcome is uncertain: %w", confirmErr))
		}
		_ = os.Remove(mirrorPath)
		return Checkpoint{}, err
	}
	return Checkpoint{ID: id, TaskID: ref.TaskID, StepID: ref.StepID, LeaseID: ref.LeaseID, IdempotencyKey: input.IdempotencyKey, GitCommit: input.GitCommit, BranchName: input.BranchName, Clean: input.Clean, SummaryHash: hash, HighRisk: input.HighRisk, CreatedAt: now}, nil
}

func (s *Service) confirmCheckpointCommit(leaseID, idempotencyKey string) (Checkpoint, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for attempt := 0; attempt < 2; attempt++ {
		checkpoint, err := s.checkpointByKey(ctx, leaseID, idempotencyKey)
		if err == nil {
			return checkpoint, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Checkpoint{}, false, err
		}
		if attempt == 0 {
			timer := time.NewTimer(25 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return Checkpoint{}, false, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return Checkpoint{}, false, nil
}

// GetCheckpoint 按编号查询检查点基础信息。
func (s *Service) GetCheckpoint(ctx context.Context, checkpointID int64) (Checkpoint, error) {
	return scanCheckpoint(s.db.QueryRowContext(ctx, `select id,task_id,step_id,lease_id,idempotency_key,git_commit,branch_name,clean,summary_hash,high_risk,created_at from task_checkpoints where id=?`, checkpointID))
}

func (s *Service) checkpointByKey(ctx context.Context, leaseID, key string) (Checkpoint, error) {
	if leaseID == "" || key == "" {
		return Checkpoint{}, sql.ErrNoRows
	}
	return scanCheckpoint(s.db.QueryRowContext(ctx, `select id,task_id,step_id,lease_id,idempotency_key,git_commit,branch_name,clean,summary_hash,high_risk,created_at from task_checkpoints where lease_id=? and idempotency_key=?`, leaseID, key))
}

func scanCheckpoint(row *sql.Row) (Checkpoint, error) {
	var result Checkpoint
	var clean, highRisk int
	var created string
	err := row.Scan(&result.ID, &result.TaskID, &result.StepID, &result.LeaseID, &result.IdempotencyKey, &result.GitCommit, &result.BranchName, &clean, &result.SummaryHash, &highRisk, &created)
	if err != nil {
		return Checkpoint{}, err
	}
	result.Clean = clean == 1
	result.HighRisk = highRisk == 1
	result.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	return result, err
}

func (s *Service) validateActiveRef(ctx context.Context, ref LeaseRef) error {
	lease, err := loadLease(ctx, s.db, ref.LeaseID)
	if err != nil || !sameRef(lease.LeaseRef, ref) || lease.Status != LeaseActive || !s.clock.Now().UTC().Before(lease.ExpiresAt) {
		return ErrConflict
	}
	return nil
}

func validateGitCheckpoint(ctx context.Context, path, storedBranch, base, provision string, stepID int64, input CheckpointInput) error {
	branch, err := gitValue(ctx, path, "branch", "--show-current")
	if err != nil || branch != storedBranch || input.BranchName != storedBranch {
		return errors.New("checkpoint branch mismatch")
	}
	status, err := gitValue(ctx, path, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return err
	}
	if input.Clean {
		if input.GitCommit == "" || checkpointStatusDirty(status, stepID) {
			return errors.New("clean checkpoint does not match worktree")
		}
		head, err := gitValue(ctx, path, "rev-parse", "HEAD")
		if err != nil || head != input.GitCommit {
			return errors.New("checkpoint commit is not worktree HEAD")
		}
		baseline := provision
		if baseline == "" {
			baseline = base
		}
		if baseline == "" {
			return errors.New("checkpoint baseline is missing")
		}
		if out, err := gitx.Run(ctx, path, "merge-base", "--is-ancestor", baseline, input.GitCommit); err != nil {
			return fmt.Errorf("checkpoint is not baseline descendant: %s", strings.TrimSpace(out))
		}
		return nil
	}
	if input.GitCommit != "" || status == "" {
		return errors.New("context checkpoint must describe a dirty worktree without commit")
	}
	return nil
}

func checkpointStatusDirty(status string, stepID int64) bool {
	prefix := filepath.ToSlash(filepath.Join(".wanxiang", "checkpoints", fmt.Sprintf("%d", stepID))) + "/"
	for _, line := range strings.Split(status, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) < 4 {
			return true
		}
		path := strings.TrimSpace(line[3:])
		if strings.Contains(path, " -> ") {
			path = strings.TrimSpace(strings.SplitN(path, " -> ", 2)[1])
		}
		path = filepath.ToSlash(path)
		if strings.HasPrefix(path, prefix) && strings.HasSuffix(strings.ToLower(path), ".yaml") {
			continue
		}
		return true
	}
	return false
}

func gitValue(ctx context.Context, path string, args ...string) (string, error) {
	out, err := gitx.Run(ctx, path, args...)
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(out))
	}
	return strings.TrimSpace(out), nil
}

func validateCheckpointTests(tests []CheckpointTest) error {
	for _, test := range tests {
		if strings.TrimSpace(test.Command) == "" || len(test.Command) > maxSummaryItem || len(test.Result) > maxSummaryItem || hasControlCharacter(test.Command) || hasControlCharacter(test.Result) || sensitiveSummary.MatchString(test.Command) || sensitiveSummary.MatchString(test.Result) {
			return errors.New("checkpoint test contains unsafe content")
		}
	}
	return nil
}

func writeSummaryMirror(path string, ref LeaseRef, checkpointID int64, input CheckpointInput, summaryJSON []byte, hash string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var summary any
	if err := json.Unmarshal(summaryJSON, &summary); err != nil {
		return err
	}
	content, err := json.MarshalIndent(map[string]any{
		"checkpoint_id": checkpointID,
		"task_id":       ref.TaskID,
		"step_id":       ref.StepID,
		"git_commit":    input.GitCommit,
		"branch_name":   input.BranchName,
		"clean":         input.Clean,
		"files":         normalizeItems(input.Files),
		"tests":         input.Tests,
		"summary":       summary,
		"summary_hash":  hash,
		"high_risk":     input.HighRisk,
	}, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return os.WriteFile(path, content, 0o600)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
