package workspaces

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/gitx"
	"wanxiang-agent/server/internal/planning"
	"wanxiang-agent/server/internal/testutil"
)

func TestProvisionTaskCreatesIndependentWorktreesIdempotently(t *testing.T) {
	cfg, conn, taskID, projectDir := workspaceFixture(t)
	svc := NewService(cfg, conn, nil)
	first, err := svc.ProvisionTask(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "ready" || len(first.Items) != 2 {
		t.Fatalf("workspace=%+v", first)
	}
	if first.Items[0].BranchName == first.Items[1].BranchName || first.Items[0].WorktreePath == first.Items[1].WorktreePath {
		t.Fatalf("workspaces not isolated: %+v", first.Items)
	}
	if first.Items[0].BaseCommit != first.Items[1].BaseCommit || first.Items[0].ProvisionCommit != first.Items[1].ProvisionCommit {
		t.Fatalf("commits differ: %+v", first.Items)
	}
	for _, item := range first.Items {
		branch, runErr := gitx.Run(t.Context(), item.WorktreePath, "branch", "--show-current")
		if runErr != nil || strings.TrimSpace(branch) != item.BranchName {
			t.Fatalf("branch=%q err=%v item=%+v", branch, runErr, item)
		}
		head, _ := gitx.Run(t.Context(), item.WorktreePath, "rev-parse", "HEAD")
		if strings.TrimSpace(head) != item.ProvisionCommit {
			t.Fatalf("head=%q item=%+v", head, item)
		}
	}
	countBefore := commitCount(t, projectDir)
	second, err := svc.ProvisionTask(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if commitCount(t, projectDir) != countBefore || len(second.Items) != 2 {
		t.Fatalf("repeat changed repo: before=%d after=%d", countBefore, commitCount(t, projectDir))
	}
	var status string
	if err := conn.QueryRow(`select status from tasks where id=?`, taskID).Scan(&status); err != nil || status != "workspace_ready" {
		t.Fatalf("status=%q err=%v", status, err)
	}
}

func TestProvisionTaskDoesNotReuseUnknownBranchOrDirectory(t *testing.T) {
	for _, scenario := range []string{"branch", "directory"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, conn, taskID, projectDir := workspaceFixture(t)
			var stepID int64
			var input string
			if err := conn.QueryRow(`select id,input from task_steps where task_id=? order by id limit 1`, taskID).Scan(&stepID, &input); err != nil {
				t.Fatal(err)
			}
			var item planning.WorkItem
			_ = json.Unmarshal([]byte(input), &item)
			branch := "agent/api/" + itoa(taskID) + "-" + itoa(stepID) + "-" + item.Key
			path := filepath.Join(cfg.DataDir, "worktrees", "task-"+itoa(taskID), "step-"+itoa(stepID)+"-api")
			if scenario == "branch" {
				if out, err := gitx.Run(t.Context(), projectDir, "branch", branch, "HEAD"); err != nil {
					t.Fatalf("branch: %v %s", err, out)
				}
			} else {
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "owner.txt"), []byte("unknown"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, err := NewService(cfg, conn, nil).ProvisionTask(t.Context(), taskID)
			if err == nil {
				t.Fatal("expected safety error")
			}
			if scenario == "directory" {
				content, readErr := os.ReadFile(filepath.Join(path, "owner.txt"))
				if readErr != nil || string(content) != "unknown" {
					t.Fatalf("unknown directory changed content=%q err=%v", content, readErr)
				}
			}
			var failed int
			_ = conn.QueryRow(`select count(*) from project_workspaces where task_id=? and status='failed'`, taskID).Scan(&failed)
			if failed == 0 {
				t.Fatal("failure state not persisted")
			}
			if scenario == "directory" {
				before := commitCount(t, projectDir)
				if err := os.RemoveAll(path); err != nil {
					t.Fatal(err)
				}
				recovered, retryErr := NewService(cfg, conn, nil).ProvisionTask(t.Context(), taskID)
				if retryErr != nil {
					t.Fatal(retryErr)
				}
				if recovered.Status != "ready" || commitCount(t, projectDir) != before {
					t.Fatalf("recovery=%+v commits before=%d after=%d", recovered, before, commitCount(t, projectDir))
				}
			}
		})
	}
}

func TestAuthorizeAgentEnforcesAssignmentIdentityAndScope(t *testing.T) {
	cfg, conn, taskID, _ := workspaceFixture(t)
	svc := NewService(cfg, conn, nil)
	workspace, err := svc.ProvisionTask(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	stepID := workspace.Items[0].StepID
	if err = svc.AuthorizeAgent(t.Context(), "api", taskID, stepID, "src/api.go"); err != nil {
		t.Fatal(err)
	}
	for _, probe := range []struct {
		agent, path string
		task, step  int64
	}{{"web", "src/api.go", taskID, stepID}, {"api", "../secret", taskID, stepID}, {"api", "/tmp/secret", taskID, stepID}, {"api", "src/api.go", taskID + 1, stepID}, {"api", "src/api.go", taskID, stepID + 99}} {
		if err = svc.AuthorizeAgent(t.Context(), probe.agent, probe.task, probe.step, probe.path); err == nil {
			t.Fatalf("expected rejection: %+v", probe)
		}
	}
}

func workspaceFixture(t *testing.T) (config.Config, *sql.DB, int64, string) {
	t.Helper()
	cfg, _ := config.Load(t.TempDir())
	conn := testutil.OpenDB(t)
	projectDir := filepath.Join(cfg.ProjectDir, "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, projectDir, "init", "-b", "main")
	mustGit(t, projectDir, "config", "user.name", "Test")
	mustGit(t, projectDir, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(projectDir, "README.md"), []byte("demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, projectDir, "add", ".")
	mustGit(t, projectDir, "commit", "-m", "初始化")
	base := strings.TrimSpace(mustGit(t, projectDir, "rev-parse", "HEAD"))
	res, err := conn.Exec(`insert into projects(slug,dir,status,main_commit,remote_url,created_at) values('demo',?,'created',?,'','now')`, projectDir, base)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := res.LastInsertId()
	res, err = conn.Exec(`insert into tasks(project_id,title,description,status,created_at) values(?,'delivery','work','assigned','now')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := res.LastInsertId()
	for index, item := range []planning.WorkItem{{Key: "api", Title: "API", Kind: "backend"}, {Key: "web", Title: "Web", Kind: "frontend", DependsOn: []string{"api"}}} {
		input, _ := json.Marshal(item)
		stepRes, insertErr := conn.Exec(`insert into task_steps(task_id,agent_name,kind,status,input,created_at) values(?,?,?,'assigned',?,'now')`, taskID, []string{"api", "web"}[index], item.Kind, string(input))
		if insertErr != nil {
			t.Fatal(insertErr)
		}
		stepID, _ := stepRes.LastInsertId()
		decision, insertErr := conn.Exec(`insert into agent_match_decisions(task_id,step_id,selected_agent,reasons_json,rejections_json,created_by,status,created_at) values(?,?,?,'[]','[]','system','selected','now')`, taskID, stepID, []string{"api", "web"}[index])
		if insertErr != nil {
			t.Fatal(insertErr)
		}
		decisionID, _ := decision.LastInsertId()
		if _, insertErr = conn.Exec(`insert into task_assignments(task_id,step_id,agent_name,reports_to,status,decision_id,created_at) values(?,?,?,?, 'assigned',?,'now')`, taskID, stepID, []string{"api", "web"}[index], map[bool]any{true: nil, false: "api"}[index == 0], decisionID); insertErr != nil {
			t.Fatal(insertErr)
		}
	}
	if _, err = conn.Exec(`insert into team_decisions(task_id,project_lead,requires_lead,reason,created_at) values(?,'api',1,'cross_agent_dependency','now')`, taskID); err != nil {
		t.Fatal(err)
	}
	return cfg, conn, taskID, projectDir
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitx.Run(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return out
}
func commitCount(t *testing.T, dir string) int {
	t.Helper()
	value, err := strconv.Atoi(strings.TrimSpace(mustGit(t, dir, "rev-list", "--count", "HEAD")))
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func itoa(value int64) string { return strconv.FormatInt(value, 10) }
