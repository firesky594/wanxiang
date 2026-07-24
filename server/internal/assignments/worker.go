package assignments

import (
	"context"
	"database/sql"
	"sync"
	"time"
)

const missingResourcesRetryInterval = 30 * time.Second

type TaskAssigner interface {
	AssignTask(context.Context, int64) (Result, error)
}

type Worker struct {
	db          *sql.DB
	assigner    TaskAssigner
	interval    time.Duration
	lifecycleMu sync.Mutex
	mu          sync.Mutex
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

// NewWorker 创建任务自动分配轮询器。
func NewWorker(db *sql.DB, assigner TaskAssigner, interval time.Duration) *Worker {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &Worker{db: db, assigner: assigner, interval: interval}
}

// Start 启动待分配任务轮询。
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

// Close 停止分配轮询并等待退出。
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
	if w.db == nil || w.assigner == nil {
		return
	}
	resourceRetryBefore := time.Now().UTC().Add(-missingResourcesRetryInterval).Format(time.RFC3339Nano)
	rows, err := w.db.QueryContext(ctx, `select t.id
		from tasks t
		where t.status='planned'
		or (
			t.status='blocked: missing_config'
			and (
				not exists (
					select 1
					from agent_match_decisions mapped
					join task_steps mapped_step on mapped_step.id=mapped.step_id
					where mapped.task_id=t.id
					and mapped_step.plan_version=(
						select coalesce(max(version),1) from task_plan_versions where task_id=t.id
					)
					and mapped.created_by='system'
					and mapped.status in ('blocked: missing_config','waiting: probe','blocked: missing_resources')
					and mapped.selected_agent is not null
					and mapped.selected_agent<>''
					and mapped.id=(
						select max(latest.id)
						from agent_match_decisions latest
						where latest.task_id=t.id
						and latest.step_id=mapped.step_id
						and latest.created_by='system'
					)
				)
				or exists (
					select 1
					from agent_match_decisions ready
					join task_steps ready_step on ready_step.id=ready.step_id
					join agent_registry ready_agent on ready_agent.name=ready.selected_agent
					where ready.task_id=t.id
					and ready_step.plan_version=(
						select coalesce(max(version),1) from task_plan_versions where task_id=t.id
					)
					and ready.created_by='system'
					and ready.status in ('blocked: missing_config','waiting: probe','blocked: missing_resources')
					and ready.selected_agent is not null
					and ready.selected_agent<>''
					and ready.id=(
						select max(latest.id)
						from agent_match_decisions latest
							where latest.task_id=t.id
							and latest.step_id=ready.step_id
							and latest.created_by='system'
						)
					and (
						(ready.status='blocked: missing_config' and ready_agent.status in ('configured','online'))
						or (ready.status='waiting: probe' and ready_agent.status='online')
					)
				)
			)
		)
		or (
			t.status='blocked: missing_resources'
			and exists (
				select 1
				from agent_match_decisions ready
				join task_steps ready_step on ready_step.id=ready.step_id
				join agent_registry ready_agent on ready_agent.name=ready.selected_agent
				where ready.task_id=t.id
				and ready_step.plan_version=(
					select coalesce(max(version),1) from task_plan_versions where task_id=t.id
				)
				and ready.created_by='system'
				and ready.status in ('blocked: missing_config','waiting: probe','blocked: missing_resources')
				and ready.status='blocked: missing_resources'
				and ready.selected_agent is not null
				and ready.selected_agent<>''
				and ready_agent.status='online'
				and ready.id=(
					select max(latest.id)
					from agent_match_decisions latest
							where latest.task_id=t.id
							and latest.step_id=ready.step_id
							and latest.created_by='system'
						)
				and julianday(ready.created_at)<=julianday(?)
			)
		)
		order by t.id
		limit 10`, resourceRetryBefore)
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
	if rows.Err() != nil {
		rows.Close()
		return
	}
	if rows.Close() != nil {
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		_, _ = w.assigner.AssignTask(ctx, id)
	}
}
