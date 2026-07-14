package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"time"
)

type Event struct {
	ID        int64           `json:"id"`
	TaskID    *int64          `json:"task_id,omitempty"`
	Type      string          `json:"type"`
	Actor     string          `json:"actor"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

type Bus struct {
	db   *sql.DB
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func NewBus(db *sql.DB) *Bus {
	return &Bus{db: db, subs: map[chan Event]struct{}{}}
}

func (b *Bus) Publish(ctx context.Context, event Event) error {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	res, err := b.db.ExecContext(ctx, `insert into runtime_events(task_id,event_type,actor,payload_json,created_at) values(?,?,?,?,?)`,
		event.TaskID, event.Type, event.Actor, string(event.Payload), event.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	event.ID, _ = res.LastInsertId()
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- event:
		default:
		}
	}
	return nil
}

func (b *Bus) PublishJSON(ctx context.Context, taskID *int64, eventType, actor string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return b.Publish(ctx, Event{TaskID: taskID, Type: eventType, Actor: actor, Payload: encoded})
}

func (b *Bus) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 32)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		close(ch)
		b.mu.Unlock()
	}
}
