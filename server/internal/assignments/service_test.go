package assignments

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wanxiang-agent/server/internal/config"
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
