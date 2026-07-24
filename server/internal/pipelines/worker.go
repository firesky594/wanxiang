package pipelines

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
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
	pm2Path    func(context.Context, string, string) (string, error)
}

// NewWorker 创建流水线执行轮询器。
func NewWorker(db *sql.DB, r Runner, interval time.Duration, projectDir func(int64) (string, error)) *Worker {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	w := &Worker{db: db, runner: r, interval: interval, projectDir: projectDir, stop: make(chan struct{}), done: make(chan struct{})}
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
	rowsStale, _ := w.db.QueryContext(ctx, `select id,run_id,kind,timeout_seconds,started_at,attempt,max_attempts from pipeline_steps where status='running'`)
	type staleStep struct {
		id, run               int64
		kind, started         string
		timeout, attempt, max int
	}
	var staleSteps []staleStep
	if rowsStale != nil {
		for rowsStale.Next() {
			var x staleStep
			_ = rowsStale.Scan(&x.id, &x.run, &x.kind, &x.timeout, &x.started, &x.attempt, &x.max)
			staleSteps = append(staleSteps, x)
		}
		rowsStale.Close()
	}
	for _, x := range staleSteps {
		started, err := time.Parse(time.RFC3339Nano, x.started)
		if err != nil || time.Now().UTC().Before(started.Add(time.Duration(x.timeout)*time.Second+30*time.Second)) {
			continue
		}
		if x.kind == "test" || x.kind == "build" {
			if x.attempt >= x.max {
				_, _ = w.db.ExecContext(ctx, `update pipeline_steps set status='failed',failure_class='environment_failure',output_summary='worker interrupted and retry budget exhausted',completed_at=? where id=? and status='running'`, now(), x.id)
				_, _ = w.db.ExecContext(ctx, `insert into issues(task_id,title,body,status,blocking,created_by,created_at) select task_id,'流水线恢复失败','worker interrupted and retry budget exhausted','blocking',1,'pipeline',? from pipeline_runs where id=?`, now(), x.run)
				_, _ = w.db.ExecContext(ctx, `insert into runtime_events(task_id,event_type,actor,payload_json,created_at) select task_id,'pipeline.recovery.exhausted','pipeline',?,? from pipeline_runs where id=?`, fmt.Sprintf(`{"run_id":%d,"step_id":%d}`, x.run, x.id), now(), x.run)
				w.refreshRun(x.run)
			} else {
				_, _ = w.db.ExecContext(ctx, `update pipeline_steps set status='pending',next_retry_at=? where id=? and status='running'`, now(), x.id)
			}
		}
	}
	rowsRelease, _ := w.db.QueryContext(ctx, `select ps.id,ps.run_id,pr.safe_commit,ps.timeout_seconds,ps.started_at from pipeline_steps ps join pipeline_runs pr on pr.id=ps.run_id where ps.status='running' and ps.kind='release'`)
	if rowsRelease != nil {
		for rowsRelease.Next() {
			var id, run int64
			var safe, startedRaw string
			var timeout int
			_ = rowsRelease.Scan(&id, &run, &safe, &timeout, &startedRaw)
			started, err := time.Parse(time.RFC3339Nano, startedRaw)
			if err != nil || time.Now().UTC().Before(started.Add(time.Duration(timeout)*time.Second+30*time.Second)) {
				continue
			}
			_, _ = w.db.ExecContext(ctx, `update pipeline_steps set status='failed',failure_class='environment_failure',output_summary='发布状态不确定，需要人工回滚确认',completed_at=? where id=?`, now(), id)
			_, _ = w.db.ExecContext(ctx, `insert into pipeline_rollbacks(run_id,safe_commit,status,created_at) values(?,?,'awaiting_confirmation',?) on conflict(run_id) do nothing`, run, safe, now())
			w.refreshRun(run)
		}
		rowsRelease.Close()
	}
	_, _ = w.db.ExecContext(ctx, `update pipeline_rollbacks set status='failed',last_error='rollback worker interrupted; explicit reconfirmation required',completed_at=? where status='running' and started_at<?`, now(), time.Now().UTC().Add(-1*time.Hour).Format(time.RFC3339Nano))
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
	e := w.db.QueryRowContext(ctx, `select ps.run_id,pr.project_id,ps.step_key,ps.kind,ps.command,ps.args_json,ps.artifact,ps.health_url,ps.timeout_seconds,ps.max_attempts,ps.reversible,ps.attempt from pipeline_steps ps join pipeline_runs pr on pr.id=ps.run_id where ps.id=?`, id).Scan(&s.RunID, &project, &s.Key, &s.Kind, &s.Command, &args, &s.Artifact, &s.HealthURL, &s.TimeoutSeconds, &s.MaxAttempts, &rev, &s.Attempt)
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
	if s.Kind == "build" && s.Artifact != "" && w.runHasRelease(s.RunID) {
		if e = w.ensureBackup(s.RunID, dir, s.Artifact); e != nil {
			return w.finish(id, s, Result{FailureClass: "environment_failure", Err: e})
		}
	}
	if s.Kind == "release" {
		if e = w.validateRelease(ctx, s.RunID, dir, s.Artifact); e != nil {
			return w.finish(id, s, Result{FailureClass: "environment_failure", Err: e})
		}
		var artifact string
		_ = w.db.QueryRow(`select artifact from pipeline_steps where run_id=? and kind='build'`, s.RunID).Scan(&artifact)
		expected, pathErr := safeProjectPath(dir, artifact)
		actual, bindErr := w.pm2Path(ctx, dir, s.Args[1])
		if pathErr != nil || bindErr != nil || actual != expected {
			return w.finish(id, s, Result{FailureClass: "environment_failure", Err: fmt.Errorf("pm2 executable does not match release artifact")})
		}
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
	if result.Err == nil && s.Kind == "release" {
		result.Err = checkHealth(ctx, s.HealthURL)
		if result.Err != nil {
			result.FailureClass = "environment_failure"
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
		claim, _ := w.db.ExecContext(ctx, `update pipeline_rollbacks set status='running',started_at=? where id=? and status='pending'`, now(), x.id)
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
			var backupPath, backupHash, artifact string
			e = w.db.QueryRowContext(ctx, `select pr.backup_path,pr.backup_hash,ps.id,ps.step_key,ps.kind,ps.command,ps.args_json,ps.health_url,ps.timeout_seconds,ps.max_attempts,ps.reversible,(select artifact from pipeline_steps where run_id=pr.id and kind='build' order by id desc limit 1) from pipeline_runs pr join pipeline_steps ps on ps.run_id=pr.id and ps.kind='release' where pr.id=? order by ps.id desc limit 1`, x.run).Scan(&backupPath, &backupHash, &s.ID, &s.Key, &s.Kind, &s.Command, &args, &s.HealthURL, &s.TimeoutSeconds, &s.MaxAttempts, &rev, &artifact)
			s.Reversible = rev == 1
			_ = jsonUnmarshal(args, &s.Args)
			if e == nil {
				e = restoreBackup(dir, artifact, backupPath, backupHash)
			}
			if e == nil {
				expected, pathErr := safeProjectPath(dir, artifact)
				actual, bindErr := w.pm2Path(ctx, dir, s.Args[1])
				if pathErr != nil || bindErr != nil || actual != expected {
					e = fmt.Errorf("pm2 executable does not match rollback artifact")
				}
			}
			if e == nil {
				r := w.runner.Run(ctx, dir, s)
				e = r.Err
			}
			if e == nil {
				e = checkHealth(ctx, s.HealthURL)
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
