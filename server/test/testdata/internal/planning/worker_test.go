package planning

import (
	"context"
	"sync"
	"testing"
	"time"

	"wanxiang-agent/server/internal/testutil"
)

type fakeReadiness struct{ ready bool }

func (f fakeReadiness) ManagerReady(context.Context) (bool, error) { return f.ready, nil }

type recordingTaskPlanner struct {
	mu  sync.Mutex
	ids []int64
}

func (p *recordingTaskPlanner) PlanTask(_ context.Context, id int64) (Plan, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ids = append(p.ids, id)
	return Plan{}, nil
}
func (p *recordingTaskPlanner) count() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.ids) }

type conditionTaskPlanner struct {
	recordingTaskPlanner
	condition string
}

func (p *conditionTaskPlanner) planningCondition() string { return p.condition }

func TestWorkerConsumesCreatedTasksWhenManagerReady(t *testing.T) {
	conn := testutil.OpenDB(t)
	_, _ = conn.Exec(`insert into projects(id,slug,dir,status,remote_url,created_at) values(1,'p','/tmp/p','created','','now')`)
	_, _ = conn.Exec(`insert into tasks(id,project_id,title,description,status,created_at) values(7,1,'t','d','created','now')`)
	planner := &recordingTaskPlanner{}
	worker := NewWorker(conn, planner, fakeReadiness{ready: true}, 10*time.Millisecond)
	worker.Start()
	t.Cleanup(worker.Close)
	deadline := time.Now().Add(time.Second)
	for planner.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if planner.count() < 1 {
		t.Fatalf("calls=%d", planner.count())
	}
}

func TestWorkerWaitsForManagerReadinessAndStops(t *testing.T) {
	conn := testutil.OpenDB(t)
	planner := &recordingTaskPlanner{}
	worker := NewWorker(conn, planner, fakeReadiness{ready: false}, 5*time.Millisecond)
	worker.Start()
	time.Sleep(20 * time.Millisecond)
	worker.Close()
	if planner.count() != 0 {
		t.Fatalf("calls=%d", planner.count())
	}
}

func TestWorkerRetriesPermanentPlanningBlockOnlyAfterConditionChange(t *testing.T) {
	conn := testutil.OpenDB(t)
	_, _ = conn.Exec(`insert into projects(id,slug,dir,status,remote_url,created_at) values(1,'p','/tmp/p','created','','now')`)
	_, _ = conn.Exec(`insert into tasks(
		id,project_id,title,description,status,planning_attempts,planning_error_class,
		planning_condition_hash,created_at
	) values(7,1,'t','d','blocked: planning_error',4,?,'same','now')`, planningErrorConfiguration)
	planner := &conditionTaskPlanner{condition: "same"}
	worker := NewWorker(conn, planner, fakeReadiness{ready: true}, time.Hour)

	worker.runOnce(t.Context())
	if planner.count() != 0 {
		t.Fatalf("first readiness observation retried a permanent block: %d", planner.count())
	}
	planner.condition = "changed"
	worker.runOnce(t.Context())
	if planner.count() != 1 {
		t.Fatalf("condition change retry count=%d", planner.count())
	}
	var events int
	if err := conn.QueryRow(`select count(*) from runtime_events where task_id=7`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("recovery reset emitted synthetic events: %d", events)
	}
}
