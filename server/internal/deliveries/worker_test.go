package deliveries

import (
	"context"
	"testing"
	"time"

	"wanxiang-agent/server/internal/testutil"
)

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
