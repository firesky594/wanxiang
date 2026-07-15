package deliveries

import (
	"context"
	"database/sql"
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
}

func NewWorker(db *sql.DB, service *Service, interval time.Duration) *Worker {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &Worker{db: db, service: service, interval: interval, stop: make(chan struct{}), done: make(chan struct{})}
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
	return nil
}
