package assignments

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/matching"
	"wanxiang-agent/server/internal/planning"
)

type Service struct {
	cfg config.Config
	db  *sql.DB
}

type Assignment struct {
	StepID    int64  `json:"step_id"`
	AgentName string `json:"agent_name"`
	ReportsTo string `json:"reports_to,omitempty"`
}

type Result struct {
	TaskID         int64        `json:"task_id"`
	Status         string       `json:"status"`
	Assignments    []Assignment `json:"assignments"`
	RequiresLead   bool         `json:"requires_lead"`
	ProjectLead    string       `json:"project_lead,omitempty"`
	GeneratedAgent string       `json:"generated_agent,omitempty"`
}

type step struct {
	id   int64
	item planning.WorkItem
}

func NewService(cfg config.Config, db *sql.DB) *Service { return &Service{cfg: cfg, db: db} }

func (s *Service) AssignTask(ctx context.Context, taskID int64) (Result, error) {
	result, found, err := s.existing(ctx, taskID)
	if err != nil || found {
		return result, err
	}
	var project, taskStatus string
	err = s.db.QueryRowContext(ctx, `select p.slug,t.status from tasks t join projects p on p.id=t.project_id where t.id=?`, taskID).Scan(&project, &taskStatus)
	if err != nil {
		return Result{}, err
	}
	if taskStatus != "planned" && taskStatus != "blocked: missing_config" {
		return Result{}, fmt.Errorf("task %d is not ready for assignment: %s", taskID, taskStatus)
	}
	steps, err := s.loadSteps(ctx, taskID)
	if err != nil {
		return Result{}, err
	}
	candidates, err := s.loadCandidates(ctx)
	if err != nil {
		return Result{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()
	result = Result{TaskID: taskID, Status: "assigned", Assignments: []Assignment{}}
	for _, current := range steps {
		match := matching.Match(matching.MatchRequest{Project: project, WorkItem: current.item}, candidates)
		if len(match.Candidates) == 0 {
			name, createErr := s.createBlockedAgent(current)
			if createErr != nil {
				return Result{}, createErr
			}
			if _, err = tx.ExecContext(ctx, `insert into agent_registry(name,role,dir,status) values(?,?,?,'blocked: missing_config') on conflict(name) do update set status=excluded.status`, name, current.item.Kind, filepath.Join(s.cfg.AgentDir, name)); err != nil {
				return Result{}, err
			}
			rejections, _ := json.Marshal(match.Rejections)
			if _, err = tx.ExecContext(ctx, `insert into agent_match_decisions(task_id,step_id,selected_agent,score,reasons_json,rejections_json,created_by,status,created_at) values(?,?,null,0,'[]',?,'system','blocked: missing_config',?)`, taskID, current.id, string(rejections), now()); err != nil {
				return Result{}, err
			}
			if _, err = tx.ExecContext(ctx, `update tasks set status='blocked: missing_config' where id=?`, taskID); err != nil {
				return Result{}, err
			}
			if err = tx.Commit(); err != nil {
				return Result{}, err
			}
			result.Status, result.GeneratedAgent = "blocked: missing_config", name
			return result, nil
		}
		selected := match.Candidates[0]
		reasons, _ := json.Marshal(selected.Reasons)
		rejections, _ := json.Marshal(match.Rejections)
		decision, execErr := tx.ExecContext(ctx, `insert into agent_match_decisions(task_id,step_id,selected_agent,score,reasons_json,rejections_json,created_by,status,created_at) values(?,?,?,?,?,?,'system','selected',?)`, taskID, current.id, selected.Name, selected.Score, string(reasons), string(rejections), now())
		if execErr != nil {
			return Result{}, execErr
		}
		decisionID, _ := decision.LastInsertId()
		if _, err = tx.ExecContext(ctx, `insert into task_assignments(task_id,step_id,agent_name,status,decision_id,created_at) values(?,?,?,'assigned',?,?)`, taskID, current.id, selected.Name, decisionID, now()); err != nil {
			return Result{}, err
		}
		if _, err = tx.ExecContext(ctx, `update task_steps set agent_name=?,status='assigned' where id=?`, selected.Name, current.id); err != nil {
			return Result{}, err
		}
		result.Assignments = append(result.Assignments, Assignment{StepID: current.id, AgentName: selected.Name})
		for index := range candidates {
			if candidates[index].Definition.Name == selected.Name {
				candidates[index].ActiveTasks++
			}
		}
	}

	requiresLead, reason, err := requiresProjectLead(ctx, tx, taskID, steps, result.Assignments)
	if err != nil {
		return Result{}, err
	}
	lead := ""
	if requiresLead && len(result.Assignments) > 0 {
		lead = result.Assignments[0].AgentName
	}
	if _, err = tx.ExecContext(ctx, `insert into team_decisions(task_id,project_lead,requires_lead,reason,created_at) values(?,?,?,?,?)`, taskID, nullable(lead), boolInt(requiresLead), reason, now()); err != nil {
		return Result{}, err
	}
	if lead != "" {
		if _, err = tx.ExecContext(ctx, `update task_assignments set reports_to=? where task_id=? and agent_name<>?`, lead, taskID, lead); err != nil {
			return Result{}, err
		}
		for index := range result.Assignments {
			if result.Assignments[index].AgentName != lead {
				result.Assignments[index].ReportsTo = lead
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `update tasks set status='assigned' where id=?`, taskID); err != nil {
		return Result{}, err
	}
	if err = tx.Commit(); err != nil {
		return Result{}, err
	}
	result.RequiresLead, result.ProjectLead = requiresLead, lead
	return result, nil
}

func (s *Service) existing(ctx context.Context, taskID int64) (Result, bool, error) {
	result := Result{TaskID: taskID, Assignments: []Assignment{}}
	rows, err := s.db.QueryContext(ctx, `select step_id,agent_name,coalesce(reports_to,'') from task_assignments where task_id=? order by step_id`, taskID)
	if err != nil {
		return result, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var a Assignment
		if err := rows.Scan(&a.StepID, &a.AgentName, &a.ReportsTo); err != nil {
			return result, false, err
		}
		result.Assignments = append(result.Assignments, a)
	}
	if err := rows.Err(); err != nil {
		return result, false, err
	}
	if len(result.Assignments) == 0 {
		return result, false, nil
	}
	result.Status = "assigned"
	var required int
	err = s.db.QueryRowContext(ctx, `select requires_lead,coalesce(project_lead,'') from team_decisions where task_id=?`, taskID).Scan(&required, &result.ProjectLead)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, false, err
	}
	result.RequiresLead = required == 1
	return result, true, nil
}

func (s *Service) loadSteps(ctx context.Context, taskID int64) ([]step, error) {
	rows, err := s.db.QueryContext(ctx, `select id,input from task_steps where task_id=? order by id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []step{}
	for rows.Next() {
		var current step
		var input string
		if err := rows.Scan(&current.id, &input); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(input), &current.item); err != nil {
			return nil, fmt.Errorf("decode step %d: %w", current.id, err)
		}
		result = append(result, current)
	}
	return result, rows.Err()
}

func (s *Service) loadCandidates(ctx context.Context) ([]matching.Candidate, error) {
	rows, err := s.db.QueryContext(ctx, `select ar.name,ar.status,count(ta.id)
		from agent_registry ar
		left join task_assignments ta on ta.agent_name=ar.name and ta.status in ('assigned','running')
		group by ar.id,ar.name,ar.status order by ar.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []matching.Candidate{}
	for rows.Next() {
		var name, status string
		var active int
		if err := rows.Scan(&name, &status, &active); err != nil {
			return nil, err
		}
		definition, err := matching.LoadDefinition(s.cfg.AgentDir, name)
		if err != nil {
			continue
		}
		result = append(result, matching.Candidate{Definition: definition, Status: status, ActiveTasks: active})
	}
	return result, rows.Err()
}

func requiresProjectLead(ctx context.Context, tx *sql.Tx, taskID int64, steps []step, assignments []Assignment) (bool, string, error) {
	for _, current := range steps {
		if current.item.Kind == "security" || current.item.Kind == "deployment" {
			return true, "high_risk_work", nil
		}
	}
	agents := map[string]bool{}
	for _, assignment := range assignments {
		agents[assignment.AgentName] = true
	}
	var edges int
	if err := tx.QueryRowContext(ctx, `select count(*) from workflow_edges where task_id=?`, taskID).Scan(&edges); err != nil {
		return false, "", err
	}
	if edges > 0 && len(agents) > 1 {
		return true, "cross_agent_dependency", nil
	}
	if len(agents) > 1 {
		return true, "multiple_agents", nil
	}
	return false, "single_agent_independent_work", nil
}

var safePart = regexp.MustCompile(`[^a-z0-9-]+`)

func (s *Service) createBlockedAgent(current step) (string, error) {
	kind := strings.ToLower(current.item.Kind)
	kind = strings.Trim(safePart.ReplaceAllString(kind, "-"), "-")
	if kind == "" {
		kind = "worker"
	}
	name := fmt.Sprintf("auto-%s-%d", kind, current.id)
	dir := filepath.Join(s.cfg.AgentDir, name)
	for _, child := range []string{"skills", "mcps", filepath.Join("memory", "summaries")} {
		if err := os.MkdirAll(filepath.Join(dir, child), 0o755); err != nil {
			return "", err
		}
	}
	definition := fmt.Sprintf("role: %s\nmax_concurrency: 1\ncapabilities:\n", kind)
	for _, capability := range current.item.RequiredCapabilities {
		definition += "  - " + capability + "\n"
	}
	definition += "project_access:\n"
	files := map[string]string{
		"agent.yaml":       definition,
		".env.example":     "# 在本地安全配置实际 Provider 凭据，不要提交密钥。\nPROVIDER_API_KEY=\n",
		"system_prompt.md": "完成分配的工作包，遵守项目验收标准；缺少配置时保持阻塞并报告。\n",
	}
	for filename, content := range files {
		if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o644); err != nil {
			return "", err
		}
	}
	return name, nil
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
