package assignments

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

type DecisionView struct {
	ID            int64               `json:"id"`
	StepID        int64               `json:"step_id"`
	SelectedAgent string              `json:"selected_agent,omitempty"`
	Score         float64             `json:"score"`
	Reasons       []string            `json:"reasons"`
	Rejections    []matchingRejection `json:"rejections"`
	Status        string              `json:"status"`
}
type matchingRejection struct {
	Name    string   `json:"Name"`
	Reasons []string `json:"Reasons"`
}
type MatchView struct {
	TaskID       int64          `json:"task_id"`
	Decisions    []DecisionView `json:"decisions"`
	Assignments  []Assignment   `json:"assignments"`
	RequiresLead bool           `json:"requires_lead"`
	ProjectLead  string         `json:"project_lead,omitempty"`
	LeadReason   string         `json:"lead_reason,omitempty"`
}

// GetTaskMatch 查询任务匹配结果、候选人与负责人信息。
func (s *Service) GetTaskMatch(ctx context.Context, taskID int64) (MatchView, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx, `select count(*) from tasks where id=?`, taskID).Scan(&exists); err != nil {
		return MatchView{}, err
	}
	if exists == 0 {
		return MatchView{}, sql.ErrNoRows
	}
	view := MatchView{TaskID: taskID, Decisions: []DecisionView{}, Assignments: []Assignment{}}
	rows, err := s.db.QueryContext(ctx, `select md.id,md.step_id,coalesce(md.selected_agent,''),md.score,md.reasons_json,md.rejections_json,md.status from agent_match_decisions md join task_steps ts on ts.id=md.step_id where md.task_id=? and ts.plan_version=(select coalesce(max(version),1) from task_plan_versions where task_id=?) order by md.id`, taskID, taskID)
	if err != nil {
		return MatchView{}, err
	}
	for rows.Next() {
		var item DecisionView
		var reasons, rejections string
		if err := rows.Scan(&item.ID, &item.StepID, &item.SelectedAgent, &item.Score, &reasons, &rejections, &item.Status); err != nil {
			rows.Close()
			return MatchView{}, err
		}
		_ = json.Unmarshal([]byte(reasons), &item.Reasons)
		_ = json.Unmarshal([]byte(rejections), &item.Rejections)
		view.Decisions = append(view.Decisions, item)
	}
	if err := rows.Close(); err != nil {
		return MatchView{}, err
	}
	assignmentRows, err := s.db.QueryContext(ctx, `select ta.step_id,ta.agent_name,coalesce(ta.reports_to,'') from task_assignments ta join task_steps ts on ts.id=ta.step_id where ta.task_id=? and ts.plan_version=(select coalesce(max(version),1) from task_plan_versions where task_id=?) order by ta.step_id`, taskID, taskID)
	if err != nil {
		return MatchView{}, err
	}
	for assignmentRows.Next() {
		var item Assignment
		if err := assignmentRows.Scan(&item.StepID, &item.AgentName, &item.ReportsTo); err != nil {
			assignmentRows.Close()
			return MatchView{}, err
		}
		view.Assignments = append(view.Assignments, item)
	}
	assignmentRows.Close()
	var required int
	var lead sql.NullString
	err = s.db.QueryRowContext(ctx, `select requires_lead,project_lead,reason from team_decisions where task_id=? order by plan_version desc limit 1`, taskID).Scan(&required, &lead, &view.LeadReason)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return MatchView{}, err
	}
	view.RequiresLead, view.ProjectLead = required == 1, lead.String
	return view, nil
}

// Override 人工覆盖步骤的 Agent 分配结果。
func (s *Service) Override(ctx context.Context, taskID, stepID int64, agentName, actor string) error {
	if agentName == "" || actor == "" {
		return errors.New("agent_name and actor are required")
	}
	var status string
	err := s.db.QueryRowContext(ctx, `select a.status from agent_registry a where a.name=?`, agentName).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("agent not found")
	}
	if err != nil {
		return err
	}
	if status != "online" {
		return fmt.Errorf("agent is not online")
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `select count(*) from task_steps where id=? and task_id=?`, stepID, taskID).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	reasons, _ := json.Marshal([]string{"admin_override"})
	result, err := tx.ExecContext(ctx, `insert into agent_match_decisions(task_id,step_id,selected_agent,score,reasons_json,rejections_json,created_by,status,created_at) values(?,?,?,0,?,'[]',?,'overridden',?)`, taskID, stepID, agentName, string(reasons), actor, now())
	if err != nil {
		return err
	}
	decisionID, _ := result.LastInsertId()
	if _, err = tx.ExecContext(ctx, `insert into task_assignments(task_id,step_id,agent_name,status,decision_id,created_at) values(?,?,?,'assigned',?,?) on conflict(step_id) do update set agent_name=excluded.agent_name,status='assigned',decision_id=excluded.decision_id,reports_to=null`, taskID, stepID, agentName, decisionID, now()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `update task_steps set agent_name=?,status='assigned' where id=?`, agentName, stepID); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"step_id": stepID, "agent_name": agentName})
	if _, err = tx.ExecContext(ctx, `insert into runtime_events(task_id,event_type,actor,payload_json,created_at) values(?,'task.assignment.overridden',?,?,?)`, taskID, actor, string(payload), now()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `insert into audit_logs(actor,action,target,payload_json,created_at) values(?,'assignment.override',?,?,?)`, actor, fmt.Sprintf("task:%d/step:%d", taskID, stepID), string(payload), now()); err != nil {
		return err
	}
	return tx.Commit()
}
