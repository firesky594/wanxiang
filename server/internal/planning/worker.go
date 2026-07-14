package planning

import (
	"context"
	"database/sql"
	"sync"
	"time"
)

type TaskPlanner interface {
	PlanTask(context.Context, int64) (Plan, error)
}
type ManagerReadiness interface {
	ManagerReady(context.Context) (bool, error)
}

type Worker struct {
	db        *sql.DB
	planner   TaskPlanner
	readiness ManagerReadiness
	interval  time.Duration
	mu        sync.Mutex
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

func NewWorker(db *sql.DB, planner TaskPlanner, readiness ManagerReadiness, interval time.Duration) *Worker {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &Worker{db: db, planner: planner, readiness: readiness, interval: interval}
}

func (w *Worker) Start() {
	w.mu.Lock()
	if w.cancel != nil {
		w.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.wg.Add(1)
	w.mu.Unlock()
	go func() {
		defer w.wg.Done()
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		w.runOnce(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.runOnce(ctx)
			}
		}
	}()
}

func (w *Worker) Close() {
	w.mu.Lock()
	cancel := w.cancel
	w.cancel = nil
	w.mu.Unlock()
	if cancel != nil {
		cancel()
		w.wg.Wait()
	}
}

func (w *Worker) runOnce(ctx context.Context) {
	if w.db == nil || w.planner == nil || w.readiness == nil {
		return
	}
	ready, err := w.readiness.ManagerReady(ctx)
	if err != nil || !ready {
		return
	}
	rows, err := w.db.QueryContext(ctx, `select id from tasks where status='created' order by id limit 10`)
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		_, _ = w.planner.PlanTask(ctx, id)
	}
}
