package workspaces

import (
	"context"
	"testing"
	"time"
	"wanxiang-agent/server/internal/testutil"
)

type recordingWorkspaceOrchestrator struct{ provisioned, reconciled []int64 }

func (r *recordingWorkspaceOrchestrator) ProvisionTask(_ context.Context, id int64) (TaskWorkspace, error) {
	r.provisioned = append(r.provisioned, id)
	return TaskWorkspace{}, nil
}
func (r *recordingWorkspaceOrchestrator) ReconcileTask(_ context.Context, id int64) (TaskWorkspace, error) {
	r.reconciled = append(r.reconciled, id)
	return TaskWorkspace{}, nil
}
func TestWorkerProvisionsAssignedAndReconcilesReadyTasks(t *testing.T) {
	db := testutil.OpenDB(t)
	p, _ := db.Exec(`insert into projects(slug,dir,status,remote_url,created_at) values('p','/tmp/p','created','','now')`)
	project, _ := p.LastInsertId()
	a, _ := db.Exec(`insert into tasks(project_id,title,description,status,created_at) values(?,'a','d','assigned','now')`, project)
	assigned, _ := a.LastInsertId()
	r, _ := db.Exec(`insert into tasks(project_id,title,description,status,created_at) values(?,'r','d','workspace_ready','now')`, project)
	ready, _ := r.LastInsertId()
	recorder := &recordingWorkspaceOrchestrator{}
	NewWorker(db, recorder, time.Hour).runOnce(t.Context())
	if len(recorder.provisioned) != 1 || recorder.provisioned[0] != assigned || len(recorder.reconciled) != 1 || recorder.reconciled[0] != ready {
		t.Fatalf("provision=%v reconcile=%v", recorder.provisioned, recorder.reconciled)
	}
}
