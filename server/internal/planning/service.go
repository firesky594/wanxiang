package planning

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/providers"
	"wanxiang-agent/server/internal/tasks"
)

type AgentChatter interface {
	ChatAgent(context.Context, string, []providers.Message, int) (providers.Result, error)
}

type Service struct {
	cfg     config.Config
	db      *sql.DB
	chatter AgentChatter
}

func NewService(cfg config.Config, db *sql.DB, chatter AgentChatter) *Service {
	return &Service{cfg: cfg, db: db, chatter: chatter}
}

func (s *Service) PlanTask(ctx context.Context, taskID int64) (Plan, error) {
	task, err := s.loadTask(ctx, taskID)
	if err != nil {
		return Plan{}, err
	}
	if task.Status == "planned" {
		return s.loadPersisted(ctx, taskID)
	}
	if task.Status != "created" {
		return Plan{}, fmt.Errorf("task status %q cannot be planned", task.Status)
	}
	res, err := s.db.ExecContext(ctx, `update tasks set status='planning' where id=? and status='created'`, taskID)
	if err != nil {
		return Plan{}, err
	}
	changed, _ := res.RowsAffected()
	if changed != 1 {
		return Plan{}, errors.New("task planning was claimed concurrently")
	}
	startedAt := time.Now().UTC().Format(time.RFC3339Nano)
	startedPayload, _ := json.Marshal(map[string]any{"task_id": taskID})
	if _, err := s.db.ExecContext(ctx, `insert into runtime_events(task_id,event_type,actor,payload_json,created_at) values(?,'task.planning.started','manager',?,?)`, taskID, string(startedPayload), startedAt); err != nil {
		return Plan{}, s.block(ctx, taskID, "planning failed: could not record start", err)
	}
	messages, err := BuildMessages(filepath.Join(s.cfg.AgentDir, "manager"), task)
	if err != nil {
		return Plan{}, s.block(ctx, taskID, "planning failed: manager prompt unavailable", err)
	}
	if s.chatter == nil {
		return Plan{}, s.block(ctx, taskID, "planning failed: manager provider unavailable", errors.New("manager chatter is unavailable"))
	}
	result, err := s.chatter.ChatAgent(ctx, "manager", messages, 4000)
	if err != nil {
		return Plan{}, s.block(ctx, taskID, "planning failed: provider request failed", err)
	}
	plan, err := ParsePlan([]byte(result.Content))
	if err != nil {
		return Plan{}, s.block(ctx, taskID, "planning failed: invalid structured response", err)
	}
	if err := s.persist(ctx, taskID, plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func (s *Service) loadTask(ctx context.Context, id int64) (tasks.Task, error) {
	var item tasks.Task
	err := s.db.QueryRowContext(ctx, `select t.id,t.project_id,p.slug,t.title,t.description,t.status from tasks t join projects p on p.id=t.project_id where t.id=?`, id).Scan(&item.ID, &item.ProjectID, &item.ProjectSlug, &item.Title, &item.Description, &item.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return tasks.Task{}, tasks.ErrNotFound
	}
	return item, err
}

func (s *Service) persist(ctx context.Context, taskID int64, plan Plan) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRowContext(ctx, `select status from tasks where id=?`, taskID).Scan(&status); err != nil {
		return err
	}
	if status != "planning" {
		return fmt.Errorf("task status changed to %q during planning", status)
	}
	created := time.Now().UTC().Format(time.RFC3339Nano)
	ids := map[string]int64{}
	for _, item := range plan.WorkItems {
		encoded, _ := json.Marshal(item)
		res, err := tx.ExecContext(ctx, `insert into task_steps(task_id,agent_name,kind,status,input,output,created_at) values(?,'unassigned',?,'created',?,'',?)`, taskID, item.Kind, string(encoded), created)
		if err != nil {
			return err
		}
		ids[item.Key], _ = res.LastInsertId()
	}
	for _, item := range plan.WorkItems {
		for _, dep := range item.DependsOn {
			if _, err := tx.ExecContext(ctx, `insert into workflow_edges(task_id,from_step_id,to_step_id,label,created_at) values(?,?,?,?,?)`, taskID, ids[dep], ids[item.Key], "depends_on", created); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `update tasks set status='planned',manager_summary=? where id=? and status='planning'`, plan.Summary, taskID); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"task_id": taskID, "work_item_count": len(plan.WorkItems), "requires_project_lead": plan.RequiresProjectLead})
	if _, err := tx.ExecContext(ctx, `insert into runtime_events(task_id,event_type,actor,payload_json,created_at) values(?,'task.planning.completed','manager',?,?)`, taskID, string(payload), created); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) loadPersisted(ctx context.Context, taskID int64) (Plan, error) {
	var plan Plan
	if err := s.db.QueryRowContext(ctx, `select manager_summary from tasks where id=?`, taskID).Scan(&plan.Summary); err != nil {
		return Plan{}, err
	}
	rows, err := s.db.QueryContext(ctx, `select input from task_steps where task_id=? order by id`, taskID)
	if err != nil {
		return Plan{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var item WorkItem
		if err := rows.Scan(&raw); err != nil {
			return Plan{}, err
		}
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			return Plan{}, err
		}
		plan.WorkItems = append(plan.WorkItems, item)
	}
	return plan, rows.Err()
}

func (s *Service) block(ctx context.Context, taskID int64, summary string, cause error) error {
	_, _ = s.db.ExecContext(ctx, `update tasks set status='blocked: planning_error',manager_summary=? where id=? and status='planning'`, summary, taskID)
	payload, _ := json.Marshal(map[string]any{"task_id": taskID, "reason": summary})
	_, _ = s.db.ExecContext(ctx, `insert into runtime_events(task_id,event_type,actor,payload_json,created_at) values(?,'task.planning.blocked','manager',?,?)`, taskID, string(payload), time.Now().UTC().Format(time.RFC3339Nano))
	return fmt.Errorf("%s: %w", summary, cause)
}
