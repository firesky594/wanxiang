package assignments

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"wanxiang-agent/server/internal/testutil"
)

type recordingAssigner struct{ ids []int64 }

func (r *recordingAssigner) AssignTask(_ context.Context, id int64) (Result, error) {
	r.ids = append(r.ids, id)
	return Result{TaskID: id, Status: "assigned"}, nil
}

func TestWorkerConsumesPlannedAndRecoverableBlockedTasks(t *testing.T) {
	conn := testutil.OpenDB(t)
	project := insertWorkerProject(t, conn)
	planned := insertWorkerTask(t, conn, project, "planned")
	blocked := insertWorkerTask(t, conn, project, "blocked: missing_config")
	insertWorkerTask(t, conn, project, "created")
	if _, err := conn.Exec(`insert into agent_registry(name,role,dir,status) values('recovered','worker','/tmp/recovered','online')`); err != nil {
		t.Fatal(err)
	}
	recorder := &recordingAssigner{}
	worker := NewWorker(conn, recorder, time.Hour)
	worker.runOnce(t.Context())
	if len(recorder.ids) != 2 || recorder.ids[0] != planned || recorder.ids[1] != blocked {
		t.Fatalf("ids=%v", recorder.ids)
	}
}

func insertWorkerProject(t *testing.T, conn *sql.DB) int64 {
	t.Helper()
	result, err := conn.Exec(`insert into projects(slug,dir,status,remote_url,created_at) values('p','/tmp/p','created','','now')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}
func insertWorkerTask(t *testing.T, conn *sql.DB, project int64, status string) int64 {
	t.Helper()
	result, err := conn.Exec(`insert into tasks(project_id,title,description,status,created_at) values(?,'t','d',?,'now')`, project, status)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}
