package pipelines

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
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
	cancel     context.CancelFunc
}

func NewWorker(db *sql.DB, r Runner, interval time.Duration, projectDir func(int64) (string, error)) *Worker {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &Worker{db: db, runner: r, interval: interval, projectDir: projectDir, stop: make(chan struct{}), done: make(chan struct{})}
}
func (w *Worker) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	go func() {
		defer close(w.done)
		t := time.NewTicker(w.interval)
		defer t.Stop()
		for {
			_ = w.Scan(ctx)
			select {
			case <-t.C:
			case <-ctx.Done():
				return
			}
		}
	}()
}
func (w *Worker) Close() {
	w.once.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
		close(w.stop)
		<-w.done
	})
}
func (w *Worker) Scan(ctx context.Context) error {
	stale := time.Now().UTC().Add(-5 * time.Minute).Format(time.RFC3339Nano)
	_, _ = w.db.ExecContext(ctx, `update pipeline_steps set status='pending',next_retry_at=? where status='running' and kind in ('test','build') and started_at<?`, now(), stale)
	rowsRelease, _ := w.db.QueryContext(ctx, `select ps.id,ps.run_id,pr.safe_commit from pipeline_steps ps join pipeline_runs pr on pr.id=ps.run_id where ps.status='running' and ps.kind='release' and ps.started_at<?`, stale)
	if rowsRelease != nil {
		for rowsRelease.Next() {
			var id, run int64
			var safe string
			_ = rowsRelease.Scan(&id, &run, &safe)
			_, _ = w.db.ExecContext(ctx, `update pipeline_steps set status='failed',failure_class='environment_failure',output_summary='发布状态不确定，需要人工回滚确认',completed_at=? where id=?`, now(), id)
			_, _ = w.db.ExecContext(ctx, `insert into pipeline_rollbacks(run_id,safe_commit,status,created_at) values(?,?,'awaiting_confirmation',?) on conflict(run_id) do nothing`, run, safe, now())
		}
		rowsRelease.Close()
	}
	if err := w.scanRollbacks(ctx); err != nil {
		return err
	}
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
	e := w.db.QueryRowContext(ctx, `select ps.run_id,pr.project_id,ps.step_key,ps.kind,ps.command,ps.args_json,ps.artifact,ps.timeout_seconds,ps.max_attempts,ps.reversible,ps.attempt from pipeline_steps ps join pipeline_runs pr on pr.id=ps.run_id where ps.id=?`, id).Scan(&s.RunID, &project, &s.Key, &s.Kind, &s.Command, &args, &s.Artifact, &s.TimeoutSeconds, &s.MaxAttempts, &rev, &s.Attempt)
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
	result := w.runner.Run(ctx, dir, s)
	if result.Err == nil && s.Kind == "build" && s.Artifact != "" {
		hash, e := hashArtifact(filepath.Join(dir, s.Artifact))
		if e != nil {
			result = Result{FailureClass: "environment_failure", Err: e}
		} else {
			_, _ = w.db.ExecContext(ctx, `update pipeline_runs set artifact_hash=? where id=?`, hash, s.RunID)
		}
	}
	return w.finish(id, s, result)
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
	retryable := r.FailureClass == "environment_failure" || r.FailureClass == "provider_failure"
	retry := retryable && s.Reversible && attempt < s.MaxAttempts
	status := "failed"
	var next any
	if retry {
		status = "pending"
		next = time.Now().UTC().Add(time.Second).Format(time.RFC3339Nano)
	}
	_, e := w.db.Exec(`update pipeline_steps set status=?,failure_class=?,output_summary=?,next_retry_at=?,completed_at=? where id=?`, status, r.FailureClass, redact(fmt.Sprint(r.Err)), next, now(), id)
	if !retry {
		_, _ = w.db.Exec(`insert into issues(task_id,title,body,status,blocking,created_by,created_at) select task_id,?,?,'blocking',1,'pipeline',? from pipeline_runs where id=?`, "流水线步骤失败", redact(fmt.Sprint(r.Err)), now(), s.RunID)
		_, _ = w.db.Exec(`insert into runtime_events(task_id,event_type,actor,payload_json,created_at) select task_id,'pipeline.step.failed','pipeline',?,? from pipeline_runs where id=?`, fmt.Sprintf(`{"run_id":%d,"step_id":%d}`, s.RunID, id), now(), s.RunID)
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
func (w *Worker) scanRollbacks(ctx context.Context) error {
	rows, e := w.db.QueryContext(ctx, `select rb.id,rb.run_id,rb.safe_commit,pr.project_id from pipeline_rollbacks rb join pipeline_runs pr on pr.id=rb.run_id where rb.status='pending' order by rb.id limit 5`)
	if e != nil {
		return e
	}
	defer rows.Close()
	type item struct {
		id, run, project int64
		safe             string
	}
	var items []item
	for rows.Next() {
		var x item
		_ = rows.Scan(&x.id, &x.run, &x.safe, &x.project)
		items = append(items, x)
	}
	rows.Close()
	for _, x := range items {
		claim, _ := w.db.ExecContext(ctx, `update pipeline_rollbacks set status='running' where id=? and status='pending'`, x.id)
		n, _ := claim.RowsAffected()
		if n != 1 {
			continue
		}
		dir, e := w.projectDir(x.project)
		if e == nil {
			e = rollbackGit(ctx, dir, x.safe)
		}
		if e == nil {
			var s Step
			var args string
			var rev int
			e = w.db.QueryRowContext(ctx, `select id,step_key,kind,command,args_json,timeout_seconds,max_attempts,reversible from pipeline_steps where run_id=? and kind='release' order by id desc limit 1`, x.run).Scan(&s.ID, &s.Key, &s.Kind, &s.Command, &args, &s.TimeoutSeconds, &s.MaxAttempts, &rev)
			s.Reversible = rev == 1
			_ = jsonUnmarshal(args, &s.Args)
			if e == nil {
				r := w.runner.Run(ctx, dir, s)
				e = r.Err
			}
		}
		status := "rolled_back"
		if e != nil {
			status = "failed"
		}
		_, _ = w.db.ExecContext(ctx, `update pipeline_rollbacks set status=?,last_error=?,completed_at=? where id=?`, status, redact(fmt.Sprint(e)), now(), x.id)
	}
	return nil
}
func rollbackGit(ctx context.Context, dir, safe string) error {
	for _, args := range [][]string{{"status", "--porcelain"}, {"branch", "--show-current"}, {"cat-file", "-e", safe + "^{commit}"}} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		out, e := cmd.CombinedOutput()
		if e != nil {
			return e
		}
		if args[0] == "status" && strings.TrimSpace(string(out)) != "" {
			return fmt.Errorf("rollback workspace is dirty")
		}
		if args[0] == "branch" && strings.TrimSpace(string(out)) != "main" {
			return fmt.Errorf("rollback requires main")
		}
	}
	cmd := exec.CommandContext(ctx, "git", "reset", "--hard", safe)
	cmd.Dir = dir
	return cmd.Run()
}
func hashArtifact(path string) (string, error) {
	info, e := os.Lstat(path)
	if e != nil || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("artifact unavailable")
	}
	h := sha256.New()
	if !info.IsDir() {
		b, e := os.ReadFile(path)
		if e != nil {
			return "", e
		}
		h.Write(b)
	} else {
		var files []string
		e = filepath.Walk(path, func(p string, i os.FileInfo, e error) error {
			if e != nil {
				return e
			}
			if i.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("artifact symlink")
			}
			if !i.IsDir() {
				files = append(files, p)
			}
			return nil
		})
		if e != nil {
			return "", e
		}
		sort.Strings(files)
		for _, p := range files {
			rel, _ := filepath.Rel(path, p)
			h.Write([]byte(rel))
			b, e := os.ReadFile(p)
			if e != nil {
				return "", e
			}
			h.Write(b)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
