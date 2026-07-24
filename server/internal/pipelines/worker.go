package pipelines

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"wanxiang-agent/server/internal/gitx"
)

type Worker struct {
	db         *sql.DB
	runner     Runner
	interval   time.Duration
	projectDir func(int64) (string, error)
	dataDir    string
	stop       chan struct{}
	done       chan struct{}
	once       sync.Once
	cancel     context.CancelFunc
	pm2Path    func(context.Context, string, string) (string, error)
}

// NewWorker 创建流水线执行轮询器。
func NewWorker(db *sql.DB, r Runner, interval time.Duration, projectDir func(int64) (string, error), dataDirs ...string) *Worker {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	dataDir := ""
	if len(dataDirs) > 0 {
		dataDir = dataDirs[0]
	}
	if dataDir == "" {
		dataDir = pipelineDataDir(db)
	}
	w := &Worker{db: db, runner: r, interval: interval, projectDir: projectDir, dataDir: dataDir, stop: make(chan struct{}), done: make(chan struct{})}
	w.pm2Path = queryPM2Path
	return w
}

// Start 启动流水线步骤与回滚轮询。
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

// Close 停止流水线轮询并等待退出。
func (w *Worker) Close() {
	w.once.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
		close(w.stop)
		<-w.done
	})
}

// Scan 恢复中断步骤并执行可运行流水线任务。
func (w *Worker) Scan(ctx context.Context) error {
	if err := w.recoverInterruptedSteps(ctx); err != nil {
		return err
	}
	if err := w.recoverInterruptedReleases(ctx); err != nil {
		return err
	}
	if err := w.recoverInterruptedRollbacks(ctx); err != nil {
		return err
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

func (w *Worker) recoverInterruptedSteps(ctx context.Context) error {
	rows, err := w.db.QueryContext(ctx, `select ps.id,ps.run_id,pr.project_id,ps.kind,
			ps.timeout_seconds,ps.started_at,ps.attempt,ps.max_attempts
		from pipeline_steps ps
		join pipeline_runs pr on pr.id=ps.run_id
		where ps.status='running' and ps.kind in ('test','build')`)
	if err != nil {
		return err
	}
	type interruptedStep struct {
		id, runID, project    int64
		kind, started         string
		timeout, attempt, max int
	}
	var items []interruptedStep
	for rows.Next() {
		var item interruptedStep
		if err = rows.Scan(&item.id, &item.runID, &item.project, &item.kind,
			&item.timeout, &item.started, &item.attempt, &item.max); err != nil {
			rows.Close()
			return err
		}
		started, parseErr := time.Parse(time.RFC3339Nano, item.started)
		if parseErr == nil && !time.Now().UTC().Before(started.Add(time.Duration(item.timeout)*time.Second+30*time.Second)) {
			items = append(items, item)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range items {
		if strings.TrimSpace(w.dataDir) == "" {
			return errors.New("pipeline project lock data directory is unavailable")
		}
		release, lockErr := gitx.AcquireProjectLock(ctx, w.dataDir, item.project)
		if lockErr != nil {
			return fmt.Errorf("acquire interrupted pipeline project git lock: %w", lockErr)
		}
		current, reloadErr := w.loadPipelineStep(ctx, item.id)
		same := reloadErr == nil &&
			current.status == "running" &&
			current.step.RunID == item.runID &&
			current.project == item.project &&
			current.step.Kind == item.kind &&
			current.step.TimeoutSeconds == item.timeout &&
			current.step.Attempt == item.attempt &&
			current.step.MaxAttempts == item.max &&
			current.startedAt.Valid &&
			current.startedAt.String == item.started
		if same {
			reloadErr = w.finishInterruptedStep(ctx, item)
		}
		release()
		if reloadErr != nil && !errors.Is(reloadErr, sql.ErrNoRows) {
			return reloadErr
		}
	}
	return nil
}

func (w *Worker) finishInterruptedStep(ctx context.Context, item struct {
	id, runID, project    int64
	kind, started         string
	timeout, attempt, max int
}) error {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if item.attempt < item.max {
		result, updateErr := tx.ExecContext(ctx, `update pipeline_steps
			set status='pending',next_retry_at=?
			where id=? and run_id=? and status='running' and kind=?
				and attempt=? and started_at=?`,
			now(), item.id, item.runID, item.kind, item.attempt, item.started)
		if updateErr != nil {
			return updateErr
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return nil
		}
		return tx.Commit()
	}
	result, err := tx.ExecContext(ctx, `update pipeline_steps
		set status='failed',failure_class='environment_failure',
			output_summary='worker interrupted and retry budget exhausted',completed_at=?
		where id=? and run_id=? and status='running' and kind=?
			and attempt=? and started_at=?`,
		now(), item.id, item.runID, item.kind, item.attempt, item.started)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil
	}
	if _, err = tx.ExecContext(ctx, `insert into issues(task_id,title,body,status,blocking,created_by,created_at)
		select task_id,'流水线恢复失败','worker interrupted and retry budget exhausted','blocking',1,'pipeline',?
		from pipeline_runs where id=?`, now(), item.runID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `insert into runtime_events(task_id,event_type,actor,payload_json,created_at)
		select task_id,'pipeline.recovery.exhausted','pipeline',?,?
		from pipeline_runs where id=?`,
		fmt.Sprintf(`{"run_id":%d,"step_id":%d}`, item.runID, item.id), now(), item.runID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	w.refreshRun(item.runID)
	return nil
}

func (w *Worker) recoverInterruptedReleases(ctx context.Context) error {
	rows, err := w.db.QueryContext(ctx, `select ps.id,ps.run_id,pr.project_id,pr.safe_commit,ps.timeout_seconds,ps.started_at
		from pipeline_steps ps
		join pipeline_runs pr on pr.id=ps.run_id
		where ps.status='running' and ps.kind='release'`)
	if err != nil {
		return err
	}
	type interruptedRelease struct {
		id, runID, project int64
		safe, started      string
		timeout            int
	}
	var items []interruptedRelease
	for rows.Next() {
		var item interruptedRelease
		if err = rows.Scan(&item.id, &item.runID, &item.project, &item.safe, &item.timeout, &item.started); err != nil {
			rows.Close()
			return err
		}
		started, parseErr := time.Parse(time.RFC3339Nano, item.started)
		if parseErr == nil && !time.Now().UTC().Before(started.Add(time.Duration(item.timeout)*time.Second+30*time.Second)) {
			items = append(items, item)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range items {
		if strings.TrimSpace(w.dataDir) == "" {
			return errors.New("pipeline project lock data directory is unavailable")
		}
		release, lockErr := gitx.AcquireProjectLock(ctx, w.dataDir, item.project)
		if lockErr != nil {
			return fmt.Errorf("acquire interrupted release project git lock: %w", lockErr)
		}
		current, reloadErr := w.loadPipelineStep(ctx, item.id)
		if reloadErr == nil && current.status == "running" && current.step.Kind == "release" &&
			current.step.RunID == item.runID && current.project == item.project {
			tx, beginErr := w.db.BeginTx(ctx, nil)
			if beginErr != nil {
				release()
				return beginErr
			}
			result, updateErr := tx.ExecContext(ctx, `update pipeline_steps
				set status='failed',failure_class='environment_failure',
					output_summary='发布状态不确定，需要人工回滚确认',completed_at=?
				where id=? and run_id=? and status='running' and kind='release'`,
				now(), item.id, item.runID)
			if updateErr == nil {
				if changed, _ := result.RowsAffected(); changed != 1 {
					updateErr = errors.New("interrupted release state changed during recovery")
				}
			}
			if updateErr == nil {
				_, updateErr = tx.ExecContext(ctx, `insert into pipeline_rollbacks(run_id,safe_commit,expected_head,status,created_at)
					values(?,?,'','awaiting_confirmation',?)
					on conflict(run_id) do update set
						status='awaiting_confirmation',
						last_error='',
						completed_at=null
					where pipeline_rollbacks.status='monitoring'`,
					item.runID, item.safe, now())
			}
			if updateErr == nil {
				updateErr = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
			if updateErr != nil {
				release()
				return updateErr
			}
			w.refreshRun(item.runID)
		}
		release()
		if reloadErr != nil && !errors.Is(reloadErr, sql.ErrNoRows) {
			return reloadErr
		}
	}
	return nil
}

type pipelineStepClaim struct {
	step      Step
	project   int64
	argsJSON  string
	status    string
	startedAt sql.NullString
}

func (w *Worker) run(ctx context.Context, id int64) error {
	claimed, err := w.loadPipelineStep(ctx, id)
	if err != nil {
		return err
	}
	if claimed.status != "pending" {
		return nil
	}
	result, err := w.db.ExecContext(ctx, `update pipeline_steps set status='running',attempt=attempt+1,started_at=? where id=? and status='pending'`, now(), id)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return nil
	}
	dir, err := w.projectDir(claimed.project)
	if err != nil {
		return w.finish(ctx, id, claimed.step, Result{FailureClass: "environment_failure", Err: err})
	}
	if strings.TrimSpace(w.dataDir) == "" {
		return w.finish(ctx, id, claimed.step, Result{FailureClass: "environment_failure", Err: errors.New("pipeline project lock data directory is unavailable")})
	}
	release, err := gitx.AcquireProjectLock(ctx, w.dataDir, claimed.project)
	if err != nil {
		return w.finish(ctx, id, claimed.step, Result{FailureClass: "environment_failure", Err: fmt.Errorf("acquire pipeline project git lock: %w", err)})
	}
	defer release()
	reloaded, err := w.loadPipelineStep(ctx, id)
	if err != nil {
		return err
	}
	reloadedDir, err := w.projectDir(reloaded.project)
	if err != nil {
		return w.finish(ctx, id, claimed.step, Result{FailureClass: "environment_failure", Err: err})
	}
	if !samePipelineClaim(claimed, reloaded) || !sameProjectDirectory(dir, reloadedDir) {
		return w.failPipelineClaimChanged(ctx, id, claimed.step.RunID)
	}
	return w.executeClaimedStep(ctx, id, claimed.step, reloadedDir)
}

func (w *Worker) loadPipelineStep(ctx context.Context, id int64) (pipelineStepClaim, error) {
	var claim pipelineStepClaim
	var reversible int
	err := w.db.QueryRowContext(ctx, `select ps.run_id,pr.project_id,ps.step_key,ps.kind,ps.command,ps.args_json,
			ps.artifact,ps.health_url,ps.timeout_seconds,ps.max_attempts,ps.reversible,ps.attempt,ps.status,ps.started_at
		from pipeline_steps ps
		join pipeline_runs pr on pr.id=ps.run_id
		where ps.id=?`, id).
		Scan(&claim.step.RunID, &claim.project, &claim.step.Key, &claim.step.Kind, &claim.step.Command,
			&claim.argsJSON, &claim.step.Artifact, &claim.step.HealthURL, &claim.step.TimeoutSeconds,
			&claim.step.MaxAttempts, &reversible, &claim.step.Attempt, &claim.status, &claim.startedAt)
	if err != nil {
		return pipelineStepClaim{}, err
	}
	claim.step.ID = id
	claim.step.Reversible = reversible == 1
	if err = jsonUnmarshal(claim.argsJSON, &claim.step.Args); err != nil {
		return pipelineStepClaim{}, err
	}
	return claim, nil
}

func samePipelineClaim(claimed, reloaded pipelineStepClaim) bool {
	return reloaded.status == "running" &&
		reloaded.step.ID == claimed.step.ID &&
		reloaded.step.RunID == claimed.step.RunID &&
		reloaded.project == claimed.project &&
		reloaded.step.Key == claimed.step.Key &&
		reloaded.step.Kind == claimed.step.Kind &&
		reloaded.step.Command == claimed.step.Command &&
		reloaded.argsJSON == claimed.argsJSON &&
		reloaded.step.Artifact == claimed.step.Artifact &&
		reloaded.step.HealthURL == claimed.step.HealthURL &&
		reloaded.step.TimeoutSeconds == claimed.step.TimeoutSeconds &&
		reloaded.step.MaxAttempts == claimed.step.MaxAttempts &&
		reloaded.step.Reversible == claimed.step.Reversible &&
		reloaded.step.Attempt == claimed.step.Attempt+1 &&
		reloaded.startedAt.Valid
}

func (w *Worker) failPipelineClaimChanged(ctx context.Context, id, runID int64) error {
	result, err := w.db.ExecContext(ctx, `update pipeline_steps
		set status='failed',failure_class='environment_failure',
			output_summary='pipeline changed while waiting for project git lock',completed_at=?
		where id=? and status='running'`,
		now(), id)
	if err == nil {
		if changed, _ := result.RowsAffected(); changed == 1 {
			w.refreshRun(runID)
		}
	}
	return err
}

func (w *Worker) executeClaimedStep(ctx context.Context, id int64, step Step, dir string) error {
	if step.Kind == "build" && step.Artifact != "" && w.runHasRelease(step.RunID) {
		if err := w.ensureBackup(step.RunID, dir, step.Artifact); err != nil {
			return w.finish(ctx, id, step, Result{FailureClass: "environment_failure", Err: err})
		}
	}
	if step.Kind == "release" {
		if err := w.validateRelease(ctx, step.RunID, dir, step.Artifact); err != nil {
			return w.finish(ctx, id, step, Result{FailureClass: "environment_failure", Err: err})
		}
		expectedHead, err := pipelineGitValue(ctx, dir, "rev-parse", "HEAD")
		if err != nil {
			return w.finish(ctx, id, step, Result{FailureClass: "environment_failure", Err: err})
		}
		if err = w.armReleaseRollback(ctx, step.RunID, expectedHead); err != nil {
			return w.finish(ctx, id, step, Result{FailureClass: "environment_failure", Err: err})
		}
		var artifact string
		_ = w.db.QueryRowContext(ctx, `select artifact from pipeline_steps where run_id=? and kind='build'`, step.RunID).Scan(&artifact)
		expected, pathErr := safeProjectPath(dir, artifact)
		actual, bindErr := w.pm2Path(ctx, dir, step.Args[1])
		if pathErr != nil || bindErr != nil || actual != expected {
			return w.finish(ctx, id, step, Result{FailureClass: "environment_failure", Err: fmt.Errorf("pm2 executable does not match release artifact")})
		}
	}
	result := w.runner.Run(ctx, dir, step)
	if result.Err == nil && step.Kind == "build" && step.Artifact != "" {
		hash, err := hashArtifact(filepath.Join(dir, step.Artifact))
		if err != nil {
			result = Result{FailureClass: "environment_failure", Err: err}
		} else if _, err = w.db.ExecContext(ctx, `update pipeline_runs set artifact_hash=? where id=?`, hash, step.RunID); err != nil {
			result = Result{FailureClass: "environment_failure", Err: err}
		}
	}
	if result.Err == nil && step.Kind == "release" {
		result.Err = checkHealth(ctx, step.HealthURL)
		if result.Err != nil {
			result.FailureClass = "environment_failure"
		}
	}
	return w.finish(ctx, id, step, result)
}

func (w *Worker) armReleaseRollback(ctx context.Context, runID int64, expectedHead string) error {
	if strings.TrimSpace(expectedHead) == "" {
		return errors.New("release expected head is unavailable")
	}
	var safe string
	if err := w.db.QueryRowContext(ctx, `select safe_commit from pipeline_runs where id=?`, runID).Scan(&safe); err != nil {
		return err
	}
	_, err := w.db.ExecContext(ctx, `insert into pipeline_rollbacks(run_id,safe_commit,expected_head,status,created_at)
		values(?,?,?,'monitoring',?)
		on conflict(run_id) do update set
			safe_commit=excluded.safe_commit,
			expected_head=excluded.expected_head,
			status='monitoring',
			last_error='',
			started_at=null,
			completed_at=null
		where pipeline_rollbacks.status='monitoring'
			and (pipeline_rollbacks.expected_head='' or pipeline_rollbacks.expected_head=excluded.expected_head)`,
		runID, safe, expectedHead, now())
	if err != nil {
		return err
	}
	var storedSafe, storedHead, status string
	if err = w.db.QueryRowContext(ctx, `select safe_commit,expected_head,status from pipeline_rollbacks where run_id=?`, runID).
		Scan(&storedSafe, &storedHead, &status); err != nil {
		return err
	}
	if storedSafe != safe || storedHead != expectedHead || status != "monitoring" {
		return errors.New("release rollback guard conflicts with existing state")
	}
	return nil
}

func (w *Worker) finish(ctx context.Context, id int64, s Step, r Result) error {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if r.Err == nil {
		result, updateErr := tx.ExecContext(ctx, `update pipeline_steps
			set status='passed',output_summary=?,failure_class='',completed_at=?
			where id=? and status='running'`, redact(r.Output), now(), id)
		if updateErr != nil {
			return updateErr
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return errors.New("pipeline step state changed during finalization")
		}
		if s.Kind == "release" {
			result, updateErr = tx.ExecContext(ctx, `delete from pipeline_rollbacks where run_id=? and status='monitoring'`, s.RunID)
			if updateErr != nil {
				return updateErr
			}
			if changed, _ := result.RowsAffected(); changed != 1 {
				return errors.New("release rollback guard changed during finalization")
			}
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		w.refreshRun(s.RunID)
		return nil
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
	result, updateErr := tx.ExecContext(ctx, `update pipeline_steps
		set status=?,failure_class=?,output_summary=?,next_retry_at=?,completed_at=?
		where id=? and status='running'`,
		status, r.FailureClass, redact(fmt.Sprint(r.Err)), next, now(), id)
	if updateErr != nil {
		return updateErr
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("pipeline step state changed during finalization")
	}
	if !retry {
		_, _ = tx.ExecContext(ctx, `insert into issues(task_id,title,body,status,blocking,created_by,created_at) select task_id,?,?,'blocking',1,'pipeline',? from pipeline_runs where id=?`, "流水线步骤失败", redact(fmt.Sprint(r.Err)), now(), s.RunID)
		_, _ = tx.ExecContext(ctx, `insert into runtime_events(task_id,event_type,actor,payload_json,created_at) select task_id,'pipeline.step.failed','pipeline',?,? from pipeline_runs where id=?`, fmt.Sprintf(`{"run_id":%d,"step_id":%d}`, s.RunID, id), now(), s.RunID)
		if s.Kind == "release" && s.Reversible {
			var safe string
			if err = tx.QueryRowContext(ctx, `select safe_commit from pipeline_runs where id=?`, s.RunID).Scan(&safe); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `insert into pipeline_rollbacks(run_id,safe_commit,expected_head,status,created_at)
				values(?,?,'','awaiting_confirmation',?)
				on conflict(run_id) do update set
					status='awaiting_confirmation',
					last_error='',
					completed_at=null
				where pipeline_rollbacks.status='monitoring'`,
				s.RunID, safe, now()); err != nil {
				return err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	w.refreshRun(s.RunID)
	return nil
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

type rollbackWork struct {
	id       int64
	runID    int64
	project  int64
	safe     string
	expected string
	runSafe  string
	dir      string
	status   string
}

func (w *Worker) recoverInterruptedRollbacks(ctx context.Context) error {
	rows, err := w.db.QueryContext(ctx, `select rb.id,rb.run_id,rb.safe_commit,rb.expected_head,pr.safe_commit,pr.project_id
		from pipeline_rollbacks rb
		join pipeline_runs pr on pr.id=rb.run_id
		where rb.status='running' and rb.started_at<?`,
		time.Now().UTC().Add(-1*time.Hour).Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	var items []rollbackWork
	for rows.Next() {
		var item rollbackWork
		if err = rows.Scan(&item.id, &item.runID, &item.safe, &item.expected, &item.runSafe, &item.project); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range items {
		if strings.TrimSpace(w.dataDir) == "" {
			return errors.New("pipeline project lock data directory is unavailable")
		}
		release, lockErr := gitx.AcquireProjectLock(ctx, w.dataDir, item.project)
		if lockErr != nil {
			return fmt.Errorf("acquire rollback project git lock: %w", lockErr)
		}
		current, reloadErr := w.reloadRollback(ctx, item.id)
		if reloadErr == nil && current.status == "running" &&
			current.runID == item.runID && current.project == item.project &&
			current.safe == item.safe && current.expected == item.expected &&
			current.runSafe == item.runSafe && current.safe == current.runSafe {
			_, reloadErr = w.db.ExecContext(ctx, `update pipeline_rollbacks
				set status='failed',last_error='rollback worker interrupted; explicit reconfirmation required',completed_at=?
				where id=? and status='running' and started_at<?`,
				now(), item.id, time.Now().UTC().Add(-1*time.Hour).Format(time.RFC3339Nano))
		} else if reloadErr == nil && current.status == "running" {
			reloadErr = w.markRollbackStateChanged(ctx, item.id)
		}
		release()
		if reloadErr != nil && !errors.Is(reloadErr, sql.ErrNoRows) {
			return reloadErr
		}
	}
	return nil
}

func (w *Worker) scanRollbacks(ctx context.Context) error {
	rows, e := w.db.QueryContext(ctx, `select rb.id,rb.run_id,rb.safe_commit,rb.expected_head,pr.safe_commit,pr.project_id
		from pipeline_rollbacks rb
		join pipeline_runs pr on pr.id=rb.run_id
		where rb.status='pending'
		order by rb.id
		limit 5`)
	if e != nil {
		return e
	}
	defer rows.Close()
	var items []rollbackWork
	for rows.Next() {
		var x rollbackWork
		if e = rows.Scan(&x.id, &x.runID, &x.safe, &x.expected, &x.runSafe, &x.project); e != nil {
			rows.Close()
			return e
		}
		items = append(items, x)
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		return e
	}
	rows.Close()
	for _, x := range items {
		claim, claimErr := w.db.ExecContext(ctx, `update pipeline_rollbacks set status='running',started_at=? where id=? and status='pending'`, now(), x.id)
		if claimErr != nil {
			return claimErr
		}
		n, _ := claim.RowsAffected()
		if n != 1 {
			continue
		}
		x.dir, e = w.projectDir(x.project)
		if e != nil {
			if finishErr := w.finishClaimedRollback(ctx, x, false, e); finishErr != nil {
				return finishErr
			}
			continue
		}
		if strings.TrimSpace(w.dataDir) == "" {
			if finishErr := w.finishClaimedRollback(ctx, x, false, errors.New("pipeline project lock data directory is unavailable")); finishErr != nil {
				return finishErr
			}
			continue
		}
		release, lockErr := gitx.AcquireProjectLock(ctx, w.dataDir, x.project)
		if lockErr != nil {
			if finishErr := w.finishClaimedRollback(ctx, x, false, fmt.Errorf("acquire rollback project git lock: %w", lockErr)); finishErr != nil {
				return finishErr
			}
			continue
		}
		e = w.runClaimedRollback(ctx, x)
		release()
		if e != nil {
			return e
		}
	}
	return nil
}

func (w *Worker) runClaimedRollback(ctx context.Context, claimed rollbackWork) error {
	current, err := w.reloadRollback(ctx, claimed.id)
	if err != nil {
		return err
	}
	if current.status != "running" {
		return nil
	}
	current.dir, err = w.projectDir(current.project)
	if err != nil {
		return w.finishClaimedRollback(ctx, claimed, false, err)
	}
	if current.runID != claimed.runID || current.project != claimed.project ||
		current.safe != claimed.safe || current.expected != claimed.expected ||
		current.runSafe != claimed.runSafe || current.safe != current.runSafe ||
		!sameProjectDirectory(current.dir, claimed.dir) {
		return w.markRollbackStateChanged(ctx, claimed.id)
	}

	err = rollbackGit(ctx, current.dir, current.safe, current.expected)
	gitReset := err == nil
	if err == nil {
		var s Step
		var args string
		var rev int
		var backupPath, backupHash, artifact string
		err = w.db.QueryRowContext(ctx, `select pr.backup_path,pr.backup_hash,ps.id,ps.step_key,ps.kind,ps.command,ps.args_json,ps.health_url,ps.timeout_seconds,ps.max_attempts,ps.reversible,(select artifact from pipeline_steps where run_id=pr.id and kind='build' order by id desc limit 1) from pipeline_runs pr join pipeline_steps ps on ps.run_id=pr.id and ps.kind='release' where pr.id=? order by ps.id desc limit 1`, current.runID).Scan(&backupPath, &backupHash, &s.ID, &s.Key, &s.Kind, &s.Command, &args, &s.HealthURL, &s.TimeoutSeconds, &s.MaxAttempts, &rev, &artifact)
		s.Reversible = rev == 1
		_ = jsonUnmarshal(args, &s.Args)
		if err == nil {
			err = restoreBackup(current.dir, artifact, backupPath, backupHash)
		}
		if err == nil {
			expected, pathErr := safeProjectPath(current.dir, artifact)
			actual, bindErr := w.pm2Path(ctx, current.dir, s.Args[1])
			if pathErr != nil || bindErr != nil || actual != expected {
				err = fmt.Errorf("pm2 executable does not match rollback artifact")
			}
		}
		if err == nil {
			result := w.runner.Run(ctx, current.dir, s)
			err = result.Err
		}
		if err == nil {
			err = checkHealth(ctx, s.HealthURL)
		}
	}
	return w.finishClaimedRollback(ctx, current, gitReset, err)
}

func (w *Worker) reloadRollback(ctx context.Context, rollbackID int64) (rollbackWork, error) {
	var current rollbackWork
	err := w.db.QueryRowContext(ctx, `select rb.id,rb.run_id,rb.safe_commit,rb.expected_head,rb.status,pr.safe_commit,pr.project_id
		from pipeline_rollbacks rb
		join pipeline_runs pr on pr.id=rb.run_id
		where rb.id=?`, rollbackID).
		Scan(&current.id, &current.runID, &current.safe, &current.expected, &current.status, &current.runSafe, &current.project)
	if err != nil {
		return rollbackWork{}, err
	}
	return current, nil
}

func (w *Worker) finishClaimedRollback(ctx context.Context, item rollbackWork, gitReset bool, cause error) error {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if gitReset {
		result, updateErr := tx.ExecContext(ctx, `update projects set main_commit=? where id=?`, item.safe, item.project)
		if updateErr != nil {
			return updateErr
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return errors.New("rollback project state changed during git reset")
		}
	}
	status := "rolled_back"
	message := ""
	if cause != nil {
		status = "failed"
		message = redact(cause.Error())
	}
	result, err := tx.ExecContext(ctx, `update pipeline_rollbacks
		set status=?,last_error=?,completed_at=?
		where id=? and status='running' and run_id=? and safe_commit=? and expected_head=?
			and exists(
				select 1 from pipeline_runs pr
				where pr.id=pipeline_rollbacks.run_id and pr.project_id=? and pr.safe_commit=?
			)`,
		status, message, now(), item.id, item.runID, item.safe, item.expected, item.project, item.runSafe)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("rollback state changed during finalization")
	}
	return tx.Commit()
}

func (w *Worker) markRollbackStateChanged(ctx context.Context, rollbackID int64) error {
	_, err := w.db.ExecContext(ctx, `update pipeline_rollbacks
		set status='failed',last_error='rollback changed while waiting for project git lock',completed_at=?
		where id=? and status='running'`,
		now(), rollbackID)
	return err
}

func sameProjectDirectory(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	return leftErr == nil && rightErr == nil && filepath.Clean(leftAbs) == filepath.Clean(rightAbs)
}

func pipelineGitValue(ctx context.Context, dir string, args ...string) (string, error) {
	output, err := gitx.Run(ctx, dir, args...)
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(output))
	}
	return strings.TrimSpace(output), nil
}

func pipelineDataDir(db *sql.DB) string {
	if db == nil {
		return ""
	}
	rows, err := db.Query(`pragma database_list`)
	if err != nil {
		return ""
	}
	defer rows.Close()
	for rows.Next() {
		var sequence int
		var name, path string
		if rows.Scan(&sequence, &name, &path) == nil && name == "main" && path != "" {
			absolute, absErr := filepath.Abs(path)
			if absErr == nil {
				return filepath.Dir(absolute)
			}
			return ""
		}
	}
	return ""
}

func (w *Worker) runHasRelease(runID int64) bool {
	var n int
	_ = w.db.QueryRow(`select count(*) from pipeline_steps where run_id=? and kind='release'`, runID).Scan(&n)
	return n == 1
}

func queryPM2Path(ctx context.Context, dir, app string) (string, error) {
	pm2Home, err := trustedPM2Home()
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "pm2", "jlist")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir(), "PM2_HOME=" + pm2Home}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("pm2 binding unavailable")
	}
	var items []struct {
		Name string `json:"name"`
		Env  struct {
			ExecPath string `json:"pm_exec_path"`
		} `json:"pm2_env"`
	}
	if json.Unmarshal(out, &items) != nil {
		return "", fmt.Errorf("pm2 binding invalid")
	}
	for _, item := range items {
		if item.Name == app {
			p, err := filepath.EvalSymlinks(item.Env.ExecPath)
			if err != nil {
				return "", err
			}
			return p, nil
		}
	}
	return "", fmt.Errorf("pm2 app not found")
}

func (w *Worker) ensureBackup(runID int64, dir, artifact string) error {
	var existing, expectedHash string
	if err := w.db.QueryRow(`select backup_path,backup_hash from pipeline_runs where id=?`, runID).Scan(&existing, &expectedHash); err != nil || existing != "" {
		return err
	}
	src, err := safeProjectPath(dir, artifact)
	if err != nil {
		return err
	}
	if _, err = os.Lstat(src); err != nil {
		return fmt.Errorf("existing deployment artifact required: %w", err)
	}
	sourceHash, err := hashArtifact(src)
	if err != nil {
		return err
	}
	if expectedHash == "" {
		if _, err = w.db.Exec(`update pipeline_runs set backup_hash=? where id=? and backup_hash=''`, sourceHash, runID); err != nil {
			return err
		}
		expectedHash = sourceHash
	}
	if expectedHash != sourceHash {
		return fmt.Errorf("deployment artifact changed during backup")
	}
	dst := filepath.Join(filepath.Dir(dir), ".wanxiang-release-backups", filepath.Base(dir), fmt.Sprint(runID))
	if _, statErr := os.Lstat(dst); statErr == nil {
		hash, hashErr := hashArtifact(dst)
		if hashErr == nil && hash == expectedHash {
			_, err = w.db.Exec(`update pipeline_runs set backup_path=? where id=? and backup_path=''`, dst, runID)
			return err
		}
		if err = os.RemoveAll(dst); err != nil {
			return err
		}
	}
	tmp := dst + ".partial"
	_ = os.RemoveAll(tmp)
	if err = copyArtifact(src, tmp); err != nil {
		return err
	}
	hash, err := hashArtifact(tmp)
	if err != nil || hash != expectedHash {
		return fmt.Errorf("backup verification failed")
	}
	if err = os.Rename(tmp, dst); err != nil {
		return err
	}
	_, err = w.db.Exec(`update pipeline_runs set backup_path=? where id=? and backup_path=''`, dst, runID)
	return err
}

func (w *Worker) validateRelease(ctx context.Context, runID int64, dir, _ string) error {
	var safe, expected, artifact, definitionHash string
	if err := w.db.QueryRow(`select pr.safe_commit,pr.artifact_hash,pr.definition_hash,(select artifact from pipeline_steps where run_id=pr.id and kind='build' order by id desc limit 1) from pipeline_runs pr where pr.id=?`, runID).Scan(&safe, &expected, &definitionHash, &artifact); err != nil {
		return err
	}
	if len(safe) != 40 {
		return fmt.Errorf("release safe commit invalid")
	}
	definition, err := LoadDefinition(dir)
	if err != nil {
		return fmt.Errorf("release definition unavailable")
	}
	raw, _ := json.Marshal(definition)
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != definitionHash {
		return fmt.Errorf("release definition drifted")
	}
	for _, spec := range []struct {
		args []string
		want string
	}{{[]string{"branch", "--show-current"}, "main"}, {[]string{"status", "--porcelain"}, ""}, {[]string{"rev-parse", "HEAD"}, safe}} {
		cmd := exec.CommandContext(ctx, "git", spec.args...)
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil || strings.TrimSpace(string(out)) != spec.want {
			return fmt.Errorf("release git baseline drifted")
		}
	}
	p, err := safeProjectPath(dir, artifact)
	if err != nil {
		return err
	}
	actual, err := hashArtifact(p)
	if err != nil || expected == "" || actual != expected {
		return fmt.Errorf("release artifact hash mismatch")
	}
	return nil
}

func safeProjectPath(root, rel string) (string, error) {
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	p := filepath.Join(base, rel)
	parent, err := filepath.EvalSymlinks(filepath.Dir(p))
	if err != nil {
		return "", fmt.Errorf("artifact parent unavailable")
	}
	p = filepath.Join(parent, filepath.Base(p))
	if p != base && !strings.HasPrefix(p, base+string(filepath.Separator)) {
		return "", fmt.Errorf("artifact escapes project")
	}
	return p, nil
}

func copyArtifact(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe artifact")
	}
	if info.IsDir() {
		if err = os.MkdirAll(dst, 0700); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, ent := range entries {
			if err = copyArtifact(filepath.Join(src, ent.Name()), filepath.Join(dst, ent.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if err = os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, cpErr := io.Copy(out, in)
	closeErr := out.Close()
	if cpErr != nil {
		return cpErr
	}
	return closeErr
}

func restoreBackup(dir, artifact, backup, expected string) error {
	if backup == "" || expected == "" {
		return fmt.Errorf("rollback backup unavailable")
	}
	h, err := hashArtifact(backup)
	if err != nil || h != expected {
		return fmt.Errorf("rollback backup hash mismatch")
	}
	dst, err := safeProjectPath(dir, artifact)
	if err != nil {
		return err
	}
	if err = os.RemoveAll(dst); err != nil {
		return err
	}
	if err = copyArtifact(backup, dst); err != nil {
		return err
	}
	h, err = hashArtifact(dst)
	if err != nil || h != expected {
		return fmt.Errorf("restored artifact hash mismatch")
	}
	return nil
}

func checkHealth(ctx context.Context, raw string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("health status %d", resp.StatusCode)
	}
	return nil
}
func rollbackGit(ctx context.Context, dir, safe, expectedHead string) error {
	if !validPipelineCommit(safe) || !validPipelineCommit(expectedHead) {
		return errors.New("rollback commit identity is unavailable")
	}
	status, err := pipelineGitValue(ctx, dir, "status", "--porcelain")
	if err != nil {
		return err
	}
	if status != "" {
		return errors.New("rollback workspace is dirty")
	}
	branch, err := pipelineGitValue(ctx, dir, "branch", "--show-current")
	if err != nil {
		return err
	}
	if branch != "main" {
		return errors.New("rollback requires main")
	}
	head, err := pipelineGitValue(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if head != expectedHead && head != safe {
		return errors.New("rollback main advanced after release failure")
	}
	if _, err = pipelineGitValue(ctx, dir, "cat-file", "-e", safe+"^{commit}"); err != nil {
		return err
	}
	if _, err = pipelineGitValue(ctx, dir, "merge-base", "--is-ancestor", safe, expectedHead); err != nil {
		return errors.New("rollback safe commit is not an ancestor of expected head")
	}
	if head == expectedHead {
		if _, err = pipelineGitValue(ctx, dir, "reset", "--hard", safe); err != nil {
			return err
		}
	}
	head, err = pipelineGitValue(ctx, dir, "rev-parse", "HEAD")
	if err != nil || head != safe {
		return errors.New("rollback head verification failed")
	}
	status, err = pipelineGitValue(ctx, dir, "status", "--porcelain")
	if err != nil || status != "" {
		return errors.New("rollback cleanliness verification failed")
	}
	return nil
}

func validPipelineCommit(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
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
