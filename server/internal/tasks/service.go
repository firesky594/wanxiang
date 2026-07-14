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
