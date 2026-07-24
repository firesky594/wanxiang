package leases

import (
	"errors"
	"testing"
	"time"
)

func TestFreezeAndUnfreezeRotateLeaseAndRevokeWrites(t *testing.T) {
	svc, conn, _, taskID, stepID := leaseFixture(t)
	lease, err := svc.Acquire(t.Context(), taskID, stepID, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.FreezeStep(t.Context(), taskID, stepID, "admin", "人工检查"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Authorize(t.Context(), lease.LeaseRef, "src/main.go"); !errors.Is(err, ErrConflict) {
		t.Fatalf("frozen old lease err=%v", err)
	}
	rotated, err := svc.UnfreezeStep(t.Context(), taskID, stepID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if rotated.LeaseID == lease.LeaseID || rotated.LeaseVersion != lease.LeaseVersion+1 || rotated.Status != LeaseActive {
		t.Fatalf("rotated=%+v old=%+v", rotated, lease)
	}
	if err := svc.Authorize(t.Context(), lease.LeaseRef, "src/main.go"); !errors.Is(err, ErrConflict) {
		t.Fatalf("old lease revived err=%v", err)
	}
	var audits int
	_ = conn.QueryRow(`select count(*) from audit_logs where target=? and action in ('lease.freeze','lease.unfreeze')`, stepTarget(taskID, stepID)).Scan(&audits)
	if audits != 2 {
		t.Fatalf("audits=%d", audits)
	}
}

func TestExtendResumeDeadlineOnlyForInterruptedLease(t *testing.T) {
	svc, conn, clock, taskID, stepID := leaseFixture(t)
	lease, _ := svc.Acquire(t.Context(), taskID, stepID, "agent-a")
	if _, err := svc.ExtendResumeDeadline(t.Context(), lease.LeaseRef, clock.Now().Add(10*time.Minute), "admin"); !errors.Is(err, ErrConflict) {
		t.Fatalf("active extension err=%v", err)
	}
	clock.Advance(LeaseTTL)
	_, _ = svc.InterruptExpired(t.Context())
	deadline := clock.Now().Add(20 * time.Minute)
	extended, err := svc.ExtendResumeDeadline(t.Context(), lease.LeaseRef, deadline, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if extended.ResumeDeadline == nil || !extended.ResumeDeadline.Equal(deadline) {
		t.Fatalf("extended=%+v", extended)
	}
	var audits int
	_ = conn.QueryRow(`select count(*) from audit_logs where target=? and action='lease.extend_resume_deadline'`, stepTarget(taskID, stepID)).Scan(&audits)
	if audits != 1 {
		t.Fatalf("audits=%d", audits)
	}
}
