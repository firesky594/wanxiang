package deliveries

import (
	"context"
	"strings"
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

func TestDecisionIdempotencyKeyCannotCrossSnapshotOrActor(t *testing.T) {
	db := testutil.OpenDB(t)
	_, n := deliveryFixture(t, db)
	svc := NewService(db, nil)
	snap, _ := svc.BuildSnapshot(context.Background(), n)
	_, err := svc.Decide(context.Background(), snap.ID, DecisionInput{Decision: "accepted", IdempotencyKey: "shared", CreatedBy: "admin-a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Decide(context.Background(), snap.ID+1, DecisionInput{Decision: "accepted", IdempotencyKey: "shared", CreatedBy: "admin-b"}); err == nil {
		t.Fatal("cross-snapshot/actor idempotency key was accepted")
	}
}

func TestDecisionCommentIsScrubbedBeforePersistenceAndResponse(t *testing.T) {
	db := testutil.OpenDB(t)
	_, n := deliveryFixture(t, db)
	svc := NewService(db, nil)
	snap, _ := svc.BuildSnapshot(context.Background(), n)
	result, err := svc.Decide(context.Background(), snap.ID, DecisionInput{Decision: "revision_requested", Comment: "token=super-secret", IdempotencyKey: "scrub", CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision.Comment != "[REDACTED]" {
		t.Fatalf("comment=%q", result.Decision.Comment)
	}
	detail, _ := svc.Detail(context.Background(), snap.ID)
	if detail.Decisions[0].Comment != "[REDACTED]" {
		t.Fatalf("detail=%q", detail.Decisions[0].Comment)
	}
}

func TestScrubCoversCredentialFormats(t *testing.T) {
	for _, value := range []string{
		"Authorization=Basic abc123", "AWS_ACCESS_KEY_ID=AKIAEXAMPLE", "https://example.test?a=1&access_token=secret",
		"eyJabcdefghijk.abcdefghijk.abcdefghijk",
	} {
		if got := scrub(value); strings.Contains(got, "abc123") || strings.Contains(got, "AKIAEXAMPLE") || strings.Contains(got, "secret") || strings.Contains(got, "eyJ") {
			t.Fatalf("not scrubbed: %q -> %q", value, got)
		}
	}
}

func TestDecisionDoesNotSplitTaskAndSnapshotState(t *testing.T) {
	db := testutil.OpenDB(t)
	taskID, n := deliveryFixture(t, db)
	svc := NewService(db, nil)
	snap, _ := svc.BuildSnapshot(context.Background(), n)
	_, _ = db.Exec(`update tasks set status='blocked' where id=?`, taskID)
	if _, err := svc.Decide(context.Background(), snap.ID, DecisionInput{Decision: "accepted", IdempotencyKey: "blocked", CreatedBy: "admin"}); err == nil {
		t.Fatal("decision succeeded for task outside awaiting_acceptance")
	}
	var status string
	_ = db.QueryRow(`select status from delivery_snapshots where id=?`, snap.ID).Scan(&status)
	if status != "awaiting_acceptance" {
		t.Fatalf("snapshot status=%s", status)
	}
}
