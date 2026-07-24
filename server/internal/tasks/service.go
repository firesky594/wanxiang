package tasks

import (
	"context"
	"crypto/rand"
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
	"sync"
	"time"

	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/events"
	"wanxiang-agent/server/internal/files"
	"wanxiang-agent/server/internal/gitx"
)

type Service struct {
	cfg config.Config
	db  *sql.DB
	bus *events.Bus
}

var taskCreateMu sync.Mutex

// List 分页查询任务列表。
func (s *Service) List(ctx context.Context, limit, offset int) ([]Task, error) {
	limit, offset = normalizePagination(limit, offset)
	rows, err := s.db.QueryContext(ctx, `select t.id,t.project_id,p.slug,t.title,t.description,t.status
		from tasks t join projects p on p.id=t.project_id order by t.created_at desc,t.id desc limit ? offset ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Task, 0)
	for rows.Next() {
		var task Task
		if err := rows.Scan(&task.ID, &task.ProjectID, &task.ProjectSlug, &task.Title, &task.Description, &task.Status); err != nil {
			return nil, err
		}
		result = append(result, task)
	}
	return result, rows.Err()
}

// ListProjects 分页查询项目列表。
func (s *Service) ListProjects(ctx context.Context, limit, offset int) ([]Project, error) {
	limit, offset = normalizePagination(limit, offset)
	rows, err := s.db.QueryContext(ctx, `select id,slug,dir,status,main_commit,remote_url,created_at from projects order by created_at desc,id desc limit ? offset ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Project, 0)
	for rows.Next() {
		var item Project
		var main sql.NullString
		if err := rows.Scan(&item.ID, &item.Slug, &item.Dir, &item.Status, &main, &item.RemoteURL, &item.CreatedAt); err != nil {
			return nil, err
		}
		if main.Valid {
			item.MainCommit = &main.String
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// Get 查询任务、项目、步骤与依赖详情。
func (s *Service) Get(ctx context.Context, id int64) (TaskDetail, error) {
	var detail TaskDetail
	var mainCommit sql.NullString
	err := s.db.QueryRowContext(ctx, `select t.id,t.project_id,p.slug,t.title,t.description,t.status,
		p.id,p.slug,p.dir,p.status,p.main_commit,p.remote_url,p.created_at
		from tasks t join projects p on p.id=t.project_id where t.id=?`, id).Scan(
		&detail.Task.ID, &detail.Task.ProjectID, &detail.Task.ProjectSlug, &detail.Task.Title, &detail.Task.Description, &detail.Task.Status,
		&detail.Project.ID, &detail.Project.Slug, &detail.Project.Dir, &detail.Project.Status, &mainCommit, &detail.Project.RemoteURL, &detail.Project.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskDetail{}, ErrNotFound
	}
	if err != nil {
		return TaskDetail{}, err
	}
	if mainCommit.Valid {
		detail.Project.MainCommit = &mainCommit.String
	}
	detail.Steps, err = s.listSteps(ctx, id)
	if err != nil {
		return TaskDetail{}, err
	}
	detail.Edges, err = s.listEdges(ctx, id)
	return detail, err
}

// UpdateStatus 校验状态机后更新任务状态并发布事件。
func (s *Service) UpdateStatus(ctx context.Context, id int64, next, actor string) (Task, error) {
	detail, err := s.Get(ctx, id)
	if err != nil {
		return Task{}, err
	}
	if !validTransition(detail.Task.Status, next) {
		return Task{}, ErrInvalidTransition
	}
	previous := detail.Task.Status
	res, err := s.db.ExecContext(ctx, `update tasks set status=? where id=? and status=?`, next, id, previous)
	if err != nil {
		return Task{}, err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return Task{}, err
	}
	if changed != 1 {
		return Task{}, ErrInvalidTransition
	}
	detail.Task.Status = next
	if err := s.bus.PublishJSON(ctx, &id, "task.status_changed", actor, map[string]any{"task_id": id, "from": previous, "to": next}); err != nil {
		return Task{}, err
	}
	return detail.Task, nil
}

func (s *Service) listSteps(ctx context.Context, taskID int64) ([]TaskStep, error) {
	rows, err := s.db.QueryContext(ctx, `select id,task_id,agent_name,kind,status,input,output,created_at,completed_at from task_steps where task_id=? order by id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]TaskStep, 0)
	for rows.Next() {
		var item TaskStep
		var completed sql.NullString
		if err := rows.Scan(&item.ID, &item.TaskID, &item.AgentName, &item.Kind, &item.Status, &item.Input, &item.Output, &item.CreatedAt, &completed); err != nil {
			return nil, err
		}
		if completed.Valid {
			item.CompletedAt = &completed.String
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Service) listEdges(ctx context.Context, taskID int64) ([]WorkflowEdge, error) {
	rows, err := s.db.QueryContext(ctx, `select id,task_id,from_step_id,to_step_id,label,created_at from workflow_edges where task_id=? order by id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]WorkflowEdge, 0)
	for rows.Next() {
		var item WorkflowEdge
		var from, to sql.NullInt64
		if err := rows.Scan(&item.ID, &item.TaskID, &from, &to, &item.Label, &item.CreatedAt); err != nil {
			return nil, err
		}
		if from.Valid {
			item.FromStepID = &from.Int64
		}
		if to.Valid {
			item.ToStepID = &to.Int64
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func normalizePagination(limit, offset int) (int, int) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func validTransition(current, next string) bool {
	allowed := map[string]map[string]bool{
		"created":           {"planning": true, "blocked": true},
		"planning":          {"assigned": true, "blocked": true},
		"assigned":          {"workspace_ready": true, "in_progress": true, "blocked": true},
		"workspace_ready":   {"in_progress": true, "blocked": true},
		"in_progress":       {"review": true, "blocked": true, "interrupted": true},
		"interrupted":       {"in_progress": true, "blocked": true},
		"review":            {"merged": true, "changes_requested": true, "blocked": true},
		"changes_requested": {"in_progress": true, "blocked": true},
		"merged":            {"completed": true},
		"blocked":           {"planning": true, "assigned": true, "in_progress": true},
	}
	return allowed[current][next]
}

// NewService 创建任务领域服务。
func NewService(cfg config.Config, db *sql.DB, bus *events.Bus) *Service {
	if bus == nil {
		bus = events.NewBus(db)
	}
	return &Service{cfg: cfg, db: db, bus: bus}
}

// CreateTask 按标题与描述创建新项目任务。
func (s *Service) CreateTask(ctx context.Context, title, description string, actors ...string) (task Task, err error) {
	return s.CreateTaskWithInput(ctx, CreateTaskInput{Title: title, Description: description}, actors...)
}

// CreateTaskWithInput 在新建或既有项目中事务化创建任务。
func (s *Service) CreateTaskWithInput(ctx context.Context, input CreateTaskInput, actors ...string) (task Task, err error) {
	input.Title = strings.TrimSpace(input.Title)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.Title == "" {
		return Task{}, fmt.Errorf("%w: task title is required", ErrInvalidInput)
	}
	if input.IdempotencyKey != "" && !idempotencyKeyPattern.MatchString(input.IdempotencyKey) {
		return Task{}, fmt.Errorf("%w: invalid idempotency key", ErrInvalidInput)
	}
	if input.ProjectID != nil && *input.ProjectID <= 0 {
		return Task{}, fmt.Errorf("%w: project_id must be positive", ErrInvalidInput)
	}
	if input.IdempotencyKey != "" {
		taskCreateMu.Lock()
		defer taskCreateMu.Unlock()
		if existing, found, findErr := s.findIdempotentTask(ctx, input); findErr != nil {
			return Task{}, findErr
		} else if found {
			return existing, nil
		}
	}
	if input.ProjectID != nil {
		return s.createTaskInExistingProject(ctx, input, actors...)
	}
	title, description := input.Title, input.Description
	slug := fmt.Sprintf("task-%s-%s-%s", time.Now().UTC().Format("20060102150405"), slugify(title), taskSlugSuffix())
	projectDir := filepath.Join(s.cfg.ProjectDir, slug)
	if _, statErr := os.Stat(projectDir); statErr == nil {
		return Task{}, errors.New("task project already exists")
	} else if !os.IsNotExist(statErr) {
		return Task{}, statErr
	}
	keepProject := false
	defer func() {
		if !keepProject {
			_ = os.RemoveAll(projectDir)
		}
	}()
	for _, dir := range []string{
		filepath.Join(projectDir, ".wanxiang", "merge_requests"),
		filepath.Join(projectDir, ".wanxiang", "test_reports"),
		filepath.Join(projectDir, ".wanxiang", "manager_reviews"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Task{}, err
		}
	}
	if out, err := gitx.Run(ctx, projectDir, "init", "-b", "main"); err != nil {
		if fallbackOut, fallbackErr := gitx.Run(ctx, projectDir, "init"); fallbackErr != nil {
			return Task{}, fmt.Errorf("git init failed: %w: %s", err, out+fallbackOut)
		}
		if branchOut, branchErr := gitx.Run(ctx, projectDir, "branch", "-M", "main"); branchErr != nil {
			return Task{}, fmt.Errorf("git branch initialization failed: %w: %s", branchErr, strings.TrimSpace(branchOut))
		}
	}
	for key, value := range map[string]string{
		"user.name":  "Wanxiang Agent",
		"user.email": "wanxiang-agent@localhost",
	} {
		if out, err := gitx.Run(ctx, projectDir, "config", "--local", key, value); err != nil {
			return Task{}, fmt.Errorf("git identity configuration failed for %s: %w: %s", key, err, strings.TrimSpace(out))
		}
	}
	taskYaml := fmt.Sprintf("title: %q\ndescription: %q\n", title, description)
	if err := os.WriteFile(filepath.Join(projectDir, ".wanxiang", "task.yaml"), []byte(taskYaml), 0o644); err != nil {
		return Task{}, err
	}
	if out, err := gitx.Run(ctx, projectDir, "add", "."); err != nil {
		return Task{}, fmt.Errorf("git add failed: %w: %s", err, strings.TrimSpace(out))
	}
	if out, err := gitx.Run(ctx, projectDir, "commit", "-m", "工程：初始化任务项目", "--allow-empty"); err != nil {
		return Task{}, fmt.Errorf("git commit failed: %w: %s", err, strings.TrimSpace(out))
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `insert into projects(slug,dir,status,remote_url,created_at) values(?,?,?,?,?)`, slug, projectDir, "created", s.cfg.RemoteURL, createdAt)
	if err != nil {
		_ = tx.Rollback()
		return s.resolveIdempotentCreateError(ctx, input, err)
	}
	projectID, _ := res.LastInsertId()
	fingerprint := createTaskFingerprint(input)
	res, err = tx.ExecContext(ctx, `insert into tasks(project_id,title,description,status,priority,created_at,idempotency_key,request_fingerprint) values(?,?,?,?,0,?,?,?)`,
		projectID, title, description, "created", createdAt, input.IdempotencyKey, fingerprint)
	if err != nil {
		_ = tx.Rollback()
		return s.resolveIdempotentCreateError(ctx, input, err)
	}
	taskID, _ := res.LastInsertId()
	actor := taskActor(actors)
	event, err := insertTaskCreatedEvent(ctx, tx, taskID, projectID, slug, title, actor)
	if err != nil {
		return Task{}, err
	}
	if err := tx.Commit(); err != nil {
		// Commit 返回错误时事务结果可能不确定；保留刚创建的项目目录，避免数据库已提交却被清理。
		keepProject = true
		recovered, recoverErr := s.resolveIdempotentCommitError(input, err)
		return recovered, recoverErr
	}
	keepProject = true
	task = Task{ID: taskID, ProjectID: projectID, ProjectSlug: slug, Title: title, Description: description, Status: "created"}
	s.bus.Notify(event)
	return task, nil
}

func (s *Service) createTaskInExistingProject(ctx context.Context, input CreateTaskInput, actors ...string) (Task, error) {
	var slug, projectDir string
	err := s.db.QueryRowContext(ctx, `select slug,dir from projects where id=?`, *input.ProjectID).Scan(&slug, &projectDir)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrProjectNotFound
	}
	if err != nil {
		return Task{}, err
	}
	checked, err := files.UnderRoot(s.cfg.ProjectDir, projectDir)
	if err != nil {
		return Task{}, fmt.Errorf("%w: unsafe project path", ErrProjectConflict)
	}
	if info, statErr := os.Stat(checked); statErr != nil || !info.IsDir() {
		return Task{}, fmt.Errorf("%w: project directory unavailable", ErrProjectConflict)
	}
	branch, branchErr := gitx.Run(ctx, checked, "branch", "--show-current")
	if branchErr != nil || strings.TrimSpace(branch) != "main" {
		return Task{}, fmt.Errorf("%w: project must be on main", ErrProjectConflict)
	}
	status, statusErr := gitx.Run(ctx, checked, "status", "--porcelain")
	if statusErr != nil || strings.TrimSpace(status) != "" {
		return Task{}, fmt.Errorf("%w: project worktree must be clean", ErrProjectConflict)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `insert into tasks(project_id,title,description,status,priority,created_at,idempotency_key,request_fingerprint) values(?,?,?,?,0,?,?,?)`,
		*input.ProjectID, input.Title, input.Description, "created", createdAt, input.IdempotencyKey, createTaskFingerprint(input))
	if err != nil {
		_ = tx.Rollback()
		return s.resolveIdempotentCreateError(ctx, input, err)
	}
	taskID, _ := result.LastInsertId()
	actor := taskActor(actors)
	event, err := insertTaskCreatedEvent(ctx, tx, taskID, *input.ProjectID, slug, input.Title, actor)
	if err != nil {
		return Task{}, err
	}
	task := Task{ID: taskID, ProjectID: *input.ProjectID, ProjectSlug: slug, Title: input.Title, Description: input.Description, Status: "created"}
	if err := tx.Commit(); err != nil {
		return s.resolveIdempotentCommitError(input, err)
	}
	s.bus.Notify(event)
	return task, nil
}

var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func (s *Service) findIdempotentTask(ctx context.Context, input CreateTaskInput) (Task, bool, error) {
	var task Task
	var fingerprint string
	err := s.db.QueryRowContext(ctx, `select t.id,t.project_id,p.slug,t.title,t.description,t.status,t.request_fingerprint
		from tasks t join projects p on p.id=t.project_id where t.idempotency_key=?`, input.IdempotencyKey).
		Scan(&task.ID, &task.ProjectID, &task.ProjectSlug, &task.Title, &task.Description, &task.Status, &fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, nil
	}
	if err != nil {
		return Task{}, false, err
	}
	if fingerprint != createTaskFingerprint(input) {
		return Task{}, false, ErrIdempotencyConflict
	}
	return task, true, nil
}

func createTaskFingerprint(input CreateTaskInput) string {
	raw, _ := json.Marshal(struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		ProjectID   *int64 `json:"project_id"`
	}{Title: input.Title, Description: input.Description, ProjectID: input.ProjectID})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (s *Service) resolveIdempotentCreateError(ctx context.Context, input CreateTaskInput, original error) (Task, error) {
	if input.IdempotencyKey == "" {
		return Task{}, original
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if existing, found, err := s.findIdempotentTask(ctx, input); err != nil {
			if !isSQLiteBusy(err) {
				return Task{}, err
			}
		} else if found {
			return existing, nil
		}
		select {
		case <-ctx.Done():
			return Task{}, ctx.Err()
		case <-deadline.C:
			return Task{}, original
		case <-ticker.C:
		}
	}
}

func (s *Service) resolveIdempotentCommitError(input CreateTaskInput, original error) (Task, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.resolveIdempotentCreateError(ctx, input, original)
}

func isSQLiteBusy(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "sqlite_busy")
}

func taskSlugSuffix() string {
	raw := make([]byte, 4)
	if _, err := rand.Read(raw); err == nil {
		return hex.EncodeToString(raw)
	}
	return fmt.Sprintf("%08x", uint64(time.Now().UTC().UnixNano())&0xffffffff)
}

func insertTaskCreatedEvent(ctx context.Context, tx *sql.Tx, taskID, projectID int64, slug, title, actor string) (events.Event, error) {
	payload, err := json.Marshal(map[string]any{
		"task_id": taskID, "project_id": projectID, "project_slug": slug, "title": title, "status": "created",
	})
	if err != nil {
		return events.Event{}, err
	}
	return events.InsertTx(ctx, tx, events.Event{TaskID: &taskID, Type: "task.created", Actor: actor, Payload: payload})
}

func taskActor(actors []string) string {
	if len(actors) > 0 && strings.TrimSpace(actors[0]) != "" {
		return actors[0]
	}
	return "admin"
}

func slugify(input string) string {
	lower := strings.ToLower(input)
	re := regexp.MustCompile(`[^a-z0-9]+`)
	value := strings.Trim(re.ReplaceAllString(lower, "-"), "-")
	if value == "" {
		return "task"
	}
	if len(value) > 40 {
		return value[:40]
	}
	return value
}
