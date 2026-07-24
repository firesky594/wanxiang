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
	"wanxiang-agent/server/internal/files"
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

type preparedAssignment struct {
	step                 step
	selected             matching.CandidateScore
	rejections           []matching.Rejection
	projectAccessPending bool
}

type agentProjectAccessChange struct {
	name     string
	path     string
	original string
	updated  string
	mode     os.FileMode
}

type blockedAssignment struct {
	step       step
	agentName  string
	status     string
	rejections []matching.Rejection
}

// NewService 创建任务分配服务。
func NewService(cfg config.Config, db *sql.DB) *Service { return &Service{cfg: cfg, db: db} }

// AssignTask 匹配 Agent 并持久化任务步骤分配。
func (s *Service) AssignTask(ctx context.Context, taskID int64) (Result, error) {
	version, err := s.currentVersion(ctx, taskID)
	if err != nil {
		return Result{}, err
	}
	var project, taskStatus string
	err = s.db.QueryRowContext(ctx, `select p.slug,t.status from tasks t join projects p on p.id=t.project_id where t.id=?`, taskID).Scan(&project, &taskStatus)
	if err != nil {
		return Result{}, err
	}
	if !safeProject.MatchString(project) {
		return Result{}, errors.New("project slug cannot be written to an agent definition")
	}
	result, found, err := s.existing(ctx, taskID, version)
	if err != nil {
		return result, err
	}
	if found {
		if taskStatus == "planned" || taskStatus == "blocked: missing_config" || taskStatus == "blocked: missing_resources" {
			changed, updateErr := s.db.ExecContext(ctx, `update tasks set status='assigned'
				where id=? and status=? and ?=(select coalesce(max(version),1) from task_plan_versions where task_id=?)`,
				taskID, taskStatus, version, taskID)
			if updateErr != nil {
				return Result{}, updateErr
			}
			if rows, _ := changed.RowsAffected(); rows != 1 {
				return Result{}, errors.New("task assignment state changed concurrently")
			}
		}
		result.Status = "assigned"
		return result, nil
	}
	if taskStatus != "planned" && taskStatus != "blocked: missing_config" && taskStatus != "blocked: missing_resources" {
		return Result{}, fmt.Errorf("task %d is not ready for assignment: %s", taskID, taskStatus)
	}
	steps, err := s.loadSteps(ctx, taskID, version)
	if err != nil {
		return Result{}, err
	}
	if len(steps) == 0 {
		return Result{}, errors.New("task plan has no work items")
	}
	candidates, err := s.loadCandidates(ctx, taskID, version)
	if err != nil {
		return Result{}, err
	}
	prepared := make([]preparedAssignment, 0, len(steps))
	blocked := make([]blockedAssignment, 0)
	pendingProjectAccess := map[string]bool{}
	for _, current := range steps {
		match := matching.Match(matching.MatchRequest{Project: project, WorkItem: current.item}, candidates)
		if len(match.Candidates) == 0 {
			updated, granted, grantErr := s.selectBestProjectAccessGrant(ctx, taskID, version, project, current.item, candidates, prepared, match.Rejections)
			if grantErr != nil {
				return Result{}, grantErr
			}
			if granted != "" {
				candidates = updated
				match = matching.Match(matching.MatchRequest{Project: project, WorkItem: current.item}, candidates)
				if len(match.Candidates) == 0 || match.Candidates[0].Name != granted {
					return Result{}, errors.New("project access grant did not produce the selected candidate")
				}
				selected := match.Candidates[0]
				pendingProjectAccess[selected.Name] = true
				prepared = append(prepared, preparedAssignment{
					step: current, selected: selected, rejections: match.Rejections, projectAccessPending: true,
				})
				candidates = reserveCandidate(candidates, matching.Candidate{}, selected.Name)
				continue
			}
			name, prepareErr := s.prepareMappedAgent(ctx, taskID, version, project, current)
			if prepareErr != nil {
				return Result{}, prepareErr
			}
			candidate, exists, loadErr := s.loadCandidate(ctx, name, taskID, version, prepared)
			if loadErr != nil {
				return Result{}, loadErr
			}
			if exists {
				if !hasExactProjectAccess(candidate.Definition.ProjectAccess, project) {
					candidate.Definition.ProjectAccess = nil
				}
				mapped := matching.Match(matching.MatchRequest{Project: project, WorkItem: current.item}, []matching.Candidate{candidate})
				if len(mapped.Candidates) > 0 {
					prepared = append(prepared, preparedAssignment{
						step: current, selected: mapped.Candidates[0], rejections: mapped.Rejections,
						projectAccessPending: pendingProjectAccess[mapped.Candidates[0].Name],
					})
					candidates = reserveCandidate(candidates, candidate, mapped.Candidates[0].Name)
					continue
				}
				match = mapped
			}
			status := "blocked: missing_config"
			if exists && candidate.Status == "configured" {
				status = "waiting: probe"
			} else if exists && candidate.Status == "online" {
				status = "blocked: missing_resources"
			}
			blocked = append(blocked, blockedAssignment{step: current, agentName: name, status: status, rejections: match.Rejections})
			continue
		}
		selected := match.Candidates[0]
		prepared = append(prepared, preparedAssignment{
			step: current, selected: selected, rejections: match.Rejections,
			projectAccessPending: pendingProjectAccess[selected.Name],
		})
		candidates = reserveCandidate(candidates, matching.Candidate{}, selected.Name)
	}
	if len(blocked) > 0 {
		return s.persistBlocked(ctx, taskID, version, taskStatus, prepared, blocked)
	}
	changes, err := s.prepareProjectAccessChanges(prepared, project)
	if err != nil {
		return Result{}, err
	}
	if err := applyProjectAccessChanges(changes); err != nil {
		return Result{}, err
	}
	for index := range prepared {
		if prepared[index].projectAccessPending {
			prepared[index].selected.Reasons = append(prepared[index].selected.Reasons, "project_access_granted")
		}
	}
	result, err = s.persistAssignments(ctx, taskID, version, taskStatus, steps, prepared)
	if err == nil {
		return result, nil
	}
	resolveCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	persisted, found, resolveErr := s.existing(resolveCtx, taskID, version)
	cancel()
	if resolveErr == nil && found && assignmentResultMatchesPrepared(persisted, prepared) {
		return persisted, nil
	}
	if rollbackErr := rollbackProjectAccessChanges(changes); rollbackErr != nil {
		return Result{}, errors.Join(err, fmt.Errorf("rollback project access grants: %w", rollbackErr))
	}
	return Result{}, err
}

func assignmentResultMatchesPrepared(result Result, prepared []preparedAssignment) bool {
	if len(result.Assignments) != len(prepared) {
		return false
	}
	expected := make(map[int64]string, len(prepared))
	for _, item := range prepared {
		expected[item.step.id] = item.selected.Name
	}
	for _, item := range result.Assignments {
		if expected[item.StepID] != item.AgentName {
			return false
		}
		delete(expected, item.StepID)
	}
	return len(expected) == 0
}

func (s *Service) existing(ctx context.Context, taskID, version int64) (Result, bool, error) {
	result := Result{TaskID: taskID, Assignments: []Assignment{}}
	var steps, assigned int
	err := s.db.QueryRowContext(ctx, `select count(*),coalesce(sum(case when ta.id is not null and ta.agent_name=ts.agent_name then 1 else 0 end),0)
		from task_steps ts left join task_assignments ta on ta.task_id=ts.task_id and ta.step_id=ts.id
		where ts.task_id=? and ts.plan_version=?`, taskID, version).Scan(&steps, &assigned)
	if err != nil {
		return result, false, err
	}
	if steps == 0 || assigned != steps {
		return result, false, nil
	}
	rows, err := s.db.QueryContext(ctx, `select ta.step_id,ta.agent_name,coalesce(ta.reports_to,'') from task_assignments ta join task_steps ts on ts.id=ta.step_id where ta.task_id=? and ts.plan_version=? order by ta.step_id`, taskID, version)
	if err != nil {
		return result, false, err
	}
	for rows.Next() {
		var a Assignment
		if err := rows.Scan(&a.StepID, &a.AgentName, &a.ReportsTo); err != nil {
			rows.Close()
			return result, false, err
		}
		result.Assignments = append(result.Assignments, a)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, false, err
	}
	if err := rows.Close(); err != nil {
		return result, false, err
	}
	if len(result.Assignments) != steps {
		return result, false, nil
	}
	result.Status = "assigned"
	var required int
	err = s.db.QueryRowContext(ctx, `select requires_lead,coalesce(project_lead,'') from team_decisions where task_id=? and plan_version=?`, taskID, version).Scan(&required, &result.ProjectLead)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, false, err
	}
	result.RequiresLead = required == 1
	return result, true, nil
}

func (s *Service) loadSteps(ctx context.Context, taskID, version int64) ([]step, error) {
	rows, err := s.db.QueryContext(ctx, `select id,input from task_steps where task_id=? and plan_version=? order by id`, taskID, version)
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

func (s *Service) currentVersion(ctx context.Context, taskID int64) (int64, error) {
	var version int64
	err := s.db.QueryRowContext(ctx, `select coalesce(max(version),1) from task_plan_versions where task_id=?`, taskID).Scan(&version)
	return version, err
}

func (s *Service) loadCandidates(ctx context.Context, taskID, version int64) ([]matching.Candidate, error) {
	rows, err := s.db.QueryContext(ctx, `select ar.name,ar.status,count(ta.id)
			from agent_registry ar
			left join (
				select active.id,active.agent_name
				from task_assignments active
				join task_steps active_step on active_step.id=active.step_id
				where active.status in ('assigned','running','review')
				and not (active.task_id=? and active_step.plan_version=?)
			) ta on ta.agent_name=ar.name
			group by ar.id,ar.name,ar.status order by ar.name`, taskID, version)
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

func (s *Service) loadCandidate(ctx context.Context, name string, taskID, version int64, prepared []preparedAssignment) (matching.Candidate, bool, error) {
	var status string
	var active int
	err := s.db.QueryRowContext(ctx, `select ar.status,count(ta.id)
			from agent_registry ar
			left join (
				select current.id,current.agent_name
				from task_assignments current
				join task_steps current_step on current_step.id=current.step_id
				where current.status in ('assigned','running','review')
				and not (current.task_id=? and current_step.plan_version=?)
			) ta on ta.agent_name=ar.name
			where ar.name=?
			group by ar.id,ar.status`, taskID, version, name).Scan(&status, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return matching.Candidate{}, false, nil
	}
	if err != nil {
		return matching.Candidate{}, false, err
	}
	definition, err := matching.LoadDefinition(s.cfg.AgentDir, name)
	if err != nil {
		definition = matching.AgentDefinition{Name: name}
	}
	for _, assignment := range prepared {
		if assignment.selected.Name == name {
			active++
		}
	}
	return matching.Candidate{Definition: definition, Status: status, ActiveTasks: active}, true, nil
}

func reserveCandidate(candidates []matching.Candidate, refreshed matching.Candidate, selected string) []matching.Candidate {
	found := false
	for index := range candidates {
		if refreshed.Definition.Name != "" && candidates[index].Definition.Name == refreshed.Definition.Name {
			candidates[index] = refreshed
		}
		if candidates[index].Definition.Name == selected {
			candidates[index].ActiveTasks++
			found = true
		}
	}
	if refreshed.Definition.Name != "" && !found {
		refreshed.ActiveTasks++
		candidates = append(candidates, refreshed)
	}
	return candidates
}

func (s *Service) selectBestProjectAccessGrant(
	ctx context.Context,
	taskID, version int64,
	project string,
	item planning.WorkItem,
	candidates []matching.Candidate,
	prepared []preparedAssignment,
	rejections []matching.Rejection,
) ([]matching.Candidate, string, error) {
	request := matching.MatchRequest{Project: project, WorkItem: item}
	eligible := make([]matching.Candidate, 0)
	for _, candidate := range candidates {
		if !hasOnlyNamedRejection(rejections, candidate.Definition.Name, "project_access_denied") {
			continue
		}
		scored := candidate
		scored.Definition.ProjectAccess = append(append([]string{}, candidate.Definition.ProjectAccess...), project)
		eligible = append(eligible, scored)
	}
	ranked := matching.Match(request, eligible)
	for _, score := range ranked.Candidates {
		candidate, exists, err := s.loadCandidate(ctx, score.Name, taskID, version, prepared)
		if err != nil {
			return candidates, "", err
		}
		if !exists {
			continue
		}
		current := matching.Match(request, []matching.Candidate{candidate})
		if !hasOnlyRejections(current, "project_access_denied") {
			continue
		}
		candidate.Definition.ProjectAccess = append(append([]string{}, candidate.Definition.ProjectAccess...), project)
		if len(matching.Match(request, []matching.Candidate{candidate}).Candidates) == 0 {
			return candidates, "", fmt.Errorf("agent %s no longer matches after project access grant", score.Name)
		}
		for index := range candidates {
			if candidates[index].Definition.Name == score.Name {
				candidates[index] = candidate
				break
			}
		}
		return candidates, score.Name, nil
	}
	return candidates, "", nil
}

func hasOnlyNamedRejection(rejections []matching.Rejection, name, reason string) bool {
	for _, rejection := range rejections {
		if rejection.Name == name {
			return len(rejection.Reasons) == 1 && rejection.Reasons[0] == reason
		}
	}
	return false
}

func (s *Service) persistBlocked(ctx context.Context, taskID, version int64, expectedStatus string, prepared []preparedAssignment, blocked []blockedAssignment) (Result, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()
	if err := verifyTaskState(ctx, tx, taskID, version, expectedStatus); err != nil {
		return Result{}, err
	}
	if err := clearPartialAssignments(ctx, tx, taskID, version); err != nil {
		return Result{}, err
	}

	taskStatus := "blocked: missing_resources"
	mapped := make([]map[string]any, 0, len(blocked))
	stateChanged := false
	for _, item := range blocked {
		dir, pathErr := files.UnderRoot(s.cfg.AgentDir, filepath.Join(s.cfg.AgentDir, item.agentName))
		if pathErr != nil {
			return Result{}, fmt.Errorf("resolve generated agent %s: %w", item.agentName, pathErr)
		}
		if _, err = tx.ExecContext(ctx, `insert into agent_registry(name,role,dir,status)
				values(?,?,?,'blocked: missing_config')
				on conflict(name) do update set dir=excluded.dir`,
			item.agentName, sanitizeKind(item.step.item.Kind), dir); err != nil {
			return Result{}, err
		}
		decisionChanged, decisionErr := upsertBlockedDecision(ctx, tx, taskID, item)
		if decisionErr != nil {
			return Result{}, decisionErr
		}
		if decisionChanged {
			stateChanged = true
		}
		if item.status == "blocked: missing_config" || item.status == "waiting: probe" {
			taskStatus = "blocked: missing_config"
		}
		mapped = append(mapped, map[string]any{
			"step_id": item.step.id, "agent_name": item.agentName, "status": item.status,
		})
	}
	preparedChanged, err := resolvePreparedBlockedDecisions(ctx, tx, taskID, prepared)
	if err != nil {
		return Result{}, err
	}
	if preparedChanged {
		stateChanged = true
	}
	if taskStatus != expectedStatus {
		stateChanged = true
	}
	changed, err := tx.ExecContext(ctx, `update tasks set status=? where id=? and status=?`, taskStatus, taskID, expectedStatus)
	if err != nil {
		return Result{}, err
	}
	if count, countErr := changed.RowsAffected(); countErr != nil || count != 1 {
		if countErr != nil {
			return Result{}, countErr
		}
		return Result{}, errors.New("task assignment state changed concurrently")
	}
	if stateChanged {
		payload, encodeErr := json.Marshal(map[string]any{
			"plan_version": version,
			"agents":       mapped,
			"ready_steps":  len(prepared),
		})
		if encodeErr != nil {
			return Result{}, encodeErr
		}
		if _, err = tx.ExecContext(ctx, `insert into runtime_events(task_id,event_type,actor,payload_json,created_at)
				values(?,'task.assignment.blocked','manager',?,?)`, taskID, string(payload), now()); err != nil {
			return Result{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Result{}, err
	}
	result := Result{TaskID: taskID, Status: taskStatus, Assignments: []Assignment{}}
	if len(blocked) > 0 {
		result.GeneratedAgent = blocked[0].agentName
	}
	return result, nil
}

func (s *Service) persistAssignments(ctx context.Context, taskID, version int64, expectedStatus string, steps []step, prepared []preparedAssignment) (Result, error) {
	if len(prepared) != len(steps) {
		return Result{}, errors.New("not all task steps passed assignment preflight")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()
	if err := verifyTaskState(ctx, tx, taskID, version, expectedStatus); err != nil {
		return Result{}, err
	}
	if err := clearPartialAssignments(ctx, tx, taskID, version); err != nil {
		return Result{}, err
	}

	result := Result{TaskID: taskID, Status: "assigned", Assignments: make([]Assignment, 0, len(prepared))}
	for _, item := range prepared {
		reasons, err := json.Marshal(item.selected.Reasons)
		if err != nil {
			return Result{}, err
		}
		rejections, err := json.Marshal(item.rejections)
		if err != nil {
			return Result{}, err
		}
		decision, err := tx.ExecContext(ctx, `insert into agent_match_decisions(
				task_id,step_id,selected_agent,score,reasons_json,rejections_json,created_by,status,created_at
			) values(?,?,?,?,?,?,'system','selected',?)`,
			taskID, item.step.id, item.selected.Name, item.selected.Score, string(reasons), string(rejections), now())
		if err != nil {
			return Result{}, err
		}
		decisionID, err := decision.LastInsertId()
		if err != nil {
			return Result{}, err
		}
		if _, err = tx.ExecContext(ctx, `insert into task_assignments(
				task_id,step_id,agent_name,status,decision_id,created_at
			) values(?,?,?,'assigned',?,?)`,
			taskID, item.step.id, item.selected.Name, decisionID, now()); err != nil {
			return Result{}, err
		}
		changed, err := tx.ExecContext(ctx, `update task_steps set agent_name=?,status='assigned'
			where id=? and task_id=? and plan_version=? and status='created'`,
			item.selected.Name, item.step.id, taskID, version)
		if err != nil {
			return Result{}, err
		}
		if count, countErr := changed.RowsAffected(); countErr != nil || count != 1 {
			if countErr != nil {
				return Result{}, countErr
			}
			return Result{}, fmt.Errorf("step %d assignment state changed concurrently", item.step.id)
		}
		result.Assignments = append(result.Assignments, Assignment{StepID: item.step.id, AgentName: item.selected.Name})
	}

	requiresLead, reason, err := requiresProjectLead(ctx, tx, taskID, steps, result.Assignments)
	if err != nil {
		return Result{}, err
	}
	lead := ""
	if len(result.Assignments) > 0 {
		lead = result.Assignments[0].AgentName
	}
	if _, err = tx.ExecContext(ctx, `insert into team_decisions(
			task_id,plan_version,project_lead,requires_lead,reason,created_at
		) values(?,?,?,?,?,?)
		on conflict(task_id,plan_version) do update set
			project_lead=excluded.project_lead,
			requires_lead=excluded.requires_lead,
			reason=excluded.reason,
			created_at=excluded.created_at`,
		taskID, version, nullable(lead), boolInt(requiresLead), reason, now()); err != nil {
		return Result{}, err
	}
	if lead != "" {
		if _, err = tx.ExecContext(ctx, `update task_assignments set reports_to=?
			where task_id=? and step_id in (
				select id from task_steps where task_id=? and plan_version=?
			) and agent_name<>?`, lead, taskID, taskID, version, lead); err != nil {
			return Result{}, err
		}
		for index := range result.Assignments {
			if result.Assignments[index].AgentName != lead {
				result.Assignments[index].ReportsTo = lead
			}
		}
	}
	changed, err := tx.ExecContext(ctx, `update tasks set status='assigned' where id=? and status=?`, taskID, expectedStatus)
	if err != nil {
		return Result{}, err
	}
	if count, countErr := changed.RowsAffected(); countErr != nil || count != 1 {
		if countErr != nil {
			return Result{}, countErr
		}
		return Result{}, errors.New("task assignment state changed concurrently")
	}
	payload, err := json.Marshal(map[string]any{
		"plan_version": version, "assignments": len(result.Assignments),
		"project_lead": lead, "requires_lead": requiresLead,
	})
	if err != nil {
		return Result{}, err
	}
	if _, err = tx.ExecContext(ctx, `insert into runtime_events(task_id,event_type,actor,payload_json,created_at)
			values(?,'task.assignment.completed','manager',?,?)`, taskID, string(payload), now()); err != nil {
		return Result{}, err
	}
	if err := tx.Commit(); err != nil {
		return Result{}, err
	}
	result.RequiresLead, result.ProjectLead = requiresLead, lead
	return result, nil
}

func verifyTaskState(ctx context.Context, tx *sql.Tx, taskID, version int64, expectedStatus string) error {
	var currentStatus string
	var currentVersion int64
	err := tx.QueryRowContext(ctx, `select status,
		(select coalesce(max(version),1) from task_plan_versions where task_id=tasks.id)
		from tasks where id=?`, taskID).Scan(&currentStatus, &currentVersion)
	if err != nil {
		return err
	}
	if currentStatus != expectedStatus || currentVersion != version {
		return errors.New("task assignment state changed concurrently")
	}
	return nil
}

func clearPartialAssignments(ctx context.Context, tx *sql.Tx, taskID, version int64) error {
	var runtimeRows int
	err := tx.QueryRowContext(ctx, `select
		(select count(*) from project_workspaces pw join task_steps ts on ts.id=pw.step_id
			where ts.task_id=? and ts.plan_version=?)
		+
		(select count(*) from task_step_leases l join task_steps ts on ts.id=l.step_id
			where ts.task_id=? and ts.plan_version=?)`,
		taskID, version, taskID, version).Scan(&runtimeRows)
	if err != nil {
		return err
	}
	if runtimeRows > 0 {
		return errors.New("cannot replace partial assignments after workspace or lease creation")
	}
	var unsafeSteps int
	if err := tx.QueryRowContext(ctx, `select count(*) from task_steps
		where task_id=? and plan_version=? and status not in ('created','assigned')`,
		taskID, version).Scan(&unsafeSteps); err != nil {
		return err
	}
	if unsafeSteps > 0 {
		return errors.New("cannot replace partial assignments after step execution started")
	}
	if _, err := tx.ExecContext(ctx, `delete from task_assignments
		where task_id=? and step_id in (
			select id from task_steps where task_id=? and plan_version=?
		)`, taskID, taskID, version); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `update task_steps set agent_name='unassigned',status='created'
		where task_id=? and plan_version=? and status in ('created','assigned')`, taskID, version)
	return err
}

func upsertBlockedDecision(ctx context.Context, tx *sql.Tx, taskID int64, item blockedAssignment) (bool, error) {
	reasons, err := json.Marshal([]string{"generated_agent_mapping"})
	if err != nil {
		return false, err
	}
	rejections, err := json.Marshal(item.rejections)
	if err != nil {
		return false, err
	}
	var decisionID int64
	var selectedAgent, currentReasons, currentRejections, currentStatus string
	err = tx.QueryRowContext(ctx, `select id,coalesce(selected_agent,''),reasons_json,rejections_json,status
			from agent_match_decisions
			where task_id=? and step_id=? and created_by='system'
			order by id desc limit 1`, taskID, item.step.id).
		Scan(&decisionID, &selectedAgent, &currentReasons, &currentRejections, &currentStatus)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !isBlockedDecisionStatus(currentStatus)) {
		_, err = tx.ExecContext(ctx, `insert into agent_match_decisions(
				task_id,step_id,selected_agent,score,reasons_json,rejections_json,created_by,status,created_at
			) values(?,?,?,0,?,?,'system',?,?)`,
			taskID, item.step.id, item.agentName, string(reasons), string(rejections), item.status, now())
		return err == nil, err
	}
	if err != nil {
		return false, err
	}
	if selectedAgent == item.agentName &&
		currentReasons == string(reasons) &&
		currentRejections == string(rejections) &&
		currentStatus == item.status {
		if item.status == "blocked: missing_resources" {
			_, err = tx.ExecContext(ctx, `update agent_match_decisions set created_at=? where id=?`, now(), decisionID)
		}
		return false, err
	}
	_, err = tx.ExecContext(ctx, `update agent_match_decisions set
		selected_agent=?,score=0,reasons_json=?,rejections_json=?,status=?,created_at=?
		where id=?`,
		item.agentName, string(reasons), string(rejections), item.status, now(), decisionID)
	return err == nil, err
}

func resolvePreparedBlockedDecisions(ctx context.Context, tx *sql.Tx, taskID int64, prepared []preparedAssignment) (bool, error) {
	changed := false
	for _, item := range prepared {
		if item.projectAccessPending {
			continue
		}
		reasons, err := json.Marshal(item.selected.Reasons)
		if err != nil {
			return false, err
		}
		rejections, err := json.Marshal(item.rejections)
		if err != nil {
			return false, err
		}
		var decisionID int64
		err = tx.QueryRowContext(ctx, `select id from agent_match_decisions
				where task_id=? and step_id=? and created_by='system'
				and status in ('blocked: missing_config','waiting: probe','blocked: missing_resources')
				and id=(select max(latest.id) from agent_match_decisions latest
					where latest.task_id=? and latest.step_id=? and latest.created_by='system')
				order by id desc limit 1`, taskID, item.step.id, taskID, item.step.id).Scan(&decisionID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		result, err := tx.ExecContext(ctx, `update agent_match_decisions set
			selected_agent=?,score=?,reasons_json=?,rejections_json=?,status='ready: assignment',created_at=?
			where id=? and status in ('blocked: missing_config','waiting: probe','blocked: missing_resources')`,
			item.selected.Name, item.selected.Score, string(reasons), string(rejections), now(), decisionID)
		if err != nil {
			return false, err
		}
		if count, countErr := result.RowsAffected(); countErr != nil {
			return false, countErr
		} else if count == 1 {
			changed = true
		}
	}
	return changed, nil
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

var safeProject = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

const maxGeneratedAgentFileBytes = 64 * 1024
const legacyGeneratedEnvExample = "# 在本地安全配置实际 Provider 凭据，不要提交密钥。\nPROVIDER_API_KEY=\n"
const generatedEnvExample = "# 在本地安全配置实际 Provider 凭据，不要提交密钥。\nAGENT_PROVIDER_TYPE=openai\nAGENT_API_KEY=\nAGENT_BASE_URL=https://api.openai.com/v1\nAGENT_MODEL=\n"

func (s *Service) prepareMappedAgent(ctx context.Context, taskID, version int64, project string, current step) (string, error) {
	if !safeProject.MatchString(project) {
		return "", errors.New("project slug cannot be written to an agent definition")
	}
	kind := sanitizeKind(current.item.Kind)
	generated := fmt.Sprintf("sub-%d-%s", current.id, kind)
	legacy := fmt.Sprintf("auto-%s-%d", kind, current.id)

	mapped, found, err := mappedGeneratedAgent(ctx, s.db, taskID, version, current.id)
	if err != nil {
		return "", err
	}
	if found && (mapped == generated || mapped == legacy) {
		if mapped == generated {
			if err := s.ensureGeneratedAgent(generated, project, current); err != nil {
				return "", err
			}
			if err := s.repairGeneratedAccess(generated, project, current.item); err != nil {
				return "", err
			}
		} else {
			if _, err := s.compensateLegacyAccess(ctx, legacy, project, current.item); err != nil {
				return "", err
			}
			if err := s.upgradeGeneratedEnvExample(legacy); err != nil {
				return "", err
			}
		}
		return mapped, nil
	}

	if compensated, err := s.compensateLegacyAccess(ctx, legacy, project, current.item); err != nil {
		return "", err
	} else if compensated {
		if err := s.upgradeGeneratedEnvExample(legacy); err != nil {
			return "", err
		}
		return legacy, nil
	}
	if err := s.ensureGeneratedAgent(generated, project, current); err != nil {
		return "", err
	}
	if err := s.repairGeneratedAccess(generated, project, current.item); err != nil {
		return "", err
	}
	return generated, nil
}

func mappedGeneratedAgent(ctx context.Context, db *sql.DB, taskID, version, stepID int64) (string, bool, error) {
	var name string
	err := db.QueryRowContext(ctx, `select coalesce(md.selected_agent,'')
			from agent_match_decisions md
			join task_steps ts on ts.id=md.step_id
			where md.task_id=? and md.step_id=? and ts.plan_version=?
			and md.created_by='system'
			and md.status in ('blocked: missing_config','waiting: probe','blocked: missing_resources')
			and md.selected_agent is not null
			and md.selected_agent<>''
			and md.id=(select max(latest.id) from agent_match_decisions latest
				where latest.task_id=? and latest.step_id=? and latest.created_by='system')
			order by md.id desc limit 1`, taskID, stepID, version, taskID, stepID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return name, err == nil, err
}

func (s *Service) ensureGeneratedAgent(name, project string, current step) error {
	dir, err := files.UnderRoot(s.cfg.AgentDir, filepath.Join(s.cfg.AgentDir, name))
	if err != nil {
		return fmt.Errorf("resolve generated agent directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "memory", "summaries"), 0o755); err != nil {
		return err
	}
	if err := ensureRegularDirectory(dir); err != nil {
		return err
	}
	if err := ensureRegularDirectory(filepath.Join(dir, "memory")); err != nil {
		return err
	}
	if err := ensureRegularDirectory(filepath.Join(dir, "memory", "summaries")); err != nil {
		return err
	}

	var definition strings.Builder
	definition.WriteString("role: ")
	definition.WriteString(sanitizeKind(current.item.Kind))
	definition.WriteString("\nmax_concurrency: 1\ncapabilities:\n")
	for _, capability := range current.item.RequiredCapabilities {
		value := strings.TrimSpace(capability)
		if value == "" || strings.ContainsAny(value, "\r\n") {
			return errors.New("agent capability contains an unsafe value")
		}
		definition.WriteString("  - ")
		definition.WriteString(value)
		definition.WriteByte('\n')
	}
	definition.WriteString("project_access:\n  - ")
	definition.WriteString(project)
	definition.WriteByte('\n')

	entries := []struct {
		name    string
		content string
		mode    os.FileMode
	}{
		{name: "agent.yaml", content: definition.String(), mode: 0o644},
		{name: ".env.example", content: generatedEnvExample, mode: 0o644},
		{name: "system_prompt.md", content: "完成分配的工作包，遵守项目验收标准；缺少配置或资源时保持阻塞并报告。\n", mode: 0o644},
	}
	for _, entry := range entries {
		if err := writeFileOnce(filepath.Join(dir, entry.name), entry.content, entry.mode); err != nil {
			return err
		}
	}
	return s.upgradeGeneratedEnvExample(name)
}

func (s *Service) upgradeGeneratedEnvExample(name string) error {
	path, err := files.UnderRoot(s.cfg.AgentDir, filepath.Join(s.cfg.AgentDir, name, ".env.example"))
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxGeneratedAgentFileBytes {
		return errors.New("generated agent env example is not a safe regular file")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(content) != legacyGeneratedEnvExample {
		return nil
	}
	return replaceRegularFile(path, generatedEnvExample, info.Mode().Perm())
}

func (s *Service) repairGeneratedAccess(name, project string, item planning.WorkItem) error {
	definition, err := matching.LoadDefinition(s.cfg.AgentDir, name)
	if err != nil {
		return nil
	}
	if hasExactProjectAccess(definition.ProjectAccess, project) {
		return nil
	}
	if len(definition.ProjectAccess) != 0 {
		return nil
	}
	repairable := onlyProjectAccessRejected(project, item, definition)
	if !repairable {
		return nil
	}
	return s.appendProjectAccess(name, project)
}

func (s *Service) compensateLegacyAccess(ctx context.Context, name, project string, item planning.WorkItem) (bool, error) {
	exists, err := s.agentDefinitionExists(name)
	if err != nil || !exists {
		return false, err
	}
	configured, err := s.hasLegacyConfiguration(ctx, name)
	if err != nil || !configured {
		return false, err
	}
	definition, err := matching.LoadDefinition(s.cfg.AgentDir, name)
	if err != nil {
		return false, nil
	}
	if hasExactProjectAccess(definition.ProjectAccess, project) {
		return onlyStatusRejected(project, item, definition), nil
	}
	if len(definition.ProjectAccess) != 0 || !onlyProjectAccessRejected(project, item, definition) {
		return false, nil
	}
	if err := s.appendProjectAccess(name, project); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) agentDefinitionExists(name string) (bool, error) {
	path, err := files.UnderRoot(s.cfg.AgentDir, filepath.Join(s.cfg.AgentDir, name, "agent.yaml"))
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxGeneratedAgentFileBytes {
		return false, errors.New("agent definition is not a safe regular file")
	}
	return true, nil
}

func (s *Service) hasLegacyConfiguration(ctx context.Context, name string) (bool, error) {
	var status string
	err := s.db.QueryRowContext(ctx, `select status from agent_registry where name=?`, name).Scan(&status)
	if err == nil && (status == "configured" || status == "online") {
		return true, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	envPath, err := files.UnderRoot(s.cfg.AgentDir, filepath.Join(s.cfg.AgentDir, name, "env"))
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(envPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular() &&
		info.Mode()&os.ModeSymlink == 0 &&
		info.Mode().Perm()&0o077 == 0 &&
		info.Size() <= maxGeneratedAgentFileBytes, nil
}

func onlyProjectAccessRejected(project string, item planning.WorkItem, definition matching.AgentDefinition) bool {
	match := matching.Match(matching.MatchRequest{Project: project, WorkItem: item}, []matching.Candidate{{
		Definition: definition,
		Status:     "online",
	}})
	return hasOnlyRejections(match, "project_access_denied")
}

func onlyStatusRejected(project string, item planning.WorkItem, definition matching.AgentDefinition) bool {
	match := matching.Match(matching.MatchRequest{Project: project, WorkItem: item}, []matching.Candidate{{
		Definition: definition,
		Status:     "configured",
	}})
	return hasOnlyRejections(match, "status_offline")
}

func hasOnlyRejections(result matching.MatchResult, reason string) bool {
	return len(result.Candidates) == 0 &&
		len(result.Rejections) == 1 &&
		len(result.Rejections[0].Reasons) == 1 &&
		result.Rejections[0].Reasons[0] == reason
}

func (s *Service) appendProjectAccess(name, project string) error {
	return s.appendAgentProjectAccess(name, project, true)
}

func (s *Service) appendAgentProjectAccess(name, project string, requireEmpty bool) error {
	change, changed, err := s.prepareAgentProjectAccessChange(name, project, requireEmpty)
	if err != nil || !changed {
		return err
	}
	return replaceRegularFile(change.path, change.updated, change.mode)
}

func (s *Service) prepareProjectAccessChanges(prepared []preparedAssignment, project string) ([]agentProjectAccessChange, error) {
	seen := map[string]bool{}
	changes := make([]agentProjectAccessChange, 0)
	for _, item := range prepared {
		name := item.selected.Name
		if !item.projectAccessPending || seen[name] {
			continue
		}
		seen[name] = true
		change, changed, err := s.prepareAgentProjectAccessChange(name, project, false)
		if err != nil {
			return nil, fmt.Errorf("prepare project access grant for %s: %w", name, err)
		}
		if changed {
			changes = append(changes, change)
		}
	}
	return changes, nil
}

func applyProjectAccessChanges(changes []agentProjectAccessChange) error {
	applied := make([]agentProjectAccessChange, 0, len(changes))
	for _, change := range changes {
		content, err := os.ReadFile(change.path)
		if err != nil {
			if rollbackErr := rollbackProjectAccessChanges(applied); rollbackErr != nil {
				return errors.Join(err, fmt.Errorf("rollback project access grants: %w", rollbackErr))
			}
			return err
		}
		if string(content) != change.original {
			err = errors.New("agent definition changed before project access grant")
		} else {
			err = replaceRegularFile(change.path, change.updated, change.mode)
		}
		if err != nil {
			if rollbackErr := rollbackProjectAccessChanges(applied); rollbackErr != nil {
				return errors.Join(err, fmt.Errorf("rollback project access grants: %w", rollbackErr))
			}
			return err
		}
		applied = append(applied, change)
	}
	return nil
}

func rollbackProjectAccessChanges(changes []agentProjectAccessChange) error {
	var rollbackErrors []error
	for index := len(changes) - 1; index >= 0; index-- {
		change := changes[index]
		content, err := os.ReadFile(change.path)
		if err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("%s: %w", change.name, err))
			continue
		}
		if string(content) != change.updated {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("%s: agent definition changed after project access grant", change.name))
			continue
		}
		if err := replaceRegularFile(change.path, change.original, change.mode); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("%s: %w", change.name, err))
		}
	}
	return errors.Join(rollbackErrors...)
}

func (s *Service) prepareAgentProjectAccessChange(name, project string, requireEmpty bool) (agentProjectAccessChange, bool, error) {
	change := agentProjectAccessChange{name: name}
	path, err := files.UnderRoot(s.cfg.AgentDir, filepath.Join(s.cfg.AgentDir, name, "agent.yaml"))
	if err != nil {
		return change, false, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return change, false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxGeneratedAgentFileBytes {
		return change, false, errors.New("agent definition is not a safe regular file")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return change, false, err
	}
	lines := strings.Split(string(content), "\n")
	index := -1
	insertAt := -1
	accessValues := []string{}
	for current, line := range lines {
		if len(line) == len(strings.TrimLeft(line, " \t")) && strings.TrimSpace(line) == "project_access:" {
			if index >= 0 {
				return change, false, errors.New("agent definition has duplicate project_access blocks")
			}
			index = current
			insertAt = current + 1
		}
	}
	if index < 0 {
		return change, false, errors.New("agent definition has no project_access block")
	}
	for current := index + 1; current < len(lines); current++ {
		line := lines[current]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			insertAt = current + 1
			continue
		}
		if len(line) == len(strings.TrimLeft(line, " \t")) {
			break
		}
		if strings.HasPrefix(trimmed, "- ") {
			value := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			accessValues = append(accessValues, value)
			insertAt = current + 1
			continue
		}
		return change, false, errors.New("agent definition has an unsupported project_access entry")
	}
	for _, value := range accessValues {
		if value == "*" {
			return change, false, errors.New("wildcard project access cannot be extended automatically")
		}
		if value == project {
			return change, false, nil
		}
	}
	if requireEmpty && len(accessValues) > 0 {
		return change, false, errors.New("agent definition project_access is no longer empty")
	}
	lines = append(lines[:insertAt], append([]string{"  - " + project}, lines[insertAt:]...)...)
	change.path = path
	change.original = string(content)
	change.updated = strings.Join(lines, "\n")
	change.mode = info.Mode().Perm()
	return change, true, nil
}

func writeFileOnce(path, content string, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("generated agent file is not a safe regular file")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if errors.Is(err, os.ErrExist) {
		return writeFileOnce(path, content, mode)
	}
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.WriteString(content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	keep = true
	return nil
}

func replaceRegularFile(path, content string, mode os.FileMode) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".agent-definition-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	keep := false
	defer func() {
		_ = temp.Close()
		if !keep {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(mode); err != nil {
		return err
	}
	if _, err := temp.WriteString(content); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	keep = true
	return nil
}

func ensureRegularDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("generated agent path is not a safe directory")
	}
	return nil
}

func sanitizeKind(value string) string {
	kind := strings.ToLower(value)
	kind = strings.Trim(safePart.ReplaceAllString(kind, "-"), "-")
	if kind == "" {
		return "worker"
	}
	if len(kind) > 32 {
		kind = strings.TrimRight(kind[:32], "-")
	}
	if kind == "" {
		return "worker"
	}
	return kind
}

func hasExactProjectAccess(items []string, want string) bool {
	return len(items) == 1 && strings.TrimSpace(items[0]) == want
}

func isBlockedDecisionStatus(status string) bool {
	switch status {
	case "blocked: missing_config", "waiting: probe", "blocked: missing_resources":
		return true
	default:
		return false
	}
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
