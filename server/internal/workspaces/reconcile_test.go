package workspaces

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReconcileDetectsDriftAndRepairsExplicitDirection(t *testing.T) {
	cfg, conn, taskID, projectDir := workspaceFixture(t)
	svc := NewService(cfg, conn, nil)
	workspace, err := svc.ProvisionTask(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	item := workspace.Items[0]
	snapshot := filepath.Join(projectDir, ".wanxiang", "assignments", itoa(taskID)+"-"+itoa(item.StepID)+".yaml")
	original, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(snapshot, append(original, []byte("# drift\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	drifted, err := svc.ReconcileTask(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if drifted.Status != "drifted" {
		t.Fatalf("workspace=%+v", drifted)
	}
	changed, _ := os.ReadFile(snapshot)
	if string(changed) == string(original) {
		t.Fatal("reconcile silently overwrote snapshot")
	}
	repaired, err := svc.RepairTask(t.Context(), taskID, RepairFromDatabase, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Status != "ready" {
		t.Fatalf("repaired=%+v", repaired)
	}
	if _, err = conn.Exec(`update project_workspaces set agent_name='wrong' where task_id=? and step_id=?`, taskID, item.StepID); err != nil {
		t.Fatal(err)
	}
	if drifted, err = svc.ReconcileTask(t.Context(), taskID); err != nil || drifted.Status != "drifted" {
		t.Fatalf("db drift=%+v err=%v", drifted, err)
	}
	repaired, err = svc.RepairTask(t.Context(), taskID, RepairFromGitSnapshot, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Items[0].AgentName != "api" {
		t.Fatalf("git repair=%+v", repaired.Items[0])
	}
}

func TestCleanupRequiresTerminalTaskOrExplicitConfirmation(t *testing.T) {
	cfg, conn, taskID, _ := workspaceFixture(t)
	svc := NewService(cfg, conn, nil)
	workspace, err := svc.ProvisionTask(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.RequestCleanup(t.Context(), taskID, false, "admin"); err == nil {
		t.Fatal("expected confirmation error")
	}
	pending, err := svc.RequestCleanup(t.Context(), taskID, true, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != "cleanup_pending" {
		t.Fatalf("pending=%+v", pending)
	}
	cleaned, err := svc.ConfirmCleanup(t.Context(), taskID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if cleaned.Status != "cleaned" {
		t.Fatalf("cleaned=%+v", cleaned)
	}
	for _, item := range workspace.Items {
		if _, statErr := os.Stat(item.WorktreePath); !os.IsNotExist(statErr) {
			t.Fatalf("worktree still exists: %s err=%v", item.WorktreePath, statErr)
		}
	}
}
