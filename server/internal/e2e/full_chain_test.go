package e2e

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"wanxiang-agent/server/internal/assignments"
	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/deliveries"
	"wanxiang-agent/server/internal/events"
	"wanxiang-agent/server/internal/leases"
	"wanxiang-agent/server/internal/mr"
	"wanxiang-agent/server/internal/planning"
	"wanxiang-agent/server/internal/providers"
	"wanxiang-agent/server/internal/testutil"
	"wanxiang-agent/server/internal/workspaces"
)

type chainPlanner struct{ content string }

func (p chainPlanner) ChatAgent(context.Context, string, []providers.Message, int) (providers.Result, error) {
	return providers.Result{Content: p.content}, nil
}

func TestNaturalLanguageTaskTraversesCompleteDeliveryTimeline(t *testing.T) {
	root := t.TempDir()
	cfg, _ := config.Load(root)
	db := testutil.OpenDB(t)
	managerDir := filepath.Join(cfg.AgentDir, "manager")
	_ = os.MkdirAll(managerDir, 0o755)
	_ = os.WriteFile(filepath.Join(managerDir, "system_prompt.md"), []byte("安全规划并输出 JSON"), 0o644)
	repo := filepath.Join(cfg.ProjectDir, "chain-project")
	chainGit(t, root, "init", "--initial-branch=main", repo)
	chainGit(t, repo, "config", "user.email", "test@example.com")
	chainGit(t, repo, "config", "user.name", "M10")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644)
	chainGit(t, repo, "add", "README.md")
	chainGit(t, repo, "commit", "-m", "初始化")
	base := strings.TrimSpace(chainGit(t, repo, "rev-parse", "HEAD"))

	p, _ := db.Exec("insert into projects(slug,dir,status,main_commit,remote_url,created_at) values('chain-project',?,'active',?,'','now')", repo, base)
	projectID, _ := p.LastInsertId()
	task, _ := db.Exec("insert into tasks(project_id,title,description,status,created_at) values(?,'实现健康接口','请实现健康接口并完成测试','created','now')", projectID)
	taskID, _ := task.LastInsertId()
	_, _ = db.Exec("insert into runtime_events(task_id,event_type,actor,payload_json,created_at) values(?,'task.created','admin','{}','2026-01-01T00:00:00Z')", taskID)

	planJSON := "{\"summary\":\"实现健康接口\",\"work_items\":[{\"key\":\"health\",\"title\":\"健康接口\",\"description\":\"实现并测试\",\"kind\":\"backend\",\"required_capabilities\":[\"go\"],\"acceptance_criteria\":[\"go test ./... 通过\"],\"depends_on\":[]}]}"
	if _, err := planning.NewService(cfg, db, chainPlanner{planJSON}).PlanTask(t.Context(), taskID); err != nil {
		t.Fatal(err)
	}
	writeChainAgent(t, cfg, db)
	assigned, err := assignments.NewService(cfg, db).AssignTask(t.Context(), taskID)
	if err != nil || len(assigned.Assignments) != 1 {
		t.Fatalf("assignment=%+v err=%v", assigned, err)
	}
	stepID := assigned.Assignments[0].StepID
	workspaceService := workspaces.NewService(cfg, db, events.NewBus(db))
	workspace, err := workspaceService.ProvisionTask(t.Context(), taskID)
	if err != nil || len(workspace.Items) != 1 {
		t.Fatalf("workspace=%+v err=%v", workspace, err)
	}
	item := workspace.Items[0]
	leaseService := leases.NewService(db, leases.SystemClock{}, workspaceService)
	lease, err := leaseService.Acquire(t.Context(), taskID, stepID, "worker")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(item.WorktreePath, "health.go"), []byte("package health\n\nfunc OK() bool { return true }\n"), 0o644)
	chainGit(t, item.WorktreePath, "add", "health.go")
	chainGit(t, item.WorktreePath, "commit", "-m", "功能：实现健康检查")
	head := strings.TrimSpace(chainGit(t, item.WorktreePath, "rev-parse", "HEAD"))
	checkpoint, err := leaseService.CreateCheckpoint(t.Context(), lease.LeaseRef, leases.CheckpointInput{IdempotencyKey: "chain", GitCommit: head, BranchName: item.BranchName, Clean: true, Files: []string{"health.go"}, Tests: []leases.CheckpointTest{{Command: "go test ./...", Result: "passed"}}, Summary: leases.RecoverySummary{Completed: []string{"健康接口"}, NextAction: "提交完成报告", FilesChanged: []string{"health.go"}}})
	if err != nil || checkpoint.ID == 0 {
		t.Fatalf("checkpoint=%+v err=%v", checkpoint, err)
	}

	mrs := mr.NewService(cfg, db, events.NewBus(db), nil)
	report := mr.CompletionReportInput{AgentName: "worker", Role: "backend", ProjectID: projectID, TaskID: taskID, StepID: stepID, LeaseID: lease.LeaseID, LeaseVersion: lease.LeaseVersion, SourceBranch: item.BranchName, CheckpointCommit: head, HeadCommit: head, Completed: []string{"健康接口"}, KeyFiles: []string{"health.go"}, Tests: []mr.TestEvidence{{Command: "go test ./...", Status: "passed"}}}
	detail, err := mrs.SubmitReport(t.Context(), mr.Principal{Name: "worker", Role: "backend"}, report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = mrs.Review(t.Context(), mr.Principal{Name: "worker", Role: "backend"}, detail.MergeRequest.ID, mr.ReviewInput{AgentName: "worker", Role: "backend", Status: mr.MRApproved, Body: "验收通过"}); err != nil {
		t.Fatal(err)
	}
	merged, err := mrs.Merge(t.Context(), mr.Principal{Name: "worker", Role: "backend"}, detail.MergeRequest.ID, mr.MergeInput{AgentName: "worker", Role: "backend"})
	if err != nil || merged.MergeCommit == "" {
		t.Fatalf("merge=%+v err=%v", merged, err)
	}
	parents := strings.Fields(strings.TrimSpace(chainGit(t, repo, "show", "-s", "--format=%P", merged.MergeCommit)))
	if len(parents) != 2 || merged.MergeCommit == head {
		t.Fatalf("merge is not no-ff: commit=%s parents=%v source=%s", merged.MergeCommit, parents, head)
	}
	var notificationID int64
	_ = db.QueryRow("select id from manager_notifications where task_id=? order by id desc limit 1", taskID).Scan(&notificationID)
	delivery := deliveries.NewService(db, events.NewBus(db))
	snapshot, err := delivery.BuildSnapshot(t.Context(), notificationID)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := delivery.Decide(t.Context(), snapshot.ID, deliveries.DecisionInput{Decision: "accepted", Comment: "本机验收通过", IdempotencyKey: "chain-accept", CreatedBy: "admin"})
	if err != nil || decision.TaskStatus != "completed" {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}

	rows, _ := db.Query("select event_type from runtime_events where task_id=? order by id", taskID)
	defer rows.Close()
	var timeline []string
	for rows.Next() {
		var event string
		_ = rows.Scan(&event)
		timeline = append(timeline, event)
	}
	last := -1
	for _, required := range []string{"task.created", "task.planning.completed", "task.assignment.completed", "task.workspace.ready", "task.step.lease.acquired", "task.step.checkpointed", "mr.created", "mr.reviewed", "mr.merged", "delivery.snapshot.created", "delivery.decision.created"} {
		index := chainEventIndex(timeline, required)
		if index <= last {
			t.Fatalf("timeline missing or out of order at %s: %v", required, timeline)
		}
		last = index
	}
}

func chainEventIndex(events []string, target string) int {
	for i, event := range events {
		if event == target {
			return i
		}
	}
	return -1
}

func writeChainAgent(t *testing.T, cfg config.Config, db *sql.DB) {
	t.Helper()
	dir := filepath.Join(cfg.AgentDir, "worker")
	_ = os.MkdirAll(dir, 0o755)
	definition := "role: backend\nmax_concurrency: 1\ncapabilities:\n  - go\nproject_access:\n  - chain-project\n"
	_ = os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(definition), 0o644)
	if _, err := db.Exec("insert into agent_registry(name,role,dir,status) values('worker','backend',?,'online')", dir); err != nil {
		t.Fatal(err)
	}
}

func chainGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return string(out)
}
