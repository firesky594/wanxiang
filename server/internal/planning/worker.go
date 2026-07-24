package planning

import (
	"context"
	"database/sql"
	"encoding/json"
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

	readinessObserved bool
	managerWasReady   bool
}

// NewWorker 创建任务规划轮询器。
func NewWorker(db *sql.DB, planner TaskPlanner, readiness ManagerReadiness, interval time.Duration) *Worker {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &Worker{db: db, planner: planner, readiness: readiness, interval: interval}
}

// Start 启动待规划任务轮询。
func (w *Worker) Start() {
	w.mu.Lock()
	if w.cancel != nil {
		w.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.readinessObserved = false
	w.managerWasReady = false
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

// Close 停止规划轮询并等待退出。
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
	if err := w.recoverStalePlanning(ctx, time.Now().UTC()); err != nil {
		return
	}
	ready, err := w.readiness.ManagerReady(ctx)
	if err != nil {
		return
	}
	w.mu.Lock()
	managerRecovered := ready && (!w.readinessObserved || !w.managerWasReady)
	w.readinessObserved = true
	w.managerWasReady = ready
	w.mu.Unlock()
	if !ready {
		return
	}
	if managerRecovered {
		if _, err := w.db.ExecContext(ctx, `update tasks
			set status='created',
				planning_attempts=0,
				planning_error_class='',
				planning_next_retry_at=null,
				planning_started_at=null
			where status='blocked: planning_error'
				and planning_error_class in (?, '')`, planningErrorConfiguration); err != nil {
			return
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := w.db.QueryContext(ctx, `select id from tasks
		where status='created'
			or (
				status='blocked: planning_error'
				and planning_error_class=?
				and planning_attempts<?
				and planning_next_retry_at is not null
				and planning_next_retry_at<=?
			)
		order by id
		limit 10`, planningErrorTransient, maxPlanningAttempts, now)
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

func (w *Worker) recoverStalePlanning(ctx context.Context, now time.Time) error {
	cutoff := now.Add(-planningClaimTimeout).Format(time.RFC3339Nano)
	rows, err := w.db.QueryContext(ctx, `select id,planning_attempts from tasks
		where status='planning'
			and (planning_started_at is null or planning_started_at<=?)
		order by id
		limit 10`, cutoff)
	if err != nil {
		return err
	}
	type stalePlanning struct {
		id      int64
		attempt int
	}
	items := make([]stalePlanning, 0, 10)
	for rows.Next() {
		var item stalePlanning
		if err := rows.Scan(&item.id, &item.attempt); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := w.recoverStaleTask(ctx, now, cutoff, item.id, item.attempt); err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) recoverStaleTask(ctx context.Context, now time.Time, cutoff string, taskID int64, storedAttempt int) error {
	attempt := storedAttempt
	if attempt < 1 {
		attempt = 1
	}
	if attempt > maxPlanningAttempts {
		attempt = maxPlanningAttempts
	}
	retryValue := planningRetryTime(now, attempt)
	var retryAt any
	summary := "planning interrupted: stale claim recovered; retry scheduled"
	if retryValue == "" {
		summary = "planning interrupted: retry limit reached"
	} else {
		retryAt = retryValue
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `update tasks
		set status='blocked: planning_error',
			manager_summary=?,
			planning_attempts=?,
			planning_error_class=?,
			planning_next_retry_at=?,
			planning_started_at=null
		where id=?
			and status='planning'
			and planning_attempts=?
			and (planning_started_at is null or planning_started_at<=?)`,
		summary, attempt, planningErrorTransient, retryAt, taskID, storedAttempt, cutoff)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return nil
	}
	payload, _ := json.Marshal(map[string]any{
		"task_id": taskID, "attempt": attempt, "reason": "stale_claim",
		"next_retry_at": retryValue,
	})
	if _, err := tx.ExecContext(ctx, `insert into runtime_events(task_id,event_type,actor,payload_json,created_at)
		values(?,'task.planning.recovered','manager',?,?)`,
		taskID, string(payload), now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}
