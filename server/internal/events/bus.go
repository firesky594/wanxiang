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
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	event, err = InsertTx(ctx, tx, event)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	b.Notify(event)
	return nil
}

func (b *Bus) Notify(event Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- event:
		default:
		}
	}
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

func (b *Bus) List(ctx context.Context, taskID *int64, limit, offset int) ([]Event, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	query := `select id,task_id,event_type,actor,payload_json,created_at from runtime_events`
	args := []any{}
	if taskID != nil {
		query += ` where task_id=?`
		args = append(args, *taskID)
	}
	query += ` order by id desc limit ? offset ?`
	args = append(args, limit, offset)
	rows, err := b.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Event, 0)
	for rows.Next() {
		var item Event
		var id sql.NullInt64
		var payload, created string
		if err := rows.Scan(&item.ID, &id, &item.Type, &item.Actor, &payload, &created); err != nil {
			return nil, err
		}
		if id.Valid {
			item.TaskID = &id.Int64
		}
		item.Payload = json.RawMessage(payload)
		item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		result = append(result, item)
	}
	return result, rows.Err()
}
