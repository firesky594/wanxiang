package mr

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/events"
	"wanxiang-agent/server/internal/files"
	"wanxiang-agent/server/internal/gitx"
)

type BlockChecker interface {
	HasBlockingForMR(ctx context.Context, mrID int64) (bool, error)
}

type ManagerReadiness interface {
	ManagerReady(ctx context.Context) (bool, error)
}

type Service struct {
	cfg     config.Config
	db      *sql.DB
	bus     *events.Bus
	blocker BlockChecker
	manager ManagerReadiness
}

type mergeRecord struct {
	ProjectID    int64
	TaskID       int64
	SourceBranch string
	TargetBranch string
	Status       string
	ProjectDir   string
}

// NewService 创建合并请求领域服务。
func NewService(cfg config.Config, db *sql.DB, bus *events.Bus, manager ManagerReadiness, blockers ...BlockChecker) *Service {
	if bus == nil {
		bus = events.NewBus(db)
	}
	blocker := BlockChecker(databaseBlockChecker{db: db})
	if len(blockers) > 0 && blockers[0] != nil {
		blocker = blockers[0]
	}
	return &Service{cfg: cfg, db: db, bus: bus, blocker: blocker, manager: manager}
}

// Create 创建基础合并请求并发布事件。
func (s *Service) Create(ctx context.Context, projectID, taskID int64, title, sourceBranch, createdBy string) (MergeRequest, error) {
	res, err := s.db.ExecContext(ctx, `insert into merge_requests(project_id,task_id,title,source_branch,target_branch,status,created_by,created_at) values(?,?,?,?,?,?,?,?)`,
		projectID, taskID, title, sourceBranch, "main", "open", createdBy, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return MergeRequest{}, err
	}
	id, _ := res.LastInsertId()
	created := MergeRequest{ID: id, ProjectID: projectID, TaskID: taskID, Title: title, SourceBranch: sourceBranch, TargetBranch: "main", Status: "open"}
	if err := s.bus.PublishJSON(ctx, &taskID, "mr.created", createdBy, map[string]any{
		"mr_id": id, "project_id": projectID, "task_id": taskID, "title": title, "source_branch": sourceBranch, "target_branch": "main", "status": "open",
	}); err != nil {
		return MergeRequest{}, err
	}
	return created, nil
}

// List 分页查询合并请求列表。
func (s *Service) List(ctx context.Context, taskID *int64, limit, offset int) ([]MergeRequest, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	query := `select id,project_id,task_id,title,source_branch,target_branch,status from merge_requests`
	args := []any{}
	if taskID != nil {
		query += ` where task_id=?`
		args = append(args, *taskID)
	}
	query += ` order by id desc limit ? offset ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]MergeRequest, 0)
	for rows.Next() {
		var item MergeRequest
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.TaskID, &item.Title, &item.SourceBranch, &item.TargetBranch, &item.Status); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// ManagerMerge 由 Manager 校验阻塞后合并主分支。
func (s *Service) ManagerMerge(ctx context.Context, mrID int64, actor string) error {
	if actor != "manager" {
		return errors.New("only manager can merge main")
	}
	record, err := s.loadMergeRecord(ctx, mrID)
	if err != nil {
		return err
	}
	if record.TargetBranch != "main" {
		return errors.New("merge target must be main")
	}
	if record.Status != "open" {
		return errors.New("merge request is not open")
	}
	projectDir, err := files.UnderRoot(s.cfg.ProjectDir, record.ProjectDir)
	if err != nil {
		return fmt.Errorf("invalid project directory: %w", err)
	}
	lockedProjectID := record.ProjectID
	releaseProjectLock, err := gitx.AcquireProjectLock(ctx, s.cfg.DataDir, lockedProjectID)
	if err != nil {
		return fmt.Errorf("acquire project git lock: %w", err)
	}
	defer releaseProjectLock()
	record, err = s.loadMergeRecord(ctx, mrID)
	if err != nil {
		return err
	}
	if record.ProjectID != lockedProjectID || record.TargetBranch != "main" || record.Status != "open" {
		return errors.New("merge request changed while waiting for project lock")
	}
	projectDir, err = files.UnderRoot(s.cfg.ProjectDir, record.ProjectDir)
	if err != nil {
		return fmt.Errorf("invalid project directory: %w", err)
	}
	ready, err := s.managerReady(ctx)
	if err != nil {
		return err
	}
	if !ready {
		return errors.New("manager is not ready")
	}
	blocked, err := s.blocker.HasBlockingForMR(ctx, mrID)
	if err != nil {
		return err
	}
	if blocked {
		return errors.New("merge blocked by human issue")
	}
	if err := validateSourceBranch(ctx, projectDir, record.SourceBranch); err != nil {
		return err
	}
	if out, err := gitx.Run(ctx, projectDir, "status", "--porcelain"); err != nil {
		return fmt.Errorf("git status failed: %w: %s", err, strings.TrimSpace(out))
	} else if strings.TrimSpace(out) != "" {
		return errors.New("project repository has uncommitted changes")
	}
	if out, err := gitx.Run(ctx, projectDir, "checkout", "main"); err != nil {
		return fmt.Errorf("git checkout main failed: %w: %s", err, strings.TrimSpace(out))
	}
	if out, err := gitx.Run(ctx, projectDir, "merge", "--no-ff", "--no-edit", record.SourceBranch); err != nil {
		mergeErr := fmt.Errorf("git merge failed: %w: %s", err, strings.TrimSpace(out))
		if abortErr := abortMerge(ctx, projectDir); abortErr != nil {
			return fmt.Errorf("%v; merge abort failed: %w", mergeErr, abortErr)
		}
		return mergeErr
	}

	res, err := s.db.ExecContext(ctx, `update merge_requests set status='merged', merged_at=? where id=? and status='open'`, time.Now().UTC().Format(time.RFC3339Nano), mrID)
	if err != nil {
		return err
	}
	updated, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return errors.New("merge request status changed during merge")
	}
	return s.bus.PublishJSON(ctx, &record.TaskID, "mr.merged", actor, map[string]any{
		"mr_id": mrID, "project_id": record.ProjectID, "task_id": record.TaskID, "source_branch": record.SourceBranch, "target_branch": record.TargetBranch, "status": "merged",
	})
}

func (s *Service) loadMergeRecord(ctx context.Context, mrID int64) (mergeRecord, error) {
	var record mergeRecord
	err := s.db.QueryRowContext(ctx, `select mr.project_id,mr.task_id,mr.source_branch,mr.target_branch,mr.status,p.dir
		from merge_requests mr join projects p on p.id=mr.project_id where mr.id=?`, mrID).Scan(
		&record.ProjectID, &record.TaskID, &record.SourceBranch, &record.TargetBranch, &record.Status, &record.ProjectDir)
	if errors.Is(err, sql.ErrNoRows) {
		return mergeRecord{}, errors.New("merge request not found")
	}
	return record, err
}

func (s *Service) managerReady(ctx context.Context) (bool, error) {
	if s.manager == nil {
		return false, errors.New("manager readiness service is unavailable")
	}
	return s.manager.ManagerReady(ctx)
}

func validateSourceBranch(ctx context.Context, projectDir, sourceBranch string) error {
	if sourceBranch == "" || strings.HasPrefix(sourceBranch, "-") || strings.Contains(sourceBranch, "@{") {
		return errors.New("invalid source branch")
	}
	if out, err := gitx.Run(ctx, projectDir, "check-ref-format", "--branch", sourceBranch); err != nil {
		return fmt.Errorf("invalid source branch: %w: %s", err, strings.TrimSpace(out))
	}
	ref := "refs/heads/" + sourceBranch
	if out, err := gitx.Run(ctx, projectDir, "show-ref", "--verify", ref); err != nil {
		return fmt.Errorf("source branch not found: %w: %s", err, strings.TrimSpace(out))
	}
	return nil
}

func abortMerge(ctx context.Context, projectDir string) error {
	out, err := gitx.Run(ctx, projectDir, "rev-parse", "--git-path", "MERGE_HEAD")
	if err != nil {
		return fmt.Errorf("locate MERGE_HEAD: %w: %s", err, strings.TrimSpace(out))
	}
	mergeHead := strings.TrimSpace(out)
	if !filepath.IsAbs(mergeHead) {
		mergeHead = filepath.Join(projectDir, mergeHead)
	}
	if _, err := os.Stat(mergeHead); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	out, err = gitx.Run(ctx, projectDir, "merge", "--abort")
	if err != nil {
		return fmt.Errorf("git merge --abort: %w: %s", err, strings.TrimSpace(out))
	}
	return nil
}

type databaseBlockChecker struct {
	db *sql.DB
}

// HasBlockingForMR 查询数据库中的未解决阻塞问题。
func (d databaseBlockChecker) HasBlockingForMR(ctx context.Context, mrID int64) (bool, error) {
	var count int
	err := d.db.QueryRowContext(ctx, `select count(*) from issues i
		where i.blocking=1 and i.status not in ('resolved','closed')
		and (i.mr_id=? or (i.mr_id is null and i.task_id=(select task_id from merge_requests where id=?)))`, mrID, mrID).Scan(&count)
	return count > 0, err
}
