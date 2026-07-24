package executor

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/leases"
)

type fakeProcess struct {
	pid              int
	done             chan error
	mu               sync.Mutex
	signaled, killed bool
}

func (p *fakeProcess) PID() int    { return p.pid }
func (p *fakeProcess) Wait() error { return <-p.done }
func (p *fakeProcess) Signal() error {
	p.mu.Lock()
	p.signaled = true
	p.mu.Unlock()
	select {
	case p.done <- nil:
	default:
	}
	return nil
}
func (p *fakeProcess) Kill() error {
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
	select {
	case p.done <- context.Canceled:
	default:
	}
	return nil
}

type fakeProcessLauncher struct {
	mu        sync.Mutex
	launches  []WorkerLaunch
	processes []*fakeProcess
}

func (l *fakeProcessLauncher) Launch(_ context.Context, input WorkerLaunch) (WorkerProcess, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	p := &fakeProcess{pid: 100 + len(l.launches), done: make(chan error, 1)}
	l.launches = append(l.launches, input)
	l.processes = append(l.processes, p)
	return p, nil
}

func TestSupervisorStartsEligibleStepOnceAndUsesOwnEnv(t *testing.T) {
	cfg, db, leaseSvc, taskID, stepID := supervisorFixture(t, true)
	launcher := &fakeProcessLauncher{}
	svc := NewSupervisor(cfg, db, leaseSvc, launcher, SupervisorOptions{GlobalLimit: 1, ScanInterval: time.Hour, CloseTimeout: time.Second})
	if n, err := svc.Scan(t.Context()); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if n, err := svc.Scan(t.Context()); err != nil || n != 0 {
		t.Fatalf("second n=%d err=%v", n, err)
	}
	if len(launcher.launches) != 1 || launcher.launches[0].Input.TaskID != taskID || launcher.launches[0].Input.StepID != stepID || launcher.launches[0].Env["AGENT_API_KEY"] != "agent-private" {
		t.Fatalf("launches=%+v", launcher.launches)
	}
	svc.Close()
}

func TestSupervisorMissingAgentEnvBlocksWithoutManagerFallback(t *testing.T) {
	cfg, db, leaseSvc, _, _ := supervisorFixture(t, false)
	os.MkdirAll(filepath.Join(cfg.AgentDir, "manager"), 0o755)
	os.WriteFile(filepath.Join(cfg.AgentDir, "manager", "env"), []byte("AGENT_PROVIDER_TYPE=openai\nAGENT_API_KEY=manager-secret\nAGENT_MODEL=m\n"), 0o600)
	launcher := &fakeProcessLauncher{}
	svc := NewSupervisor(cfg, db, leaseSvc, launcher, SupervisorOptions{GlobalLimit: 1})
	if n, err := svc.Scan(t.Context()); err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if len(launcher.launches) != 0 {
		t.Fatal("worker launched")
	}
	var status string
	_ = db.QueryRow(`select status from agent_registry where name='agent-a'`).Scan(&status)
	if status != "blocked: missing_config" {
		t.Fatalf("status=%q", status)
	}
}

func TestSupervisorCloseSignalsAndWaitsForWorkers(t *testing.T) {
	cfg, db, leaseSvc, _, _ := supervisorFixture(t, true)
	launcher := &fakeProcessLauncher{}
	svc := NewSupervisor(cfg, db, leaseSvc, launcher, SupervisorOptions{GlobalLimit: 1, CloseTimeout: time.Second})
	if _, err := svc.Scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	svc.Close()
	if !launcher.processes[0].signaled {
		t.Fatal("worker not signaled")
	}
}

func TestSupervisorWaitsForDependenciesAndSkipsExistingLease(t *testing.T) {
	cfg, db, leaseSvc, taskID, stepID := supervisorFixture(t, true)
	result, err := db.Exec(`insert into task_steps(task_id,agent_name,kind,status,input,created_at) values(?,'agent-a','backend','in_progress','{}','now')`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	dependencyID, _ := result.LastInsertId()
	if _, err := db.Exec(`insert into workflow_edges(task_id,from_step_id,to_step_id,label,created_at) values(?,?,?,'depends_on','now')`, taskID, dependencyID, stepID); err != nil {
		t.Fatal(err)
	}
	launcher := &fakeProcessLauncher{}
	svc := NewSupervisor(cfg, db, leaseSvc, launcher, SupervisorOptions{GlobalLimit: 1, CloseTimeout: time.Second})
	if n, err := svc.Scan(t.Context()); err != nil || n != 0 {
		t.Fatalf("blocked n=%d err=%v", n, err)
	}
	if _, err := db.Exec(`update task_steps set status='completed' where id=?`, dependencyID); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.Scan(t.Context()); err != nil || n != 1 {
		t.Fatalf("ready n=%d err=%v", n, err)
	}
	if n, err := svc.Scan(t.Context()); err != nil || n != 0 {
		t.Fatalf("duplicate n=%d err=%v", n, err)
	}
	svc.Close()
}

func supervisorFixture(t *testing.T, withEnv bool) (config.Config, *sql.DB, *leases.Service, int64, int64) {
	t.Helper()
	files, ref, _ := fileToolsFixture(t)
	db := files.db
	_, _ = db.Exec(`delete from task_step_leases where step_id=?`, ref.StepID)
	_, _ = db.Exec(`update task_steps set status='assigned',lease_id='',lease_version=0 where id=?`, ref.StepID)
	root := t.TempDir()
	cfg, _ := config.Load(root)
	agentDir := filepath.Join(cfg.AgentDir, "agent-a")
	os.MkdirAll(agentDir, 0o755)
	os.WriteFile(filepath.Join(agentDir, "agent.yaml"), []byte("role: backend\nmax_concurrency: 1\n"), 0o644)
	if withEnv {
		os.WriteFile(filepath.Join(agentDir, "env"), []byte("AGENT_PROVIDER_TYPE=openai\nAGENT_API_KEY=agent-private\nAGENT_BASE_URL=https://example.invalid/v1\nAGENT_MODEL=test-model\n"), 0o600)
	}
	_, _ = db.Exec(`insert into agent_registry(name,role,dir,status,last_heartbeat) values('agent-a','backend',?,'configured','now') on conflict(name) do update set dir=excluded.dir,status=excluded.status`, agentDir)
	return cfg, db, files.leases.(*leases.Service), ref.TaskID, ref.StepID
}
