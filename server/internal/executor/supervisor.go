package executor

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"wanxiang-agent/server/internal/auth"
	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/leases"
	"wanxiang-agent/server/internal/matching"
)

type WorkerLaunch struct {
	Input WorkerInput
	Env   map[string]string
}
type WorkerProcess interface {
	PID() int
	Wait() error
	Signal() error
	Kill() error
}
type ProcessLauncher interface {
	Launch(context.Context, WorkerLaunch) (WorkerProcess, error)
}
type SupervisorOptions struct {
	GlobalLimit                int
	ScanInterval, CloseTimeout time.Duration
}
type Supervisor struct {
	cfg                             config.Config
	db                              *sql.DB
	leases                          *leases.Service
	launcher                        ProcessLauncher
	options                         SupervisorOptions
	scanMu                          sync.Mutex
	mu                              sync.Mutex
	active                          map[int64]activeWorker
	closing                         bool
	wg                              sync.WaitGroup
	cancel                          context.CancelFunc
	done                            chan struct{}
	firstDone                       chan struct{}
	startOnce, closeOnce, firstOnce sync.Once
}
type activeWorker struct {
	agent   string
	process WorkerProcess
}

func NewSupervisor(cfg config.Config, db *sql.DB, leaseService *leases.Service, launcher ProcessLauncher, options SupervisorOptions) *Supervisor {
	if options.GlobalLimit <= 0 {
		options.GlobalLimit = 1
	}
	if options.ScanInterval <= 0 {
		options.ScanInterval = 2 * time.Second
	}
	if options.CloseTimeout <= 0 {
		options.CloseTimeout = 10 * time.Second
	}
	if launcher == nil {
		launcher = &OSProcessLauncher{cfg: cfg}
	}
	return &Supervisor{cfg: cfg, db: db, leases: leaseService, launcher: launcher, options: options, active: map[int64]activeWorker{}, done: make(chan struct{}), firstDone: make(chan struct{})}
}
func (s *Supervisor) FirstScanDone() <-chan struct{} { return s.firstDone }
func (s *Supervisor) Start() {
	s.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		go func() {
			defer close(s.done)
			_, _ = s.Scan(ctx)
			s.firstOnce.Do(func() { close(s.firstDone) })
			ticker := time.NewTicker(s.options.ScanInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					_, _ = s.Scan(ctx)
				}
			}
		}()
	})
}

func (s *Supervisor) Scan(ctx context.Context) (int, error) {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	s.mu.Lock()
	if s.closing || len(s.active) >= s.options.GlobalLimit {
		s.mu.Unlock()
		return 0, nil
	}
	slots := s.options.GlobalLimit - len(s.active)
	agentCounts := map[string]int{}
	for _, item := range s.active {
		agentCounts[item.agent]++
	}
	s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, `select ts.task_id,ts.id,ts.agent_name from task_steps ts join tasks t on t.id=ts.task_id join task_assignments ta on ta.task_id=ts.task_id and ta.step_id=ts.id and ta.agent_name=ts.agent_name join project_workspaces pw on pw.task_id=ts.task_id and pw.step_id=ts.id and pw.agent_name=ts.agent_name where t.status='workspace_ready' and ts.status='assigned' and ts.lease_id='' and ta.status='assigned' and pw.status='ready' and not exists(select 1 from workflow_edges e join task_steps dep on dep.id=e.from_step_id where e.to_step_id=ts.id and dep.status<>'completed') order by t.priority desc,ts.id limit ?`, slots*20)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type candidate struct {
		taskID, stepID int64
		agent          string
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.taskID, &item.stepID, &item.agent); err != nil {
			return 0, err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	started := 0
	for _, item := range candidates {
		definition, err := matching.LoadDefinition(s.cfg.AgentDir, item.agent)
		if err != nil {
			continue
		}
		if agentCounts[item.agent] >= definition.MaxConcurrency {
			continue
		}
		env, err := loadWorkerEnv(filepath.Join(s.cfg.AgentDir, item.agent, "env"))
		if err != nil {
			s.blockMissingConfig(ctx, item.agent)
			continue
		}
		lease, err := s.leases.Acquire(ctx, item.taskID, item.stepID, item.agent)
		if err != nil {
			continue
		}
		token, err := s.issueToken(ctx, item.agent)
		if err != nil {
			return started, err
		}
		input := WorkerInput{TaskID: item.taskID, StepID: item.stepID, AgentName: item.agent, LeaseID: lease.LeaseID, LeaseVersion: lease.LeaseVersion, ServerURL: workerServerURL(s.cfg.HTTPAddr), AgentToken: token}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		_, _ = s.db.ExecContext(ctx, `insert into executor_runs(task_id,step_id,agent_name,lease_id,lease_version,status,created_at,updated_at) values(?,?,?,?,?,'starting',?,?) on conflict(lease_id) do nothing`, item.taskID, item.stepID, item.agent, lease.LeaseID, lease.LeaseVersion, now, now)
		process, err := s.launcher.Launch(ctx, WorkerLaunch{Input: input, Env: env})
		if err != nil {
			_, _ = s.db.ExecContext(ctx, `update executor_runs set status='failed',error_summary=?,updated_at=? where lease_id=?`, Redact(err.Error()), now, lease.LeaseID)
			continue
		}
		_, _ = s.db.ExecContext(ctx, `update executor_runs set pid=?,status='running',started_at=?,updated_at=? where lease_id=?`, process.PID(), now, now, lease.LeaseID)
		s.mu.Lock()
		s.active[item.stepID] = activeWorker{agent: item.agent, process: process}
		s.mu.Unlock()
		agentCounts[item.agent]++
		started++
		s.wg.Add(1)
		go s.wait(item.stepID, lease.LeaseID, process)
		if started >= slots {
			break
		}
	}
	return started, nil
}

func (s *Supervisor) wait(stepID int64, leaseID string, process WorkerProcess) {
	defer s.wg.Done()
	err := process.Wait()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	exit := 0
	summary := ""
	if err != nil {
		exit = 1
		summary = Redact(err.Error())
	}
	_, _ = s.db.Exec(`update executor_runs set exit_code=coalesce(exit_code,?),error_summary=case when error_summary='' then ? else error_summary end,exited_at=coalesce(exited_at,?),updated_at=? where lease_id=?`, exit, summary, now, now, leaseID)
	s.mu.Lock()
	delete(s.active, stepID)
	s.mu.Unlock()
}
func (s *Supervisor) Close() {
	s.closeOnce.Do(func() {
		s.scanMu.Lock()
		s.mu.Lock()
		s.closing = true
		workers := make([]WorkerProcess, 0, len(s.active))
		for _, item := range s.active {
			workers = append(workers, item.process)
		}
		s.mu.Unlock()
		s.scanMu.Unlock()
		if s.cancel != nil {
			s.cancel()
		}
		for _, worker := range workers {
			_ = worker.Signal()
		}
		wait := make(chan struct{})
		go func() { s.wg.Wait(); close(wait) }()
		select {
		case <-wait:
		case <-time.After(s.options.CloseTimeout):
			for _, worker := range workers {
				_ = worker.Kill()
			}
			<-wait
		}
		if s.cancel != nil {
			<-s.done
		}
	})
}
func (s *Supervisor) StopRun(ctx context.Context, runID int64) error {
	var stepID int64
	if err := s.db.QueryRowContext(ctx, `select step_id from executor_runs where id=?`, runID).Scan(&stepID); err != nil {
		return err
	}
	s.mu.Lock()
	worker, ok := s.active[stepID]
	s.mu.Unlock()
	if !ok {
		return ErrRunNotActive
	}
	return worker.process.Signal()
}
func (s *Supervisor) blockMissingConfig(ctx context.Context, agent string) {
	_, _ = s.db.ExecContext(ctx, `update agent_registry set status='blocked: missing_config',last_heartbeat=? where name=?`, time.Now().UTC().Format(time.RFC3339Nano), agent)
}
func (s *Supervisor) issueToken(ctx context.Context, agent string) (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `insert into agent_tokens(agent_name,token_hash,scopes,expires_at,created_at) values(?,?,'runtime',?,?)`, agent, auth.HashSecret(token), now.Add(10*time.Minute).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	return token, err
}

func loadWorkerEnv(path string) (map[string]string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || info.Size() > 64*1024 {
		return nil, ErrMissingWorkerConfig
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, ErrMissingWorkerConfig
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 || !strings.HasPrefix(parts[0], "AGENT_") {
			return nil, ErrMissingWorkerConfig
		}
		values[parts[0]] = parts[1]
	}
	for _, key := range []string{"AGENT_PROVIDER_TYPE", "AGENT_API_KEY", "AGENT_MODEL"} {
		if strings.TrimSpace(values[key]) == "" {
			return nil, ErrMissingWorkerConfig
		}
	}
	return values, nil
}

type OSProcessLauncher struct{ cfg config.Config }

func (l *OSProcessLauncher) Launch(_ context.Context, launch WorkerLaunch) (WorkerProcess, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(launch.Input)
	if err != nil {
		reader.Close()
		writer.Close()
		return nil, err
	}
	if _, err = writer.Write(encoded); err != nil {
		reader.Close()
		writer.Close()
		return nil, err
	}
	writer.Close()
	binary, err := os.Executable()
	if err != nil {
		reader.Close()
		return nil, err
	}
	cmd := NewWorkerCommand(binary, reader, launch.Env)
	cmd.Dir = l.cfg.RootDir
	cmd.Stdout = &limitedBuffer{limit: maxRedactedBytes}
	cmd.Stderr = &limitedBuffer{limit: maxRedactedBytes}
	if err := cmd.Start(); err != nil {
		reader.Close()
		return nil, err
	}
	reader.Close()
	return &osWorkerProcess{cmd: cmd}, nil
}

type osWorkerProcess struct{ cmd *exec.Cmd }

func (p *osWorkerProcess) PID() int      { return p.cmd.Process.Pid }
func (p *osWorkerProcess) Wait() error   { return p.cmd.Wait() }
func (p *osWorkerProcess) Signal() error { return p.cmd.Process.Signal(syscall.SIGTERM) }
func (p *osWorkerProcess) Kill() error   { return p.cmd.Process.Kill() }

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := b.limit - b.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = b.Buffer.Write([]byte(Redact(string(value))))
	}
	return original, nil
}

func workerServerURL(addr string) string {
	addr = strings.TrimSpace(addr)
	if strings.HasPrefix(addr, ":") {
		return "http://127.0.0.1" + addr
	}
	if strings.HasPrefix(addr, "127.0.0.1:") || strings.HasPrefix(addr, "localhost:") {
		return "http://" + addr
	}
	return "http://127.0.0.1:8088"
}
