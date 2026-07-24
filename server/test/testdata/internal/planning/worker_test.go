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
