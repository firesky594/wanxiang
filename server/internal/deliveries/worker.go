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
	now := time.Now().UTC()
	staleNotification := now.Add(-5 * time.Minute).Format(time.RFC3339Nano)
	rows, err := w.db.QueryContext(ctx, `select id from manager_notifications where (status='pending' and (next_retry_at is null or next_retry_at<=?)) or (status='processing' and processing_started_at<?) order by id limit 20`, now.Format(time.RFC3339Nano), staleNotification)
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
		claim, claimErr := w.db.ExecContext(ctx, `update manager_notifications set status='processing',processing_started_at=? where id=? and (status='pending' or (status='processing' and processing_started_at<?))`, now.Format(time.RFC3339Nano), id, staleNotification)
		if claimErr != nil {
			continue
		}
		if changed, _ := claim.RowsAffected(); changed != 1 {
			continue
		}
		if _, err = w.service.BuildSnapshot(ctx, id); err != nil {
			retry := time.Now().UTC().Add(5 * time.Second).Format(time.RFC3339Nano)
			_, _ = w.db.ExecContext(ctx, `update manager_notifications set status='pending',processing_started_at=null,last_error=?,next_retry_at=? where id=? and status='processing'`, redactError(err), retry, id)
		}
	}
	if w.rework != nil {
		now := time.Now().UTC()
		stale := now.Add(-5 * time.Minute).Format(time.RFC3339Nano)
		rounds, err := w.db.QueryContext(ctx, `select id,task_id,plan_version,reason from rework_rounds where (status in ('planning','blocked: missing_config') and (next_retry_at is null or next_retry_at<=?)) or (status='processing' and processing_started_at<?) order by id limit 10`, now.Format(time.RFC3339Nano), stale)
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
				var taskStatus, versionStatus string
				_ = w.db.QueryRowContext(ctx, `select status from tasks where id=?`, x.task).Scan(&taskStatus)
				_ = w.db.QueryRowContext(ctx, `select status from task_plan_versions where task_id=? and version=?`, x.task, x.version).Scan(&versionStatus)
				if taskStatus == "planned" && versionStatus == "planned" {
					_, _ = w.db.ExecContext(ctx, `update rework_rounds set status='planned',completed_at=?,last_error='',processing_started_at=null where id=?`, now.Format(time.RFC3339Nano), x.id)
					continue
				}
				claim, claimErr := w.db.ExecContext(ctx, `update rework_rounds set status='processing',processing_started_at=?,last_error='' where id=? and (status in ('planning','blocked: missing_config') or (status='processing' and processing_started_at<?))`, now.Format(time.RFC3339Nano), x.id, stale)
				if claimErr != nil {
					continue
				}
				if changed, _ := claim.RowsAffected(); changed != 1 {
					continue
				}
				if err := w.rework(ctx, x.task, x.version, x.reason); err != nil {
					status := "planning"
					lower := strings.ToLower(err.Error())
					if strings.Contains(lower, "missing_config") || strings.Contains(lower, "api_key") || strings.Contains(lower, "api key") || strings.Contains(lower, "provider_type") || strings.Contains(lower, "model are required") {
						status = "blocked: missing_config"
					}
					retry := time.Now().UTC().Add(5 * time.Second).Format(time.RFC3339Nano)
					_, _ = w.db.ExecContext(ctx, `update rework_rounds set status=?,last_error=?,next_retry_at=?,processing_started_at=null where id=?`, status, redactError(err), retry, x.id)
				} else {
					finished := time.Now().UTC().Format(time.RFC3339Nano)
					_, _ = w.db.ExecContext(ctx, `update rework_rounds set status='planned',completed_at=?,last_error='',processing_started_at=null where id=?`, finished, x.id)
				}
			}
		}
	}
	return nil
}
