package events

import (
	"context"
	"encoding/json"
	"testing"

	"wanxiang-agent/server/internal/testutil"
)

func TestInsertTxRollsBackAndNotifyDoesNotPersistTwice(t *testing.T) {
	db := testutil.OpenDB(t)
	bus := NewBus(db)
	taskID := int64(7)
	event := Event{TaskID: &taskID, Type: "mr.created", Actor: "worker", Payload: json.RawMessage(`{"mr_id":1}`)}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	inserted, err := InsertTx(t.Context(), tx, event)
	if err != nil {
		t.Fatal(err)
	}
	if inserted.ID == 0 {
		t.Fatal("inserted event has no id")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`select count(*) from runtime_events`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback count=%d err=%v", count, err)
	}
	tx, _ = db.BeginTx(context.Background(), nil)
	inserted, err = InsertTx(t.Context(), tx, event)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	ch, cancel := bus.Subscribe()
	defer cancel()
	bus.Notify(inserted)
	if got := <-ch; got.ID != inserted.ID {
		t.Fatalf("notified id=%d want=%d", got.ID, inserted.ID)
	}
	if err := db.QueryRow(`select count(*) from runtime_events`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("notify persisted again count=%d err=%v", count, err)
	}
}
