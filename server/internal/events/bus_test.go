package events

import (
	"context"
	"encoding/json"
	"testing"

	"wanxiang-agent/server/internal/testutil"
)

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
