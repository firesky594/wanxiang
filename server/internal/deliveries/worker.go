package deliveries

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"time"
)

type Worker struct {
	db       *sql.DB
	service  *Service
	interval time.Duration
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
	rework   func(context.Context, int64, int64, string) error
}

func NewWorker(db *sql.DB, service *Service, interval time.Duration, rework ...func(context.Context, int64, int64, string) error) *Worker {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	w := &Worker{db: db, service: service, interval: interval, stop: make(chan struct{}), done: make(chan struct{})}
	if len(rework) > 0 {
		w.rework = rework[0]
	}
	return w
}
func (w *Worker) Start() {
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			_ = w.Scan(context.Background())
			select {
			case <-ticker.C:
			case <-w.stop:
				return
			}
		}
	}()
}
func (w *Worker) Close() { w.once.Do(func() { close(w.stop); <-w.done }) }
func (w *Worker) Scan(ctx context.Context) error {
	rows, err := w.db.QueryContext(ctx, `select id from manager_notifications where status='pending' and (next_retry_at is null or next_retry_at<=?) order by id limit 20`, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if _, err = w.service.BuildSnapshot(ctx, id); err != nil {
			retry := time.Now().UTC().Add(5 * time.Second).Format(time.RFC3339Nano)
			_, _ = w.db.ExecContext(ctx, `update manager_notifications set last_error=?,next_retry_at=? where id=? and status='pending'`, redactError(err), retry, id)
		}
	}
	if w.rework != nil {
		rounds, err := w.db.QueryContext(ctx, `select id,task_id,plan_version,reason from rework_rounds where status in ('planning','blocked: missing_config') order by id limit 10`)
		if err == nil {
			type item struct {
				id, task, version int64
				reason            string
			}
			var items []item
			for rounds.Next() {
				var x item
				if rounds.Scan(&x.id, &x.task, &x.version, &x.reason) == nil {
					items = append(items, x)
				}
			}
			rounds.Close()
			for _, x := range items {
				_, _ = w.db.ExecContext(ctx, `update rework_rounds set status='planning',last_error='' where id=?`, x.id)
				if err := w.rework(ctx, x.task, x.version, x.reason); err != nil {
					status := "blocked"
					if strings.Contains(err.Error(), "missing_config") {
						status = "blocked: missing_config"
					}
					_, _ = w.db.ExecContext(ctx, `update rework_rounds set status=?,last_error=? where id=?`, status, redactError(err), x.id)
				} else {
					now := time.Now().UTC().Format(time.RFC3339Nano)
					_, _ = w.db.ExecContext(ctx, `update rework_rounds set status='planned',completed_at=?,last_error='' where id=?`, now, x.id)
				}
			}
		}
	}
	return nil
}
