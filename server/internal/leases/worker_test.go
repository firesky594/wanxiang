package leases

import (
	"context"
	"testing"
	"time"
)

func TestWorkerScansImmediatelyOnStartup(t *testing.T) {
	svc, conn, clock, taskID, stepID := leaseFixture(t)
	lease, err := svc.Acquire(t.Context(), taskID, stepID, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(LeaseTTL)
	ctx, cancel := context.WithCancel(t.Context())
	worker := NewWorker(svc, time.Hour)
	done := make(chan struct{})
	go func() {
		worker.Run(ctx)
		close(done)
	}()
	select {
	case <-worker.FirstScanDone():
	case <-time.After(time.Second):
		t.Fatal("worker did not run startup scan")
	}
	cancel()
	<-done
	var status string
	_ = conn.QueryRow(`select status from task_step_leases where lease_id=?`, lease.LeaseID).Scan(&status)
	if status != string(LeaseInterrupted) {
		t.Fatalf("status=%s", status)
	}
}
