package workspaces

import (
	"context"
	"database/sql"
	"sync"
	"time"
)

type WorkspaceOrchestrator interface {
	ProvisionTask(context.Context, int64) (TaskWorkspace, error)
	ReconcileTask(context.Context, int64) (TaskWorkspace, error)
}
type Worker struct {
	db          *sql.DB
	service     WorkspaceOrchestrator
	interval    time.Duration
	lifecycleMu sync.Mutex
	mu          sync.Mutex
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

// NewWorker 创建工作区装配与校准轮询器。
func NewWorker(db *sql.DB, service WorkspaceOrchestrator, interval time.Duration) *Worker {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &Worker{db: db, service: service, interval: interval}
}

// Start 启动工作区装配与校准轮询。
func (w *Worker) Start() {
	w.lifecycleMu.Lock()
	defer w.lifecycleMu.Unlock()
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

// Close 停止工作区轮询并等待退出。
func (w *Worker) Close() {
	w.lifecycleMu.Lock()
	defer w.lifecycleMu.Unlock()
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
	if w.db == nil || w.service == nil {
		return
	}
	w.runStatus(ctx, "assigned", w.service.ProvisionTask)
	w.runStatus(ctx, "workspace_ready", w.service.ReconcileTask)
}
func (w *Worker) runStatus(ctx context.Context, status string, fn func(context.Context, int64) (TaskWorkspace, error)) {
	rows, err := w.db.QueryContext(ctx, `select id from tasks where status=? order by id limit 10`, status)
	if err != nil {
		return
	}
	ids := []int64{}
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
		_, _ = fn(ctx, id)
	}
}
