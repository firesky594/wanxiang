package deliveries

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wanxiang-agent/server/internal/testutil"
)

func TestReworkWorkerRetriesTemporaryProviderFailure(t *testing.T) {
	db := testutil.OpenDB(t)
	_, n := deliveryFixture(t, db)
	svc := NewService(db, nil)
	snap, _ := svc.BuildSnapshot(context.Background(), n)
	result, err := svc.Decide(context.Background(), snap.ID, DecisionInput{Decision: "revision_requested", Comment: "retry", IdempotencyKey: "retry", CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	w := NewWorker(db, svc, time.Hour, func(context.Context, int64, int64, string) error { return errors.New("provider timeout") })
	_ = w.Scan(context.Background())
	var status string
	var retry any
	_ = db.QueryRow(`select status,next_retry_at from rework_rounds where id=?`, result.ReworkRound.ID).Scan(&status, &retry)
	if status != "planning" || retry == nil {
		t.Fatalf("status=%s retry=%#v", status, retry)
	}
}

func TestWorkerConsumesPendingAndRecordsRetry(t *testing.T) {
	db := testutil.OpenDB(t)
	_, n := deliveryFixture(t, db)
	svc := NewService(db, nil)
	w := NewWorker(db, svc, 5*time.Millisecond)
	w.Start()
	defer w.Close()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var count int
		_ = db.QueryRow(`select count(*) from delivery_snapshots`).Scan(&count)
		if count == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("snapshot not created")
	_ = n
}

func TestReworkWorkerClaimsOnceAndRecoversCommittedPlan(t *testing.T) {
	db := testutil.OpenDB(t)
	_, n := deliveryFixture(t, db)
	svc := NewService(db, nil)
	snap, _ := svc.BuildSnapshot(context.Background(), n)
	result, err := svc.Decide(context.Background(), snap.ID, DecisionInput{Decision: "revision_requested", Comment: "revise", IdempotencyKey: "rw", CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	work := func(context.Context, int64, int64, string) error {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return nil
	}
	w1, w2 := NewWorker(db, svc, time.Hour, work), NewWorker(db, svc, time.Hour, work)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = w1.Scan(context.Background()) }()
	go func() { defer wg.Done(); _ = w2.Scan(context.Background()) }()
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
	_, _ = db.Exec(`update rework_rounds set status='planning' where id=?`, result.ReworkRound.ID)
	_, _ = db.Exec(`update tasks set status='planned' where id=?`, result.ReworkRound.TaskID)
	_, _ = db.Exec(`update task_plan_versions set status='planned' where task_id=? and version=?`, result.ReworkRound.TaskID, result.ReworkRound.PlanVersion)
	calls.Store(0)
	_ = w1.Scan(context.Background())
	var status string
	_ = db.QueryRow(`select status from rework_rounds where id=?`, result.ReworkRound.ID).Scan(&status)
	if status != "planned" || calls.Load() != 0 {
		t.Fatalf("status=%s calls=%d", status, calls.Load())
	}
}

func TestWorkerLeavesNotReadyNotificationRecoverable(t *testing.T) {
	db := testutil.OpenDB(t)
	_, n := deliveryFixture(t, db)
	_, _ = db.Exec(`update task_steps set status='in_progress'`)
	svc := NewService(db, nil)
	_ = NewWorker(db, svc, time.Hour).Scan(context.Background())
	var status, last string
	var retry any
	_ = db.QueryRow(`select status,last_error,next_retry_at from manager_notifications where id=?`, n).Scan(&status, &last, &retry)
	if status != "pending" || last == "" || retry == nil {
		t.Fatalf("%s %q %#v", status, last, retry)
	}
}
