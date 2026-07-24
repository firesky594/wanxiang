package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"wanxiang-agent/server/internal/gitx"
	"wanxiang-agent/server/internal/leases"
	"wanxiang-agent/server/internal/mr"
)

// DatabaseCompletionReporter 使用数据库中的租约、工作区与检查点生成完成报告。
type DatabaseCompletionReporter struct {
	db      *sql.DB
	service *mr.Service
}

// NewDatabaseCompletionReporter 创建仅从受控数据库生成完成报告的提交器。
func NewDatabaseCompletionReporter(db *sql.DB, service *mr.Service) *DatabaseCompletionReporter {
	return &DatabaseCompletionReporter{db: db, service: service}
}

// SubmitCompleted 实时核验工作区后从当前租约的干净检查点生成报告并送审。
func (r *DatabaseCompletionReporter) SubmitCompleted(ctx context.Context, ref leases.LeaseRef) error {
	if r == nil || r.db == nil || r.service == nil {
		return errors.New("completion reporter is unavailable")
	}

	var (
		projectID            int64
		checkpointID         int64
		role, branch, commit string
		worktreePath         string
		filesJSON, testsJSON string
		summaryJSON          string
		checkpointCreatedAt  string
		highRisk             int
	)
	err := r.db.QueryRowContext(ctx, `select t.project_id,
			coalesce(nullif(ar.role,''),nullif(ts.kind,''),'agent'),
			pw.branch_name,pw.worktree_path,cp.id,cp.git_commit,cp.files_json,cp.tests_json,cp.summary_json,cp.created_at,cp.high_risk
		from task_steps ts
		join tasks t on t.id=ts.task_id
		join task_assignments ta on ta.task_id=ts.task_id and ta.step_id=ts.id and ta.agent_name=ts.agent_name
		join project_workspaces pw on pw.task_id=ts.task_id and pw.step_id=ts.id and pw.agent_name=ts.agent_name
		join task_step_leases l on l.task_id=ts.task_id and l.step_id=ts.id and l.agent_name=ts.agent_name
		join task_checkpoints cp on cp.id=ts.checkpoint_id and cp.task_id=ts.task_id and cp.step_id=ts.id and cp.lease_id=l.lease_id
		left join agent_registry ar on ar.name=ts.agent_name
		where ts.task_id=? and ts.id=? and ts.agent_name=?
			and l.lease_id=? and l.lease_version=? and l.status='active'
			and cp.clean=1 and cp.git_commit<>'' and cp.branch_name=pw.branch_name`,
		ref.TaskID, ref.StepID, ref.AgentName, ref.LeaseID, ref.LeaseVersion).
		Scan(&projectID, &role, &branch, &worktreePath, &checkpointID, &commit, &filesJSON, &testsJSON, &summaryJSON, &checkpointCreatedAt, &highRisk)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("clean checkpoint is unavailable")
	}
	if err != nil {
		return err
	}
	if err := validateCompletionWorktree(ctx, worktreePath, branch, commit, ref.StepID, checkpointID); err != nil {
		return err
	}

	var summary leases.RecoverySummary
	if err := json.Unmarshal([]byte(summaryJSON), &summary); err != nil || len(summary.Completed) == 0 {
		return errors.New("checkpoint summary is invalid")
	}
	var keyFiles []string
	if err := json.Unmarshal([]byte(filesJSON), &keyFiles); err != nil {
		return errors.New("checkpoint files are invalid")
	}
	var checkpointTests []leases.CheckpointTest
	if err := json.Unmarshal([]byte(testsJSON), &checkpointTests); err != nil {
		return errors.New("checkpoint tests are invalid")
	}
	if err := validateCompletionTestEvidence(ctx, r.db, ref, checkpointCreatedAt, checkpointTests); err != nil {
		return err
	}
	tests := make([]mr.TestEvidence, 0, len(checkpointTests))
	for _, item := range checkpointTests {
		tests = append(tests, mr.TestEvidence{Command: item.Command, Status: item.Result})
	}
	risks := append([]string(nil), summary.Risks...)
	if highRisk == 1 {
		risks = append(risks, "检查点已标记为高风险，需负责人重点评审")
	}

	dependencies, err := r.dependencies(ctx, ref.TaskID, ref.StepID)
	if err != nil {
		return err
	}
	input := mr.CompletionReportInput{
		AgentName:        ref.AgentName,
		Role:             role,
		ProjectID:        projectID,
		TaskID:           ref.TaskID,
		StepID:           ref.StepID,
		LeaseID:          ref.LeaseID,
		LeaseVersion:     ref.LeaseVersion,
		SourceBranch:     branch,
		CheckpointCommit: commit,
		HeadCommit:       commit,
		Completed:        summary.Completed,
		Incomplete:       summary.Blockers,
		KeyFiles:         keyFiles,
		Tests:            tests,
		Risks:            risks,
		Dependencies:     dependencies,
	}
	_, err = r.service.SubmitReport(ctx, mr.Principal{Name: ref.AgentName, Role: role}, input)
	return err
}

func validateCompletionTestEvidence(ctx context.Context, db *sql.DB, ref leases.LeaseRef, checkpointCreatedAt string, tests []leases.CheckpointTest) error {
	if len(tests) == 0 {
		return errors.New("completion requires passing run_check evidence")
	}
	checkpointTime, err := time.Parse(time.RFC3339Nano, checkpointCreatedAt)
	if err != nil {
		return errors.New("checkpoint test timestamp is invalid")
	}
	seen := make(map[string]struct{}, len(tests))
	for _, item := range tests {
		command := strings.TrimSpace(item.Command)
		if command == "" || command != item.Command || item.Result != "passed" {
			return errors.New("completion requires passing run_check evidence")
		}
		if _, exists := seen[command]; exists {
			continue
		}
		seen[command] = struct{}{}
		rows, err := db.QueryContext(ctx, `select ea.created_at
			from executor_actions ea
			join executor_runs er on er.id=ea.run_id
			where er.task_id=? and er.step_id=? and er.agent_name=?
				and er.lease_id=? and er.lease_version=?
				and ea.action_type='run_check' and ea.relative_path=? and ea.status='passed'
			order by ea.id`,
			ref.TaskID, ref.StepID, ref.AgentName, ref.LeaseID, ref.LeaseVersion, command)
		if err != nil {
			return err
		}
		backed := false
		for rows.Next() {
			var createdAt string
			if err := rows.Scan(&createdAt); err != nil {
				rows.Close()
				return err
			}
			actionTime, parseErr := time.Parse(time.RFC3339Nano, createdAt)
			if parseErr == nil && !actionTime.After(checkpointTime) {
				backed = true
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if !backed {
			return errors.New("checkpoint test evidence is not backed by run_check")
		}
	}
	return nil
}

func validateCompletionWorktree(ctx context.Context, worktreePath, expectedBranch, expectedCommit string, stepID, checkpointID int64) error {
	branch, err := gitx.Run(ctx, worktreePath, "branch", "--show-current")
	if err != nil || strings.TrimSpace(branch) != expectedBranch {
		return errors.New("completion worktree branch mismatch")
	}
	head, err := gitx.Run(ctx, worktreePath, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(head) != expectedCommit {
		return errors.New("completion worktree HEAD mismatch")
	}
	status, err := gitx.Run(ctx, worktreePath, "status", "--porcelain", "--untracked-files=all")
	if err != nil || completionWorktreeDirty(status, stepID, checkpointID) {
		return errors.New("completion worktree is dirty")
	}
	return nil
}

func completionWorktreeDirty(status string, stepID, checkpointID int64) bool {
	_ = checkpointID
	mirrorPrefix := filepath.ToSlash(filepath.Join(
		".wanxiang",
		"checkpoints",
		strconv.FormatInt(stepID, 10),
	)) + "/"
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
		if strings.HasPrefix(path, mirrorPrefix) && strings.HasSuffix(strings.ToLower(path), ".yaml") {
			continue
		}
		return true
	}
	return false
}

func (r *DatabaseCompletionReporter) dependencies(ctx context.Context, taskID, stepID int64) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `select from_step_id from workflow_edges
		where task_id=? and to_step_id=? and from_step_id is not null order by from_step_id`, taskID, stepID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]int64, 0)
	for rows.Next() {
		var dependency int64
		if err := rows.Scan(&dependency); err != nil {
			return nil, err
		}
		result = append(result, dependency)
	}
	return result, rows.Err()
}
