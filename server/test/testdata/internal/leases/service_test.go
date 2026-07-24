package leases

import (
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/testutil"
	"wanxiang-agent/server/internal/workspaces"
)

func TestAcquireRequiresReadyOwnedWorkspaceAndIsIdempotent(t *testing.T) {
	svc, conn, clock, taskID, stepID := leaseFixture(t)

	lease, err := svc.Acquire(t.Context(), taskID, stepID, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	if lease.LeaseID == "" || lease.LeaseVersion != 1 || lease.Status != LeaseActive {
		t.Fatalf("lease=%+v", lease)
	}
	if !lease.ExpiresAt.Equal(clock.Now().Add(LeaseTTL)) {
		t.Fatalf("expires=%s", lease.ExpiresAt)
	}
	repeated, err := svc.Acquire(t.Context(), taskID, stepID, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	if repeated.LeaseID != lease.LeaseID || repeated.LeaseVersion != lease.LeaseVersion {
		t.Fatalf("repeat created another lease: first=%+v repeated=%+v", lease, repeated)
	}
	var count int
	_ = conn.QueryRow(`select count(*) from task_step_leases where step_id=?`, stepID).Scan(&count)
	if count != 1 {
		t.Fatalf("lease history count=%d", count)
	}

	if _, err := svc.Acquire(t.Context(), taskID, stepID, "agent-b"); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong owner err=%v", err)
	}
	if _, err := conn.Exec(`update project_workspaces set status='failed' where step_id=?`, stepID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`update task_steps set lease_id='',lease_version=0 where id=?`, stepID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Acquire(t.Context(), taskID, stepID, "agent-a"); !errors.Is(err, ErrConflict) {
		t.Fatalf("non-ready workspace err=%v", err)
	}
}

func TestAcquireConcurrentCallsCreateOneActiveLease(t *testing.T) {
	svc, conn, _, taskID, stepID := leaseFixture(t)
	const workers = 12
	leases := make(chan Lease, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, err := svc.Acquire(t.Context(), taskID, stepID, "agent-a")
			leases <- lease
			errs <- err
		}()
	}
	wg.Wait()
	close(leases)
	close(errs)
	var leaseID string
	for err := range errs {
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
	}
	for lease := range leases {
		if leaseID == "" {
			leaseID = lease.LeaseID
		}
		if lease.LeaseID != leaseID {
			t.Fatalf("different lease ids: %q and %q", leaseID, lease.LeaseID)
		}
	}
	var active int
	_ = conn.QueryRow(`select count(*) from task_step_leases where step_id=? and status='active'`, stepID).Scan(&active)
	if active != 1 {
		t.Fatalf("active leases=%d", active)
	}
}

func TestHeartbeatRenewsOnlyExactActiveLease(t *testing.T) {
	svc, conn, clock, taskID, stepID := leaseFixture(t)
	lease, err := svc.Acquire(t.Context(), taskID, stepID, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(HeartbeatInterval)
	renewed, err := svc.Heartbeat(t.Context(), lease.LeaseRef)
	if err != nil {
		t.Fatal(err)
	}
	if !renewed.ExpiresAt.Equal(clock.Now().Add(LeaseTTL)) || renewed.LastHeartbeatAt == nil || !renewed.LastHeartbeatAt.Equal(clock.Now()) {
		t.Fatalf("renewed=%+v now=%s", renewed, clock.Now())
	}

	probes := []LeaseRef{lease.LeaseRef}
	probes = append(probes,
		LeaseRef{TaskID: taskID, StepID: stepID, AgentName: "agent-b", LeaseID: lease.LeaseID, LeaseVersion: lease.LeaseVersion},
		LeaseRef{TaskID: taskID, StepID: stepID, AgentName: "agent-a", LeaseID: "old", LeaseVersion: lease.LeaseVersion},
		LeaseRef{TaskID: taskID, StepID: stepID, AgentName: "agent-a", LeaseID: lease.LeaseID, LeaseVersion: lease.LeaseVersion + 1},
		LeaseRef{TaskID: taskID + 1, StepID: stepID, AgentName: "agent-a", LeaseID: lease.LeaseID, LeaseVersion: lease.LeaseVersion},
		LeaseRef{TaskID: taskID, StepID: stepID + 1, AgentName: "agent-a", LeaseID: lease.LeaseID, LeaseVersion: lease.LeaseVersion},
	)
	for i, probe := range probes[1:] {
		if _, err := svc.Heartbeat(t.Context(), probe); !errors.Is(err, ErrConflict) {
			t.Fatalf("probe %d err=%v", i, err)
		}
	}
	if _, err := conn.Exec(`update task_step_leases set status='frozen' where lease_id=?`, lease.LeaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Heartbeat(t.Context(), lease.LeaseRef); !errors.Is(err, ErrConflict) {
		t.Fatalf("frozen err=%v", err)
	}
	if _, err := conn.Exec(`update task_step_leases set status='active' where lease_id=?`, lease.LeaseID); err != nil {
		t.Fatal(err)
	}
	clock.Advance(LeaseTTL + time.Nanosecond)
	if _, err := svc.Heartbeat(t.Context(), lease.LeaseRef); !errors.Is(err, ErrConflict) {
		t.Fatalf("expired err=%v", err)
	}
}

func TestAuthorizeRequiresLeaseAndWorkspaceScope(t *testing.T) {
	svc, _, _, taskID, stepID := leaseFixture(t)
	lease, err := svc.Acquire(t.Context(), taskID, stepID, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Authorize(t.Context(), lease.LeaseRef, "src/main.go"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../secret", "/tmp/secret", "docs/readme.md"} {
		if err := svc.Authorize(t.Context(), lease.LeaseRef, path); !errors.Is(err, ErrConflict) {
			t.Fatalf("path %q err=%v", path, err)
		}
	}
	old := lease.LeaseRef
	old.LeaseVersion++
	if err := svc.Authorize(t.Context(), old, "src/main.go"); !errors.Is(err, ErrConflict) {
		t.Fatalf("old lease err=%v", err)
	}
}

func leaseFixture(t *testing.T) (*Service, *sql.DB, *FakeClock, int64, int64) {
	t.Helper()
	conn := testutil.OpenDB(t)
	result, err := conn.Exec(`insert into projects(slug,dir,status,remote_url,created_at) values('lease-demo','/tmp/lease-demo','created','','now')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := result.LastInsertId()
	result, err = conn.Exec(`insert into tasks(project_id,title,description,status,created_at) values(?,'lease','test','workspace_ready','now')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := result.LastInsertId()
	result, err = conn.Exec(`insert into task_steps(task_id,agent_name,kind,status,input,created_at) values(?,'agent-a','backend','assigned','{}','now')`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ := result.LastInsertId()
	if _, err = conn.Exec(`insert into task_assignments(task_id,step_id,agent_name,status,decision_id,created_at) values(?,?,'agent-a','assigned',1,'now')`, taskID, stepID); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(`insert into project_workspaces(project_id,task_id,step_id,assignment_id,agent_name,branch_name,worktree_path,base_commit,provision_commit,write_scope_json,metadata_hash,status,created_at,updated_at) values(?,?,?,1,'agent-a','agent/agent-a/lease','/tmp/lease-demo-worktree','base','base','["src"]','hash','ready','now','now')`, projectID, taskID, stepID); err != nil {
		t.Fatal(err)
	}
	clock := NewFakeClock(time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC))
	workspaceService := workspaces.NewService(config.Config{}, conn, nil)
	return NewService(conn, clock, workspaceService), conn, clock, taskID, stepID
}
