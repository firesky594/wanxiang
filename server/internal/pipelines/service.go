package pipelines

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

type Service struct{ db *sql.DB }

func NewService(db *sql.DB) *Service { return &Service{db: db} }
func (s *Service) Start(ctx context.Context, in StartInput) (Run, error) {
	if Validate(in.Definition) != nil || in.ProjectID < 1 || in.IdempotencyKey == "" || in.RequestedBy == "" {
		return Run{}, ErrInvalidDefinition
	}
	if r, e := s.byKey(ctx, in.IdempotencyKey); e == nil {
		return r, nil
	}
	raw, _ := json.Marshal(in.Definition)
	sum := sha256.Sum256(raw)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return Run{}, e
	}
	defer tx.Rollback()
	res, e := tx.ExecContext(ctx, `insert into pipeline_runs(project_id,task_id,definition_json,definition_hash,status,safe_commit,idempotency_key,requested_by,created_at) values(?,?,?,?, 'pending',?,?,?,?)`, in.ProjectID, in.TaskID, string(raw), hex.EncodeToString(sum[:]), in.SafeCommit, in.IdempotencyKey, in.RequestedBy, now)
	if e != nil {
		return Run{}, e
	}
	id, _ := res.LastInsertId()
	for _, st := range in.Definition.Steps {
		args, _ := json.Marshal(st.Args)
		status := "pending"
		if requiresConfirmation(st.Kind) {
			status = "awaiting_confirmation"
		}
		_, e = tx.ExecContext(ctx, `insert into pipeline_steps(run_id,step_key,kind,command,args_json,timeout_seconds,max_attempts,reversible,status) values(?,?,?,?,?,?,?,?,?)`, id, st.ID, st.Kind, st.Command, string(args), st.TimeoutSeconds, st.MaxAttempts, boolInt(st.Reversible), status)
		if e != nil {
			return Run{}, e
		}
	}
	if e = tx.Commit(); e != nil {
		return Run{}, e
	}
	return s.Get(ctx, id)
}
func (s *Service) Confirm(ctx context.Context, runID int64, stepKey, actor string) (Step, error) {
	if actor == "" {
		return Step{}, ErrConfirmationRequired
	}
	res, e := s.db.ExecContext(ctx, `update pipeline_steps set status='pending',confirmed_by=?,confirmed_at=? where run_id=? and step_key=? and status='awaiting_confirmation'`, actor, time.Now().UTC().Format(time.RFC3339Nano), runID, stepKey)
	if e != nil {
		return Step{}, e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return Step{}, ErrConfirmationRequired
	}
	return s.getStep(ctx, runID, stepKey)
}
func (s *Service) Get(ctx context.Context, id int64) (Run, error) {
	var r Run
	var task sql.NullInt64
	e := s.db.QueryRowContext(ctx, `select id,project_id,task_id,status,safe_commit,artifact_hash,definition_hash,requested_by,created_at,last_error from pipeline_runs where id=?`, id).Scan(&r.ID, &r.ProjectID, &task, &r.Status, &r.SafeCommit, &r.ArtifactHash, &r.DefinitionHash, &r.RequestedBy, &r.CreatedAt, &r.LastError)
	if errors.Is(e, sql.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	if e != nil {
		return Run{}, e
	}
	if task.Valid {
		r.TaskID = &task.Int64
	}
	rows, e := s.db.QueryContext(ctx, `select id,run_id,step_key,kind,command,args_json,timeout_seconds,max_attempts,reversible,status,attempt,failure_class,output_summary,confirmed_by from pipeline_steps where run_id=? order by id`, id)
	if e != nil {
		return Run{}, e
	}
	defer rows.Close()
	for rows.Next() {
		var x Step
		var args string
		var rev int
		if e = rows.Scan(&x.ID, &x.RunID, &x.Key, &x.Kind, &x.Command, &args, &x.TimeoutSeconds, &x.MaxAttempts, &rev, &x.Status, &x.Attempt, &x.FailureClass, &x.OutputSummary, &x.ConfirmedBy); e != nil {
			return Run{}, e
		}
		x.Reversible = rev == 1
		_ = json.Unmarshal([]byte(args), &x.Args)
		r.Steps = append(r.Steps, x)
	}
	return r, rows.Err()
}
func (s *Service) List(ctx context.Context) ([]Run, error) {
	rows, e := s.db.QueryContext(ctx, `select id from pipeline_runs order by id desc limit 100`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		r, e := s.Get(ctx, id)
		if e == nil {
			out = append(out, r)
		}
	}
	return out, rows.Err()
}
func (s *Service) byKey(ctx context.Context, key string) (Run, error) {
	var id int64
	e := s.db.QueryRowContext(ctx, `select id from pipeline_runs where idempotency_key=?`, key).Scan(&id)
	if e != nil {
		return Run{}, e
	}
	return s.Get(ctx, id)
}
func (s *Service) getStep(ctx context.Context, run int64, key string) (Step, error) {
	r, e := s.Get(ctx, run)
	if e != nil {
		return Step{}, e
	}
	for _, x := range r.Steps {
		if x.Key == key {
			return x, nil
		}
	}
	return Step{}, ErrNotFound
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
