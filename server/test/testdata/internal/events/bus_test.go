package events

import (
	"context"
	"encoding/json"
	"testing"

	"wanxiang-agent/server/internal/testutil"
)

func TestListReturnsPersistedTaskEventsNewestFirst(t *testing.T) {
	conn := testutil.OpenDB(t)
	bus := NewBus(conn)
	taskID := int64(7)
	for _, kind := range []string{"first", "second"} {
		if err := bus.PublishJSON(context.Background(), &taskID, kind, "manager", map[string]any{"kind": kind}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := bus.List(context.Background(), &taskID, 10, 0)
	if err != nil || len(got) != 2 || got[0].Type != "second" {
		t.Fatalf("events=%+v err=%v", got, err)
	}
}

func TestPublishStoresAndBroadcastsEvent(t *testing.T) {
	conn := testutil.OpenDB(t)
	bus := NewBus(conn)
	ch, unsubscribe := bus.Subscribe()
	defer unsubscribe()

	err := bus.Publish(context.Background(), Event{
		Type:    "agent.heartbeat",
		Actor:   "manager",
		Payload: json.RawMessage(`{"status":"online"}`),
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	got := <-ch
	if got.Type != "agent.heartbeat" || got.Actor != "manager" {
		t.Fatalf("event=%+v", got)
	}
	var count int
	if err := conn.QueryRow(`select count(*) from runtime_events`).Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != 1 {
		t.Fatalf("count=%d", count)
	}
}
