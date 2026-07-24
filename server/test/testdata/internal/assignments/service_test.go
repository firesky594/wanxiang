package assignments

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/matching"
	"wanxiang-agent/server/internal/planning"
	"wanxiang-agent/server/internal/testutil"
)

func TestAssignTaskPersistsDecisionsAssignmentsAndLeadIdempotently(t *testing.T) {
	cfg, conn, taskID := assignmentFixture(t)
	writeAgent(t, cfg, "api-agent", "backend", []string{"go"}, 1)
	writeAgent(t, cfg, "web-agent", "frontend", []string{"vue"}, 1)
	registerAgent(t, conn, cfg, "api-agent", "backend", "online")
	registerAgent(t, conn, cfg, "web-agent", "frontend", "online")

	result, err := NewService(cfg, conn).AssignTask(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "assigned" || len(result.Assignments) != 2 || !result.RequiresLead {
		t.Fatalf("result=%+v", result)
	}
	if result.Assignments[0].AgentName != "api-agent" || result.Assignments[1].AgentName != "web-agent" {
		t.Fatalf("assignments=%+v", result.Assignments)
	}
	if _, err := NewService(cfg, conn).AssignTask(t.Context(), taskID); err != nil {
		t.Fatal(err)
	}
	assertTableCount(t, conn, "agent_match_decisions", 2)
	assertTableCount(t, conn, "task_assignments", 2)
	assertTableCount(t, conn, "team_decisions", 1)
	var taskStatus string
	if err := conn.QueryRow(`select status from tasks where id=?`, taskID).Scan(&taskStatus); err != nil || taskStatus != "assigned" {
		t.Fatalf("status=%q err=%v", taskStatus, err)
	}
}

func TestAssignTaskAssignsNewestReworkPlanWithoutReusingOldAssignments(t *testing.T) {
	cfg, conn, taskID := assignmentFixture(t)
	writeAgent(t, cfg, "api-agent", "backend", []string{"go"}, 1)
	writeAgent(t, cfg, "web-agent", "frontend", []string{"vue"}, 1)
	registerAgent(t, conn, cfg, "api-agent", "backend", "online")
	registerAgent(t, conn, cfg, "web-agent", "frontend", "online")
	svc := NewService(cfg, conn)
	if _, err := svc.AssignTask(t.Context(), taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`update task_assignments set status='completed' where task_id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	item := planning.WorkItem{Key: "fix", Title: "Fix", Kind: "backend", RequiredCapabilities: []string{"go"}}
	input, _ := json.Marshal(item)
	if _, err := conn.Exec(`insert into task_plan_versions(task_id,version,status,summary,created_at) values(?,2,'planned','rework','now')`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`insert into task_steps(task_id,agent_name,kind,status,input,created_at,plan_version) values(?,'unassigned','backend','created',?,'now',2)`, taskID, input); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`update tasks set status='planned' where id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	result, err := svc.AssignTask(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Assignments) != 1 {
		t.Fatalf("assignments=%+v", result.Assignments)
	}
	var decisions int
	if err := conn.QueryRow(`select count(*) from team_decisions where task_id=?`, taskID).Scan(&decisions); err != nil || decisions != 2 {
		t.Fatalf("decisions=%d err=%v", decisions, err)
	}
}

func TestAssignTaskCreatesNonSecretBlockedAgentWhenNoCandidate(t *testing.T) {
	cfg, conn, taskID := assignmentFixture(t)
	if _, err := conn.Exec(`delete from task_steps where task_id=? and kind='frontend'`, taskID); err != nil {
		t.Fatal(err)
	}

	result, err := NewService(cfg, conn).AssignTask(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "blocked: missing_config" || result.GeneratedAgent == "" {
		t.Fatalf("result=%+v", result)
	}
	dir := filepath.Join(cfg.AgentDir, result.GeneratedAgent)
	definition, err := os.ReadFile(filepath.Join(dir, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(definition)), "api_key") || strings.Contains(string(definition), "secret") {
		t.Fatalf("definition contains secret field: %s", definition)
	}
	if _, err := os.Stat(filepath.Join(dir, ".env.example")); err != nil {
		t.Fatal(err)
	}
	var registryStatus, taskStatus string
	if err := conn.QueryRow(`select status from agent_registry where name=?`, result.GeneratedAgent).Scan(&registryStatus); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(`select status from tasks where id=?`, taskID).Scan(&taskStatus); err != nil {
		t.Fatal(err)
	}
	if registryStatus != "blocked: missing_config" || taskStatus != "blocked: missing_config" {
		t.Fatalf("registry=%q task=%q", registryStatus, taskStatus)
	}
	assertTableCount(t, conn, "task_assignments", 0)
}

func TestAdminOverrideWritesDecisionEventAndAudit(t *testing.T) {
	cfg, conn, taskID := assignmentFixture(t)
	writeAgent(t, cfg, "api-agent", "backend", []string{"go"}, 1)
	writeAgent(t, cfg, "replacement", "backend", []string{"go"}, 2)
	registerAgent(t, conn, cfg, "api-agent", "backend", "online")
	registerAgent(t, conn, cfg, "replacement", "backend", "online")
	svc := NewService(cfg, conn)
	if _, err := svc.AssignTask(t.Context(), taskID); err != nil {
		t.Fatal(err)
	}
	var stepID int64
	if err := conn.QueryRow(`select id from task_steps where task_id=? order by id limit 1`, taskID).Scan(&stepID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Override(t.Context(), taskID, stepID, "replacement", "admin"); err != nil {
		t.Fatal(err)
	}
	var assigned string
	if err := conn.QueryRow(`select agent_name from task_assignments where step_id=?`, stepID).Scan(&assigned); err != nil {
		t.Fatal(err)
	}
	if assigned != "replacement" {
		t.Fatalf("assigned=%q", assigned)
	}
	assertTableCount(t, conn, "audit_logs", 1)
	var events int
	if err := conn.QueryRow(`select count(*) from runtime_events where event_type='task.assignment.overridden'`).Scan(&events); err != nil || events != 1 {
		t.Fatalf("events=%d err=%v", events, err)
	}
}

func TestProjectAccessGrantRollsBackAfterDefinitePersistenceFailure(t *testing.T) {
	cfg, conn, taskID := assignmentFixture(t)
	if _, err := conn.Exec(`delete from task_steps where task_id=? and kind='frontend'`, taskID); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cfg.AgentDir, "api-agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	definition := "role: backend\nmax_concurrency: 1\ncapabilities:\n  - go\nproject_access:\n"
	path := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(path, []byte(definition), 0o644); err != nil {
		t.Fatal(err)
	}
	registerAgent(t, conn, cfg, "api-agent", "backend", "online")
	if _, err := conn.Exec(`create trigger reject_assignment_event before insert on runtime_events
		when new.event_type='task.assignment.completed'
		begin
			select raise(abort, 'forced assignment event failure');
		end`); err != nil {
		t.Fatal(err)
	}

	if _, err := NewService(cfg, conn).AssignTask(t.Context(), taskID); err == nil {
		t.Fatal("expected assignment persistence failure")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != definition {
		t.Fatalf("project access file was not rolled back:\n%s", content)
	}
	assertTableCount(t, conn, "task_assignments", 0)
}

func TestResolveAssignmentCommitOutcomeDistinguishesConfirmedAndUnknown(t *testing.T) {
	cfg, conn, taskID := assignmentFixture(t)
	writeAgent(t, cfg, "api-agent", "backend", []string{"go"}, 1)
	writeAgent(t, cfg, "web-agent", "frontend", []string{"vue"}, 1)
	registerAgent(t, conn, cfg, "api-agent", "backend", "online")
	registerAgent(t, conn, cfg, "web-agent", "frontend", "online")
	svc := NewService(cfg, conn)
	result, err := svc.AssignTask(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	prepared := make([]preparedAssignment, 0, len(result.Assignments))
	for _, assignment := range result.Assignments {
		prepared = append(prepared, preparedAssignment{
			step:     step{id: assignment.StepID},
			selected: matching.CandidateScore{Name: assignment.AgentName},
		})
	}
	persisted, found, err := svc.resolveAssignmentCommitOutcome(taskID, 1, prepared)
	if err != nil || !found || !assignmentResultMatchesPrepared(persisted, prepared) {
		t.Fatalf("persisted=%+v found=%v err=%v", persisted, found, err)
	}

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := svc.resolveAssignmentCommitOutcome(taskID, 1, prepared); found || !errors.Is(err, errAssignmentCommitOutcomeUncertain) {
		t.Fatalf("found=%v err=%v", found, err)
	}
}

func TestGeneratedAgentMappingUsesTrueLatestSystemDecision(t *testing.T) {
	_, conn, taskID := assignmentFixture(t)
	var stepID int64
	if err := conn.QueryRow(`select id from task_steps where task_id=? order by id limit 1`, taskID).Scan(&stepID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`insert into agent_match_decisions(
		task_id,step_id,selected_agent,reasons_json,rejections_json,created_by,status,created_at
	) values(?,?,?,'[]','[]','system','blocked: missing_config','2026-07-24T01:00:00Z')`,
		taskID, stepID, "sub-old"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`insert into agent_match_decisions(
		task_id,step_id,selected_agent,reasons_json,rejections_json,created_by,status,created_at
	) values(?,?,?,'[]','[]','system','ready: assignment','2026-07-24T02:00:00Z')`,
		taskID, stepID, "api-agent"); err != nil {
		t.Fatal(err)
	}

	if name, found, err := mappedGeneratedAgent(t.Context(), conn, taskID, 1, stepID); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatalf("stale blocked mapping was resurrected: %q", name)
	}
}

func TestReviewAssignmentsConsumeAgentConcurrency(t *testing.T) {
	cfg, conn, taskID := assignmentFixture(t)
	writeAgent(t, cfg, "api-agent", "backend", []string{"go"}, 1)
	registerAgent(t, conn, cfg, "api-agent", "backend", "online")
	projectResult, err := conn.Exec(`insert into projects(slug,dir,status,remote_url,created_at)
		values('other-project','/tmp/other-project','created','','now')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := projectResult.LastInsertId()
	taskResult, err := conn.Exec(`insert into tasks(project_id,title,description,status,created_at)
		values(?,'other','other','review','now')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	otherTaskID, _ := taskResult.LastInsertId()
	item, _ := json.Marshal(planning.WorkItem{Key: "review", Kind: "backend"})
	stepResult, err := conn.Exec(`insert into task_steps(task_id,agent_name,kind,status,input,created_at)
		values(?,'api-agent','backend','review',?,'now')`, otherTaskID, item)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ := stepResult.LastInsertId()
	decisionResult, err := conn.Exec(`insert into agent_match_decisions(
		task_id,step_id,selected_agent,reasons_json,rejections_json,created_by,status,created_at
	) values(?,?,?,'[]','[]','system','ready: assignment','now')`, otherTaskID, stepID, "api-agent")
	if err != nil {
		t.Fatal(err)
	}
	decisionID, _ := decisionResult.LastInsertId()
	if _, err := conn.Exec(`insert into task_assignments(
		task_id,step_id,agent_name,status,decision_id,created_at
	) values(?,?,?,'review',?,'now')`, otherTaskID, stepID, "api-agent", decisionID); err != nil {
		t.Fatal(err)
	}

	candidates, err := NewService(cfg, conn).loadCandidates(t.Context(), taskID, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		if candidate.Definition.Name == "api-agent" {
			if candidate.ActiveTasks != 1 {
				t.Fatalf("review assignment active count=%d", candidate.ActiveTasks)
			}
			return
		}
	}
	t.Fatal("api-agent candidate missing")
}

func assignmentFixture(t *testing.T) (config.Config, *sql.DB, int64) {
	t.Helper()
	cfg, _ := config.Load(t.TempDir())
	conn := testutil.OpenDB(t)
	res, err := conn.Exec(`insert into projects(slug,dir,status,remote_url,created_at) values('project-a','/tmp/project-a','created','','now')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := res.LastInsertId()
	res, err = conn.Exec(`insert into tasks(project_id,title,description,status,created_at) values(?,?,?,'planned','now')`, projectID, "delivery", "implement")
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()
	api := planning.WorkItem{Key: "api", Title: "API", Kind: "backend", RequiredCapabilities: []string{"go"}}
	web := planning.WorkItem{Key: "web", Title: "Web", Kind: "frontend", RequiredCapabilities: []string{"vue"}, DependsOn: []string{"api"}}
	for _, item := range []planning.WorkItem{api, web} {
		input, _ := json.Marshal(item)
		if _, err := conn.Exec(`insert into task_steps(task_id,agent_name,kind,status,input,created_at) values(?,'unassigned',?,'created',?,'now')`, taskID, item.Kind, string(input)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Exec(`insert into workflow_edges(task_id,from_step_id,to_step_id,label,created_at) values(?,1,2,'depends_on','now')`, taskID); err != nil {
		t.Fatal(err)
	}
	return cfg, conn, taskID
}

func writeAgent(t *testing.T, cfg config.Config, name, role string, capabilities []string, concurrency int) {
	t.Helper()
	dir := filepath.Join(cfg.AgentDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "role: " + role + "\nmax_concurrency: 1\ncapabilities:\n"
	for _, capability := range capabilities {
		content += "  - " + capability + "\n"
	}
	content += "project_access:\n  - project-a\n"
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func registerAgent(t *testing.T, conn *sql.DB, cfg config.Config, name, role, status string) {
	t.Helper()
	if _, err := conn.Exec(`insert into agent_registry(name,role,dir,status) values(?,?,?,?)`, name, role, filepath.Join(cfg.AgentDir, name), status); err != nil {
		t.Fatal(err)
	}
}

func assertTableCount(t *testing.T, conn *sql.DB, table string, want int) {
	t.Helper()
	var got int
	if err := conn.QueryRow(`select count(*) from ` + table).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s count=%d want=%d", table, got, want)
	}
}
