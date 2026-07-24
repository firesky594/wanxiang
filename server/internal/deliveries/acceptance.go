package deliveries

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Decide 记录交付验收决定并驱动后续状态。
func (s *Service) Decide(ctx context.Context, snapshotID int64, in DecisionInput) (DecisionResult, error) {
	if in.Decision != "accepted" && in.Decision != "rejected" && in.Decision != "revision_requested" {
		return DecisionResult{}, errors.New("invalid_decision")
	}
	in.Comment = strings.TrimSpace(in.Comment)
	in.Comment = scrub(in.Comment)
	in.IdempotencyKey = strings.TrimSpace(in.IdempotencyKey)
	if (in.Decision == "rejected" || in.Decision == "revision_requested") && in.Comment == "" {
		return DecisionResult{}, ErrDecisionCommentRequired
	}
	if len(in.Comment) > 16*1024 || in.IdempotencyKey == "" {
		return DecisionResult{}, errors.New("invalid_decision")
	}
	if existing, err := s.decisionByKey(ctx, in.IdempotencyKey); err == nil {
		if existing.SnapshotID != snapshotID || existing.Decision != in.Decision || existing.Comment != in.Comment || existing.CreatedBy != in.CreatedBy {
			return DecisionResult{}, errors.New("idempotency_conflict")
		}
		return s.resultForDecision(ctx, existing)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DecisionResult{}, err
	}
	defer tx.Rollback()
	var taskID int64
	var status string
	if err = tx.QueryRowContext(ctx, `select task_id,status from delivery_snapshots where id=?`, snapshotID).Scan(&taskID, &status); errors.Is(err, sql.ErrNoRows) {
		return DecisionResult{}, ErrNotFound
	}
	if err != nil {
		return DecisionResult{}, err
	}
	if status == "accepted" {
		return DecisionResult{}, ErrAcceptanceClosed
	}
	if status != "awaiting_acceptance" {
		return DecisionResult{}, ErrStaleSnapshot
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `update delivery_snapshots set status=? where id=? and status='awaiting_acceptance'`, in.Decision, snapshotID)
	if err != nil {
		return DecisionResult{}, err
	}
	affected, _ := res.RowsAffected()
	if affected != 1 {
		return DecisionResult{}, ErrStaleSnapshot
	}
	createdBy := in.CreatedBy
	if createdBy == "" {
		createdBy = "admin"
	}
	res, err = tx.ExecContext(ctx, `insert into acceptance_decisions(snapshot_id,task_id,decision,comment,idempotency_key,created_by,created_at) values(?,?,?,?,?,?,?)`, snapshotID, taskID, in.Decision, in.Comment, in.IdempotencyKey, createdBy, now)
	if err != nil {
		return DecisionResult{}, err
	}
	decisionID, _ := res.LastInsertId()
	decision := AcceptanceDecision{ID: decisionID, SnapshotID: snapshotID, TaskID: taskID, Decision: in.Decision, Comment: in.Comment, CreatedBy: createdBy, CreatedAt: now}
	result := DecisionResult{Decision: decision}
	if in.Decision == "accepted" {
		var taskResult sql.Result
		taskResult, err = tx.ExecContext(ctx, `update tasks set status='completed' where id=? and status='awaiting_acceptance'`, taskID)
		if err == nil {
			if changed, _ := taskResult.RowsAffected(); changed != 1 {
				err = ErrStaleSnapshot
			}
		}
		result.TaskStatus = "completed"
	} else {
		var round, version int64
		_ = tx.QueryRowContext(ctx, `select coalesce(max(round),0)+1 from rework_rounds where task_id=?`, taskID).Scan(&round)
		_ = tx.QueryRowContext(ctx, `select coalesce(max(version),1)+1 from task_plan_versions where task_id=?`, taskID).Scan(&version)
		_, err = tx.ExecContext(ctx, `insert into task_plan_versions(task_id,version,source_snapshot_id,source_decision_id,status,summary,created_at) values(?,?,?,?, 'planning','',?)`, taskID, version, snapshotID, decisionID, now)
		if err == nil {
			res, err = tx.ExecContext(ctx, `insert into rework_rounds(task_id,source_snapshot_id,decision_id,round,plan_version,reason,status,checkpoint_json,created_by,created_at) values(?,?,?,?,?,?,'planning','{}',?,?)`, taskID, snapshotID, decisionID, round, version, in.Comment, createdBy, now)
		}
		if err == nil {
			rid, _ := res.LastInsertId()
			result.ReworkRound = &ReworkRound{ID: rid, TaskID: taskID, SourceSnapshotID: snapshotID, DecisionID: decisionID, Round: round, PlanVersion: version, Reason: in.Comment, Status: "planning", CreatedBy: createdBy, CreatedAt: now}
			var taskResult sql.Result
			taskResult, err = tx.ExecContext(ctx, `update tasks set status='rework_planning' where id=? and status='awaiting_acceptance'`, taskID)
			if err == nil {
				if changed, _ := taskResult.RowsAffected(); changed != 1 {
					err = ErrStaleSnapshot
				}
			}
		}
		result.TaskStatus = "rework_planning"
	}
	if err != nil {
		return DecisionResult{}, err
	}
	payload := `{"snapshot_id":` + itoa(snapshotID) + `,"decision_id":` + itoa(decisionID) + `,"decision":"` + in.Decision + `"}`
	_, err = tx.ExecContext(ctx, `insert into runtime_events(task_id,event_type,actor,payload_json,created_at) values(?,'delivery.decision.created',?,?,?)`, taskID, createdBy, payload, now)
	if err != nil {
		return DecisionResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return DecisionResult{}, err
	}
	return result, nil
}

func itoa(v int64) string {
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = digits[v%10]
		v /= 10
	}
	return string(b[i:])
}

func (s *Service) decisionByKey(ctx context.Context, key string) (AcceptanceDecision, error) {
	var d AcceptanceDecision
	err := s.db.QueryRowContext(ctx, `select id,snapshot_id,task_id,decision,comment,created_by,created_at from acceptance_decisions where idempotency_key=?`, key).Scan(&d.ID, &d.SnapshotID, &d.TaskID, &d.Decision, &d.Comment, &d.CreatedBy, &d.CreatedAt)
	return d, err
}
func (s *Service) resultForDecision(ctx context.Context, d AcceptanceDecision) (DecisionResult, error) {
	result := DecisionResult{Decision: d}
	_ = s.db.QueryRowContext(ctx, `select status from tasks where id=?`, d.TaskID).Scan(&result.TaskStatus)
	var r ReworkRound
	err := s.db.QueryRowContext(ctx, `select id,task_id,source_snapshot_id,decision_id,round,plan_version,reason,status,last_error,created_by,created_at from rework_rounds where decision_id=?`, d.ID).Scan(&r.ID, &r.TaskID, &r.SourceSnapshotID, &r.DecisionID, &r.Round, &r.PlanVersion, &r.Reason, &r.Status, &r.LastError, &r.CreatedBy, &r.CreatedAt)
	if err == nil {
		result.ReworkRound = &r
	}
	return result, nil
}
