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

// NewService 创建任务规划服务。
func NewService(cfg config.Config, db *sql.DB, chatter AgentChatter) *Service {
	return &Service{cfg: cfg, db: db, chatter: chatter}
}

// PlanTask 调用 Manager 生成并持久化初版计划。
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
	if err := s.persist(ctx, taskID, 1, "planning", plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

// PlanRework 结合验收反馈生成并持久化返工计划。
func (s *Service) PlanRework(ctx context.Context, taskID, version int64, reason string) (Plan, error) {
	task, err := s.loadTask(ctx, taskID)
	if err != nil {
		return Plan{}, err
	}
	if task.Status != "rework_planning" {
		return Plan{}, fmt.Errorf("task status %q cannot be reworked", task.Status)
	}
	var snapshotSummary, evidence string
	err = s.db.QueryRowContext(ctx, `select ds.summary,ds.evidence_json from task_plan_versions pv join delivery_snapshots ds on ds.id=pv.source_snapshot_id where pv.task_id=? and pv.version=?`, taskID, version).Scan(&snapshotSummary, &evidence)
	if err != nil {
		return Plan{}, fmt.Errorf("load rework source snapshot: %w", err)
	}
	task.Description += "\n\n返工来源交付：" + snapshotSummary + "\n返工交付证据(JSON)：" + evidence + "\n用户返工意见：" + reason
	messages, err := BuildMessages(filepath.Join(s.cfg.AgentDir, "manager"), task)
	if err != nil {
		return Plan{}, err
	}
	if s.chatter == nil {
		return Plan{}, errors.New("missing_config")
	}
	result, err := s.chatter.ChatAgent(ctx, "manager", messages, 4000)
	if err != nil {
		return Plan{}, err
	}
	plan, err := ParsePlan([]byte(result.Content))
	if err != nil {
		return Plan{}, err
	}
	if err = s.persist(ctx, taskID, version, "rework_planning", plan); err != nil {
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

func (s *Service) persist(ctx context.Context, taskID, version int64, expectedStatus string, plan Plan) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRowContext(ctx, `select status from tasks where id=?`, taskID).Scan(&status); err != nil {
		return err
	}
	if status != expectedStatus {
		return fmt.Errorf("task status changed to %q during planning", status)
	}
	created := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `insert into task_plan_versions(task_id,version,status,summary,created_at) values(?,?,'planning',?,?) on conflict(task_id,version) do update set status='planning',summary=excluded.summary`, taskID, version, plan.Summary, created); err != nil {
		return err
	}
	ids := map[string]int64{}
	for _, item := range plan.WorkItems {
		encoded, _ := json.Marshal(item)
		res, err := tx.ExecContext(ctx, `insert into task_steps(task_id,agent_name,kind,status,input,output,created_at,plan_version) values(?,'unassigned',?,'created',?,'',?,?)`, taskID, item.Kind, string(encoded), created, version)
		if err != nil {
			return err
		}
		ids[item.Key], _ = res.LastInsertId()
	}
	for _, item := range plan.WorkItems {
		for _, dep := range item.DependsOn {
			if _, err := tx.ExecContext(ctx, `insert into workflow_edges(task_id,from_step_id,to_step_id,label,created_at,plan_version) values(?,?,?,?,?,?)`, taskID, ids[dep], ids[item.Key], "depends_on", created, version); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `update tasks set status='planned',manager_summary=? where id=? and status='planning'`, plan.Summary, taskID); err != nil {
		if expectedStatus == "planning" {
			return err
		}
	}
	if expectedStatus == "rework_planning" {
		if _, err := tx.ExecContext(ctx, `update tasks set status='planned',manager_summary=? where id=? and status='rework_planning'`, plan.Summary, taskID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `update task_plan_versions set status='planned',summary=? where task_id=? and version=?`, plan.Summary, taskID, version); err != nil {
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
