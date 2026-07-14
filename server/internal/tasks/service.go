package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/events"
	"wanxiang-agent/server/internal/gitx"
)

type Service struct {
	cfg config.Config
	db  *sql.DB
	bus *events.Bus
}

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
		"assigned":          {"in_progress": true, "blocked": true},
		"in_progress":       {"review": true, "blocked": true, "interrupted": true},
		"interrupted":       {"in_progress": true, "blocked": true},
		"review":            {"merged": true, "changes_requested": true, "blocked": true},
		"changes_requested": {"in_progress": true, "blocked": true},
		"merged":            {"completed": true},
		"blocked":           {"planning": true, "assigned": true, "in_progress": true},
	}
	return allowed[current][next]
}

func NewService(cfg config.Config, db *sql.DB, bus *events.Bus) *Service {
	if bus == nil {
		bus = events.NewBus(db)
	}
	return &Service{cfg: cfg, db: db, bus: bus}
}

func (s *Service) CreateTask(ctx context.Context, title, description string, actors ...string) (task Task, err error) {
	slug := fmt.Sprintf("task-%s-%s", time.Now().UTC().Format("20060102150405"), slugify(title))
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
	if out, err := gitx.Run(ctx, projectDir, "commit", "-m", "chore: initialize task project", "--allow-empty"); err != nil {
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
		return Task{}, err
	}
	projectID, _ := res.LastInsertId()
	res, err = tx.ExecContext(ctx, `insert into tasks(project_id,title,description,status,priority,created_at) values(?,?,?,?,0,?)`, projectID, title, description, "created", createdAt)
	if err != nil {
		return Task{}, err
	}
	taskID, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return Task{}, err
	}
	keepProject = true
	task = Task{ID: taskID, ProjectID: projectID, ProjectSlug: slug, Title: title, Description: description, Status: "created"}
	actor := "admin"
	if len(actors) > 0 && actors[0] != "" {
		actor = actors[0]
	}
	if err := s.bus.PublishJSON(ctx, &taskID, "task.created", actor, map[string]any{
		"task_id": taskID, "project_id": projectID, "project_slug": slug, "title": title, "status": task.Status,
	}); err != nil {
		return Task{}, err
	}
	return task, nil
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
