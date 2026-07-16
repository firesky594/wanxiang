package pipelines

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

type Worker struct {
	db         *sql.DB
	runner     Runner
	interval   time.Duration
	projectDir func(int64) (string, error)
	stop       chan struct{}
	done       chan struct{}
	once       sync.Once
}

func NewWorker(db *sql.DB, r Runner, interval time.Duration, projectDir func(int64) (string, error)) *Worker {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &Worker{db: db, runner: r, interval: interval, projectDir: projectDir, stop: make(chan struct{}), done: make(chan struct{})}
}
func (w *Worker) Start() {
	go func() {
		defer close(w.done)
		t := time.NewTicker(w.interval)
		defer t.Stop()
		for {
			_ = w.Scan(context.Background())
			select {
			case <-t.C:
			case <-w.stop:
				return
			}
		}
	}()
}
func (w *Worker) Close() { w.once.Do(func() { close(w.stop); <-w.done }) }
func (w *Worker) Scan(ctx context.Context) error {
	rows, e := w.db.QueryContext(ctx, `select ps.id,ps.run_id,pr.project_id,ps.step_key,ps.kind,ps.command,ps.args_json,ps.timeout_seconds,ps.max_attempts,ps.reversible,ps.attempt from pipeline_steps ps join pipeline_runs pr on pr.id=ps.run_id where ps.status='pending' and (ps.next_retry_at is null or ps.next_retry_at<=?) and not exists(select 1 from pipeline_steps prior where prior.run_id=ps.run_id and prior.id<ps.id and prior.status!='passed') order by ps.id limit 10`, time.Now().UTC().Format(time.RFC3339Nano))
	if e != nil {
		return e
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id, new(int64), new(int64), new(string), new(string), new(string), new(string), new(int), new(int), new(int), new(int))
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if e = w.run(ctx, id); e != nil {
			return e
		}
	}
	return nil
}
func (w *Worker) run(ctx context.Context, id int64) error {
	var s Step
	var project int64
	var args string
	var rev int
	e := w.db.QueryRowContext(ctx, `select ps.run_id,pr.project_id,ps.step_key,ps.kind,ps.command,ps.args_json,ps.timeout_seconds,ps.max_attempts,ps.reversible,ps.attempt from pipeline_steps ps join pipeline_runs pr on pr.id=ps.run_id where ps.id=?`, id).Scan(&s.RunID, &project, &s.Key, &s.Kind, &s.Command, &args, &s.TimeoutSeconds, &s.MaxAttempts, &rev, &s.Attempt)
	if e != nil {
		return e
	}
	s.ID = id
	s.Reversible = rev == 1
	_ = jsonUnmarshal(args, &s.Args)
	claim, e := w.db.ExecContext(ctx, `update pipeline_steps set status='running',attempt=attempt+1,started_at=? where id=? and status='pending'`, now(), id)
	if e != nil {
		return e
	}
	n, _ := claim.RowsAffected()
	if n != 1 {
		return nil
	}
	dir, e := w.projectDir(project)
	if e != nil {
		return w.finish(id, s, Result{FailureClass: "environment_failure", Err: e})
	}
	return w.finish(id, s, w.runner.Run(ctx, dir, s))
}
func (w *Worker) finish(id int64, s Step, r Result) error {
	if r.Err == nil {
		_, e := w.db.Exec(`update pipeline_steps set status='passed',output_summary=?,failure_class='',completed_at=? where id=?`, redact(r.Output), now(), id)
		if e == nil {
			w.refreshRun(s.RunID)
		}
		return e
	}
	attempt := s.Attempt + 1
	retry := r.FailureClass == "environment_failure" && s.Reversible && attempt < s.MaxAttempts
	status := "failed"
	var next any
	if retry {
		status = "pending"
		next = time.Now().UTC().Add(time.Second).Format(time.RFC3339Nano)
	}
	_, e := w.db.Exec(`update pipeline_steps set status=?,failure_class=?,output_summary=?,next_retry_at=?,completed_at=? where id=?`, status, r.FailureClass, redact(fmt.Sprint(r.Err)), next, now(), id)
	if !retry {
		_, _ = w.db.Exec(`insert into issues(title,body,status,blocking,created_by,created_at) values(?,?,'blocking',1,'pipeline',?)`, "流水线步骤失败", redact(fmt.Sprint(r.Err)), now())
		if s.Kind == "release" && s.Reversible {
			var safe string
			_ = w.db.QueryRow(`select safe_commit from pipeline_runs where id=?`, s.RunID).Scan(&safe)
			_, _ = w.db.Exec(`insert into pipeline_rollbacks(run_id,safe_commit,status,created_at) values(?,?,'awaiting_confirmation',?) on conflict(run_id) do nothing`, s.RunID, safe, now())
		}
	}
	w.refreshRun(s.RunID)
	return e
}
func (w *Worker) refreshRun(id int64) {
	var pending, failed int
	_ = w.db.QueryRow(`select sum(case when status in ('pending','running','awaiting_confirmation') then 1 else 0 end),sum(case when status='failed' then 1 else 0 end) from pipeline_steps where run_id=?`, id).Scan(&pending, &failed)
	status := "passed"
	if failed > 0 {
		status = "failed"
	} else if pending > 0 {
		status = "running"
	}
	_, _ = w.db.Exec(`update pipeline_runs set status=?,completed_at=case when ? in ('passed','failed') then ? else completed_at end where id=?`, status, status, now(), id)
}
func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
