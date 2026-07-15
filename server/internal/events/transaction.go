package events

import (
	"context"
	"database/sql"
	"time"
)

func InsertTx(ctx context.Context, tx *sql.Tx, event Event) (Event, error) {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	res, err := tx.ExecContext(ctx, `insert into runtime_events(task_id,event_type,actor,payload_json,created_at) values(?,?,?,?,?)`,
		event.TaskID, event.Type, event.Actor, string(event.Payload), event.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return Event{}, err
	}
	event.ID, err = res.LastInsertId()
	return event, err
}
