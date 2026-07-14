package issues

import (
	"context"
	"database/sql"
	"time"

	"wanxiang-agent/server/internal/events"
)

type Service struct {
	db  *sql.DB
	bus *events.Bus
}

func NewService(db *sql.DB, buses ...*events.Bus) *Service {
	bus := events.NewBus(db)
	if len(buses) > 0 && buses[0] != nil {
		bus = buses[0]
	}
	return &Service{db: db, bus: bus}
}

func (s *Service) Create(ctx context.Context, input CreateIssueInput) (Issue, error) {
	blocking := 0
	status := "open"
	if input.Blocking {
		blocking = 1
		status = "blocking"
	}
	res, err := s.db.ExecContext(ctx, `insert into issues(task_id,mr_id,title,body,status,blocking,created_by,created_at) values(?,?,?,?,?,?,?,?)`,
		input.TaskID, nullableMRID(input.MRID), input.Title, input.Body, status, blocking, input.CreatedBy, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return Issue{}, err
	}
	id, _ := res.LastInsertId()
	issue := Issue{ID: id, TaskID: input.TaskID, MRID: input.MRID, Title: input.Title, Body: input.Body, Status: status, Blocking: input.Blocking, CreatedBy: input.CreatedBy}
	if err := s.bus.PublishJSON(ctx, input.TaskID, "issue.created", input.CreatedBy, map[string]any{
		"issue_id": id, "task_id": input.TaskID, "mr_id": input.MRID, "title": input.Title, "status": status, "blocking": input.Blocking,
	}); err != nil {
		return Issue{}, err
	}
	return issue, nil
}

func (s *Service) HasBlockingForMR(ctx context.Context, mrID int64) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `select count(*) from issues i
		where i.blocking=1 and i.status not in ('resolved','closed')
		and (i.mr_id=? or (i.mr_id is null and i.task_id=(select task_id from merge_requests where id=?)))`, mrID, mrID).Scan(&count)
	return count > 0, err
}

func (s *Service) List(ctx context.Context, taskID *int64, limit, offset int) ([]Issue, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	query := `select id,task_id,mr_id,title,body,status,blocking,created_by from issues`
	args := []any{}
	if taskID != nil {
		query += ` where task_id=?`
		args = append(args, *taskID)
	}
	query += ` order by id desc limit ? offset ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Issue, 0)
	for rows.Next() {
		var item Issue
		var task, mr sql.NullInt64
		var blocking int
		if err := rows.Scan(&item.ID, &task, &mr, &item.Title, &item.Body, &item.Status, &blocking, &item.CreatedBy); err != nil {
			return nil, err
		}
		if task.Valid {
			item.TaskID = &task.Int64
		}
		if mr.Valid {
			item.MRID = mr.Int64
		}
		item.Blocking = blocking == 1
		result = append(result, item)
	}
	return result, rows.Err()
}

func nullableMRID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
