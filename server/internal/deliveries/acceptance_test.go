package deliveries

import (
	"context"
	"sync"
	"testing"

	"wanxiang-agent/server/internal/testutil"
)

func TestAcceptCompletesTaskAndIsIdempotent(t *testing.T) {
	db := testutil.OpenDB(t)
	taskID, n := deliveryFixture(t, db)
	svc := NewService(db, nil)
	snap, _ := svc.BuildSnapshot(context.Background(), n)
	in := DecisionInput{Decision: "accepted", Comment: "通过", IdempotencyKey: "accept-1", CreatedBy: "admin"}
	first, err := svc.Decide(context.Background(), snap.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Decide(context.Background(), snap.ID, in)
	if err != nil || second.Decision.ID != first.Decision.ID {
		t.Fatalf("idempotent %#v %v", second, err)
	}
	var status string
	_ = db.QueryRow(`select status from tasks where id=?`, taskID).Scan(&status)
	if status != "completed" {
		t.Fatalf("status=%s", status)
	}
}

func TestRevisionCreatesVersionedReworkWithoutChangingSteps(t *testing.T) {
	db := testutil.OpenDB(t)
	_, n := deliveryFixture(t, db)
	svc := NewService(db, nil)
	snap, _ := svc.BuildSnapshot(context.Background(), n)
	result, err := svc.Decide(context.Background(), snap.ID, DecisionInput{Decision: "revision_requested", Comment: "补充移动端", IdempotencyKey: "rev-1", CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ReworkRound == nil || result.ReworkRound.PlanVersion != 2 || result.TaskStatus != "rework_planning" {
		t.Fatalf("result=%#v", result)
	}
	var oldCount int
	_ = db.QueryRow(`select count(*) from task_steps where plan_version=1 and status='completed'`).Scan(&oldCount)
	if oldCount != 1 {
		t.Fatalf("old=%d", oldCount)
	}
}

func TestDecisionValidationAndConcurrency(t *testing.T) {
	db := testutil.OpenDB(t)
	_, n := deliveryFixture(t, db)
	svc := NewService(db, nil)
	snap, _ := svc.BuildSnapshot(context.Background(), n)
	if _, err := svc.Decide(context.Background(), snap.ID, DecisionInput{Decision: "rejected", IdempotencyKey: "x", CreatedBy: "admin"}); err != ErrDecisionCommentRequired {
		t.Fatalf("err=%v", err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, key := range []string{"a", "b"} {
		wg.Add(1)
		go func(k string) {
			defer wg.Done()
			_, err := svc.Decide(context.Background(), snap.ID, DecisionInput{Decision: "accepted", IdempotencyKey: k, CreatedBy: "admin"})
			errs <- err
		}(key)
	}
	wg.Wait()
	close(errs)
	success := 0
	closed := 0
	for err := range errs {
		if err == nil {
			success++
		} else if err == ErrAcceptanceClosed || err == ErrStaleSnapshot {
			closed++
		}
	}
	if success != 1 || closed != 1 {
		t.Fatalf("success=%d closed=%d", success, closed)
	}
}
