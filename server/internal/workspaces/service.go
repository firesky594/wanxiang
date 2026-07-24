package workspaces

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/events"
	"wanxiang-agent/server/internal/files"
	"wanxiang-agent/server/internal/gitx"
	"wanxiang-agent/server/internal/planning"
)

type Service struct {
	cfg    config.Config
	db     *sql.DB
	bus    *events.Bus
	lockMu sync.Mutex
	locks  map[int64]*sync.Mutex
}
type WorkspaceItem struct {
	ID              int64    `json:"id"`
	StepID          int64    `json:"step_id"`
	AssignmentID    int64    `json:"assignment_id"`
	AgentName       string   `json:"agent_name"`
	ReportsTo       string   `json:"reports_to,omitempty"`
	BranchName      string   `json:"branch_name"`
	WorktreePath    string   `json:"worktree_path"`
	BaseCommit      string   `json:"base_commit"`
	ProvisionCommit string   `json:"provision_commit"`
	WriteScope      []string `json:"write_scope"`
	MetadataHash    string   `json:"metadata_hash"`
	Status          string   `json:"status"`
	LastError       string   `json:"last_error,omitempty"`
}
type TaskWorkspace struct {
	TaskID      int64           `json:"task_id"`
	ProjectID   int64           `json:"project_id"`
	ProjectSlug string          `json:"project_slug"`
	Status      string          `json:"status"`
	Items       []WorkspaceItem `json:"items"`
}
type assignmentSource struct {
	AssignmentID, StepID int64
	AgentName, ReportsTo string
	Item                 planning.WorkItem
}

// NewService 创建任务工作区服务。
func NewService(cfg config.Config, db *sql.DB, bus *events.Bus) *Service {
	if bus == nil {
		bus = events.NewBus(db)
	}
	return &Service{cfg: cfg, db: db, bus: bus, locks: map[int64]*sync.Mutex{}}
}

// ProvisionTask 创建分支、Worktree、所有权元数据及依赖执行闸门。
func (s *Service) ProvisionTask(ctx context.Context, taskID int64) (workspace TaskWorkspace, err error) {
	var projectID int64
	var slug, projectDir, taskStatus string
	err = s.db.QueryRowContext(ctx, `select p.id,p.slug,p.dir,t.status from tasks t join projects p on p.id=t.project_id where t.id=?`, taskID).Scan(&projectID, &slug, &projectDir, &taskStatus)
	if err != nil {
		return TaskWorkspace{}, err
	}
	lockedProjectID := projectID
	lock := s.projectLock(projectID)
	lock.Lock()
	defer lock.Unlock()
	releaseProjectLock, lockErr := gitx.AcquireProjectLock(ctx, s.cfg.DataDir, lockedProjectID)
	if lockErr != nil {
		return TaskWorkspace{}, fmt.Errorf("acquire project git lock: %w", lockErr)
	}
	defer releaseProjectLock()
	if err = s.db.QueryRowContext(ctx, `select p.id,p.slug,p.dir,t.status from tasks t join projects p on p.id=t.project_id where t.id=?`, taskID).
		Scan(&projectID, &slug, &projectDir, &taskStatus); err != nil {
		return TaskWorkspace{}, err
	}
	if projectID != lockedProjectID {
		return TaskWorkspace{}, errors.New("task project changed while waiting for project lock")
	}
	projectDir, err = files.UnderRoot(s.cfg.ProjectDir, projectDir)
	if err != nil {
		return TaskWorkspace{}, fmt.Errorf("unsafe project path: %w", err)
	}
	if taskStatus != "assigned" && taskStatus != "workspace_ready" {
		return TaskWorkspace{}, fmt.Errorf("task %d is not assigned", taskID)
	}
	sources, err := s.loadSources(ctx, taskID)
	if err != nil {
		return TaskWorkspace{}, err
	}
	if len(sources) == 0 {
		return TaskWorkspace{}, errors.New("task has no assignments")
	}
	if existing, found, loadErr := s.existingReady(ctx, taskID); loadErr != nil {
		return TaskWorkspace{}, loadErr
	} else if found {
		if taskStatus == "assigned" {
			provision, verifyErr := verifyProvisionedRecovery(ctx, projectDir, taskID, sources, existing)
			if verifyErr != nil {
				return TaskWorkspace{}, verifyErr
			}
			if err = s.finalizeProvisionDatabase(ctx, taskID, projectID, taskStatus, provision); err != nil {
				return TaskWorkspace{}, err
			}
			_ = s.bus.PublishJSON(ctx, &taskID, "task.workspace.ready", "system", map[string]any{"task_id": taskID, "project_id": projectID, "provision_commit": provision, "recovered": true})
			return s.GetTask(ctx, taskID)
		}
		return existing, nil
	}
	branch, err := runTrim(ctx, projectDir, "branch", "--show-current")
	if err != nil || branch != "main" {
		return TaskWorkspace{}, errors.New("project must be on clean main")
	}
	status, err := runTrim(ctx, projectDir, "status", "--porcelain")
	if err != nil || status != "" {
		return TaskWorkspace{}, errors.New("project must be on clean main")
	}
	base, err := runTrim(ctx, projectDir, "rev-parse", "HEAD")
	if err != nil {
		return TaskWorkspace{}, err
	}
	created := timestamp()
	records := make([]WorkspaceItem, 0, len(sources))
	provision := ""
	recovering := false
	recovery, _ := s.GetTask(ctx, taskID)
	if len(recovery.Items) == len(sources) && len(recovery.Items) > 0 && recovery.Items[0].ProvisionCommit != "" {
		provision = recovery.Items[0].ProvisionCommit
		consistent := base == provision
		for _, item := range recovery.Items {
			consistent = consistent && item.ProvisionCommit == provision && item.BaseCommit == recovery.Items[0].BaseCommit
		}
		if consistent {
			records = recovery.Items
			recovering = true
			base = recovery.Items[0].BaseCommit
			_, err = s.db.ExecContext(ctx, `update project_workspaces set status='provisioning',last_error='',updated_at=? where task_id=? and step_id in (select id from task_steps where task_id=? and plan_version=(select coalesce(max(version),1) from task_plan_versions where task_id=?))`, created, taskID, taskID, taskID)
			if err != nil {
				return TaskWorkspace{}, err
			}
		}
	}
	for _, source := range sources {
		if recovering {
			break
		}
		branchName := fmt.Sprintf("agent/%s/%d-%d-%s", source.AgentName, taskID, source.StepID, source.Item.Key)
		worktreePath := filepath.Join(s.cfg.DataDir, "worktrees", fmt.Sprintf("task-%d", taskID), fmt.Sprintf("step-%d-%s", source.StepID, source.AgentName))
		metadata := AssignmentMetadata{MetadataVersion: 1, TaskID: taskID, StepID: source.StepID, AssignmentID: source.AssignmentID, WorkItemKey: source.Item.Key, AgentName: source.AgentName, ReportsTo: source.ReportsTo, BranchName: branchName, WorktreeID: fmt.Sprintf("task-%d-step-%d", taskID, source.StepID), BaseCommit: base, WriteScope: []string{"."}, Status: "ready"}
		_, hash, encodeErr := EncodeAssignment(metadata)
		if encodeErr != nil {
			return TaskWorkspace{}, encodeErr
		}
		result, insertErr := s.db.ExecContext(ctx, `insert into project_workspaces(project_id,task_id,step_id,assignment_id,agent_name,reports_to,branch_name,worktree_path,base_commit,provision_commit,write_scope_json,metadata_hash,status,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,'','["."]',?,'provisioning',?,?) on conflict(step_id) do update set assignment_id=excluded.assignment_id,agent_name=excluded.agent_name,reports_to=excluded.reports_to,branch_name=excluded.branch_name,worktree_path=excluded.worktree_path,base_commit=excluded.base_commit,write_scope_json=excluded.write_scope_json,metadata_hash=excluded.metadata_hash,status='provisioning',last_error='',updated_at=excluded.updated_at`, projectID, taskID, source.StepID, source.AssignmentID, source.AgentName, nullable(source.ReportsTo), branchName, worktreePath, base, hash, created, created)
		if insertErr != nil {
			return TaskWorkspace{}, insertErr
		}
		id, _ := result.LastInsertId()
		records = append(records, WorkspaceItem{ID: id, StepID: source.StepID, AssignmentID: source.AssignmentID, AgentName: source.AgentName, ReportsTo: source.ReportsTo, BranchName: branchName, WorktreePath: worktreePath, BaseCommit: base, WriteScope: []string{"."}, MetadataHash: hash, Status: "provisioning"})
	}
	defer func() {
		if err != nil {
			s.failTask(context.Background(), taskID, err)
		}
	}()
	if provision == "" {
		if err = s.writeMetadata(ctx, projectDir, slug, taskID, sources, base); err != nil {
			return TaskWorkspace{}, err
		}
		if _, err = gitx.Run(ctx, projectDir, "add", ".wanxiang/project.yaml", fmt.Sprintf(".wanxiang/assignments/%d-*.yaml", taskID)); err != nil {
			return TaskWorkspace{}, fmt.Errorf("stage workspace metadata: %w", err)
		}
		if out, commitErr := gitx.Run(ctx, projectDir, "commit", "-m", fmt.Sprintf("元数据：登记任务 %d 工作区", taskID)); commitErr != nil {
			return TaskWorkspace{}, fmt.Errorf("commit workspace metadata: %w: %s", commitErr, strings.TrimSpace(out))
		}
		provision, err = runTrim(ctx, projectDir, "rev-parse", "HEAD")
		if err != nil {
			return TaskWorkspace{}, err
		}
		if _, err = s.db.ExecContext(ctx, `update project_workspaces set provision_commit=?,updated_at=? where task_id=? and step_id in (select id from task_steps where task_id=? and plan_version=(select coalesce(max(version),1) from task_plan_versions where task_id=?))`, provision, timestamp(), taskID, taskID, taskID); err != nil {
			return TaskWorkspace{}, err
		}
	}
	for index := range records {
		records[index].ProvisionCommit = provision
		if err = createWorktree(ctx, projectDir, records[index].WorktreePath, records[index].BranchName, provision); err != nil {
			return TaskWorkspace{}, err
		}
	}
	now := timestamp()
	if _, err = s.db.ExecContext(ctx, `update project_workspaces set status='ready',last_error='',updated_at=? where task_id=? and step_id in (select id from task_steps where task_id=? and plan_version=(select coalesce(max(version),1) from task_plan_versions where task_id=?))`, now, taskID, taskID, taskID); err != nil {
		return TaskWorkspace{}, err
	}
	if err = s.markDependentWorkspacesWaiting(ctx, taskID); err != nil {
		return TaskWorkspace{}, err
	}
	if err = s.finalizeProvisionDatabase(ctx, taskID, projectID, taskStatus, provision); err != nil {
		return TaskWorkspace{}, err
	}
	_ = s.bus.PublishJSON(ctx, &taskID, "task.workspace.ready", "system", map[string]any{"task_id": taskID, "project_id": projectID, "provision_commit": provision})
	return s.GetTask(ctx, taskID)
}

// GetTask 查询任务工作区及各步骤状态。
func (s *Service) GetTask(ctx context.Context, taskID int64) (TaskWorkspace, error) {
	result := TaskWorkspace{TaskID: taskID, Items: []WorkspaceItem{}}
	if err := s.db.QueryRowContext(ctx, `select p.id,p.slug from tasks t join projects p on p.id=t.project_id where t.id=?`, taskID).Scan(&result.ProjectID, &result.ProjectSlug); err != nil {
		return TaskWorkspace{}, err
	}
	rows, err := s.db.QueryContext(ctx, `select pw.id,pw.step_id,pw.assignment_id,pw.agent_name,coalesce(pw.reports_to,''),pw.branch_name,pw.worktree_path,pw.base_commit,pw.provision_commit,pw.write_scope_json,pw.metadata_hash,pw.status,pw.last_error from project_workspaces pw join task_steps ts on ts.id=pw.step_id where pw.task_id=? and ts.plan_version=(select coalesce(max(version),1) from task_plan_versions where task_id=?) order by pw.step_id`, taskID, taskID)
	if err != nil {
		return TaskWorkspace{}, err
	}
	defer rows.Close()
	allProvisioned := true
	aggregateStatus := ""
	for rows.Next() {
		var item WorkspaceItem
		var scope string
		if err := rows.Scan(&item.ID, &item.StepID, &item.AssignmentID, &item.AgentName, &item.ReportsTo, &item.BranchName, &item.WorktreePath, &item.BaseCommit, &item.ProvisionCommit, &scope, &item.MetadataHash, &item.Status, &item.LastError); err != nil {
			return TaskWorkspace{}, err
		}
		_ = json.Unmarshal([]byte(scope), &item.WriteScope)
		if item.Status != "ready" && item.Status != "waiting_dependencies" && item.Status != "dependency_syncing" {
			allProvisioned = false
			if item.Status == "drifted" {
				aggregateStatus = "drifted"
			} else if aggregateStatus == "" {
				aggregateStatus = item.Status
			}
		}
		result.Items = append(result.Items, item)
	}
	if err := rows.Err(); err != nil {
		return TaskWorkspace{}, err
	}
	if len(result.Items) == 0 {
		result.Status = "pending"
	} else if allProvisioned {
		result.Status = "ready"
	} else if aggregateStatus != "" {
		result.Status = aggregateStatus
	} else {
		result.Status = result.Items[0].Status
	}
	return result, nil
}

func (s *Service) existingReady(ctx context.Context, taskID int64) (TaskWorkspace, bool, error) {
	result, err := s.GetTask(ctx, taskID)
	if err != nil {
		return TaskWorkspace{}, false, err
	}
	return result, result.Status == "ready", nil
}
func (s *Service) loadSources(ctx context.Context, taskID int64) ([]assignmentSource, error) {
	rows, err := s.db.QueryContext(ctx, `select ta.id,ta.step_id,ta.agent_name,coalesce(ta.reports_to,''),ts.input from task_assignments ta join task_steps ts on ts.id=ta.step_id where ta.task_id=? and ts.plan_version=(select coalesce(max(version),1) from task_plan_versions where task_id=?) order by ta.step_id`, taskID, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []assignmentSource{}
	for rows.Next() {
		var source assignmentSource
		var input string
		if err := rows.Scan(&source.AssignmentID, &source.StepID, &source.AgentName, &source.ReportsTo, &input); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(input), &source.Item); err != nil {
			return nil, err
		}
		result = append(result, source)
	}
	return result, rows.Err()
}
func (s *Service) writeMetadata(ctx context.Context, projectDir, slug string, taskID int64, sources []assignmentSource, base string) error {
	lead := ""
	_ = s.db.QueryRowContext(ctx, `select coalesce(project_lead,'') from team_decisions where task_id=? order by plan_version desc limit 1`, taskID).Scan(&lead)
	agents := make([]ProjectAgent, 0, len(sources))
	for _, source := range sources {
		agents = append(agents, ProjectAgent{Name: source.AgentName, ReportsTo: source.ReportsTo})
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })
	project, err := EncodeProject(ProjectMetadata{MetadataVersion: 1, Project: slug, Manager: "manager", ProjectLead: lead, Agents: agents, BranchPolicy: "agent/<agent>/<task>-<work-item>", MergeTarget: "main"})
	if err != nil {
		return err
	}
	dir := filepath.Join(projectDir, ".wanxiang", "assignments")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".wanxiang", "project.yaml"), project, 0o644); err != nil {
		return err
	}
	for _, source := range sources {
		branch := fmt.Sprintf("agent/%s/%d-%d-%s", source.AgentName, taskID, source.StepID, source.Item.Key)
		encoded, _, err := EncodeAssignment(AssignmentMetadata{MetadataVersion: 1, TaskID: taskID, StepID: source.StepID, AssignmentID: source.AssignmentID, WorkItemKey: source.Item.Key, AgentName: source.AgentName, ReportsTo: source.ReportsTo, BranchName: branch, WorktreeID: fmt.Sprintf("task-%d-step-%d", taskID, source.StepID), BaseCommit: base, WriteScope: []string{"."}, Status: "ready"})
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d-%d.yaml", taskID, source.StepID)), encoded, 0o644); err != nil {
			return err
		}
	}
	return nil
}
func createWorktree(ctx context.Context, projectDir, path, branch, provision string) error {
	if exactWorktree(ctx, projectDir, path, branch, provision) {
		return nil
	}
	if entries, err := os.ReadDir(path); err == nil && len(entries) > 0 {
		return fmt.Errorf("worktree path already contains unknown files: %s", path)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if head, err := runTrim(ctx, projectDir, "rev-parse", "refs/heads/"+branch); err == nil {
		if head != provision {
			return fmt.Errorf("branch already exists at unexpected commit: %s", branch)
		}
		if out, addErr := gitx.Run(ctx, projectDir, "worktree", "add", path, branch); addErr != nil {
			if exactWorktree(ctx, projectDir, path, branch, provision) {
				return nil
			}
			return fmt.Errorf("add existing branch worktree: %w: %s", addErr, strings.TrimSpace(out))
		}
		if !exactWorktree(ctx, projectDir, path, branch, provision) {
			return fmt.Errorf("existing branch worktree verification failed: %s", branch)
		}
		return nil
	}
	if out, err := gitx.Run(ctx, projectDir, "worktree", "add", "-b", branch, path, provision); err != nil {
		if exactWorktree(ctx, projectDir, path, branch, provision) {
			return nil
		}
		return fmt.Errorf("create worktree: %w: %s", err, strings.TrimSpace(out))
	}
	if !exactWorktree(ctx, projectDir, path, branch, provision) {
		return fmt.Errorf("created worktree verification failed: %s", branch)
	}
	return nil
}

func verifyProvisionedRecovery(ctx context.Context, projectDir string, taskID int64, sources []assignmentSource, workspace TaskWorkspace) (string, error) {
	if len(workspace.Items) == 0 || len(workspace.Items) != len(sources) {
		return "", errors.New("provision recovery workspace count mismatch")
	}
	if branch, err := runTrim(ctx, projectDir, "branch", "--show-current"); err != nil || branch != "main" {
		return "", errors.New("provision recovery project is not on main")
	}
	if status, err := runTrim(ctx, projectDir, "status", "--porcelain", "--untracked-files=all"); err != nil || status != "" {
		return "", errors.New("provision recovery project is not clean")
	}
	provision, err := runTrim(ctx, projectDir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	sourceByStep := make(map[int64]assignmentSource, len(sources))
	for _, source := range sources {
		sourceByStep[source.StepID] = source
	}
	for _, item := range workspace.Items {
		source, ok := sourceByStep[item.StepID]
		if !ok || item.ProvisionCommit == "" || item.ProvisionCommit != provision {
			return "", errors.New("provision recovery commit mismatch")
		}
		if item.Status != "ready" && item.Status != "waiting_dependencies" && item.Status != "dependency_syncing" {
			return "", errors.New("provision recovery workspace is incomplete")
		}
		snapshotPath := filepath.Join(projectDir, ".wanxiang", "assignments", fmt.Sprintf("%d-%d.yaml", taskID, item.StepID))
		content, readErr := os.ReadFile(snapshotPath)
		if readErr != nil {
			return "", readErr
		}
		metadata, decodeErr := DecodeAssignment(content)
		if decodeErr != nil {
			return "", decodeErr
		}
		canonical, hash, encodeErr := EncodeAssignment(metadata)
		if encodeErr != nil || !bytes.Equal(canonical, content) || hash != item.MetadataHash ||
			metadata.TaskID != taskID || metadata.StepID != item.StepID || metadata.AssignmentID != item.AssignmentID ||
			metadata.AgentName != source.AgentName || metadata.ReportsTo != source.ReportsTo ||
			metadata.BranchName != item.BranchName || metadata.BaseCommit != item.BaseCommit {
			return "", errors.New("provision recovery snapshot mismatch")
		}
		if !exactWorktree(ctx, projectDir, item.WorktreePath, item.BranchName, provision) {
			return "", errors.New("provision recovery worktree mismatch")
		}
	}
	return provision, nil
}

func (s *Service) finalizeProvisionDatabase(ctx context.Context, taskID, projectID int64, expectedStatus, provision string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentStatus string
	if err = tx.QueryRowContext(ctx, `select status from tasks where id=?`, taskID).Scan(&currentStatus); err != nil {
		return err
	}
	if currentStatus != expectedStatus {
		return errors.New("task changed concurrently during workspace provisioning")
	}
	if _, err = tx.ExecContext(ctx, `update projects set main_commit=? where id=?`, provision, projectID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `update tasks set status='workspace_ready' where id=? and status=?`, taskID, expectedStatus)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("task changed concurrently during workspace provisioning")
	}
	return tx.Commit()
}

func exactWorktree(ctx context.Context, projectDir, path, branch, head string) bool {
	raw, err := runTrim(ctx, projectDir, "worktree", "list", "--porcelain")
	if err != nil {
		return false
	}
	expectedPath := filepath.Clean(path)
	expectedBranch := "refs/heads/" + branch
	found := false
	var listedPath, listedHead, listedBranch string
	check := func() {
		if filepath.Clean(listedPath) == expectedPath && listedHead == head && listedBranch == expectedBranch {
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
	currentBranch, branchErr := runTrim(ctx, path, "branch", "--show-current")
	currentHead, headErr := runTrim(ctx, path, "rev-parse", "HEAD")
	status, statusErr := runTrim(ctx, path, "status", "--porcelain", "--untracked-files=all")
	return branchErr == nil && headErr == nil && statusErr == nil &&
		currentBranch == branch && currentHead == head && status == ""
}
func (s *Service) failTask(ctx context.Context, taskID int64, cause error) {
	message := cause.Error()
	if len(message) > 500 {
		message = message[:500]
	}
	_, _ = s.db.ExecContext(ctx, `update project_workspaces set status='failed',last_error=?,updated_at=?
		where task_id=?
			and status in ('provisioning','ready','waiting_dependencies','dependency_syncing')
			and exists(select 1 from tasks where id=? and status in ('assigned','workspace_ready'))
			and step_id in (
				select id from task_steps where task_id=?
					and plan_version=(select coalesce(max(version),1) from task_plan_versions where task_id=?)
			)`,
		message, timestamp(), taskID, taskID, taskID, taskID)
}
func (s *Service) projectLock(projectID int64) *sync.Mutex {
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	if s.locks[projectID] == nil {
		s.locks[projectID] = &sync.Mutex{}
	}
	return s.locks[projectID]
}
func runTrim(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := gitx.Run(ctx, dir, args...)
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(out))
	}
	return strings.TrimSpace(out), nil
}
func timestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
