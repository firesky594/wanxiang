package mr

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Review 负责人审批或退回合并请求。
func (s *Service) Review(ctx context.Context, principal Principal, mrID int64, input ReviewInput) (MRDetail, error) {
	if err := input.Validate(); err != nil {
		return MRDetail{}, err
	}
	if principal.Name != input.AgentName || principal.Role != input.Role {
		return MRDetail{}, ErrIdentityMismatch
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MRDetail{}, err
	}
	defer tx.Rollback()
	var taskID, stepID, leaseVersion int64
	var lead, status, leaseID string
	if err := tx.QueryRowContext(ctx, `select mr.task_id,coalesce(mr.step_id,0),mr.project_lead,mr.status,mr.lease_id,coalesce(cr.lease_version,0)
		from merge_requests mr left join completion_reports cr on cr.id=mr.report_id where mr.id=?`, mrID).
		Scan(&taskID, &stepID, &lead, &status, &leaseID, &leaseVersion); err != nil {
		if err == sql.ErrNoRows {
			return MRDetail{}, ErrStateConflict
		}
		return MRDetail{}, err
	}
	takeover := principal.Name == "manager" && principal.Role == "manager" && principal.Name != lead
	if principal.Name != lead && !takeover {
		return MRDetail{}, ErrIdentityMismatch
	}
	if takeover {
		if strings.TrimSpace(input.TakeoverReason) == "" {
			return MRDetail{}, ErrIdentityMismatch
		}
		var eligible int
		if err := tx.QueryRowContext(ctx, `select count(*) from merge_requests mr join task_step_leases l on l.lease_id=mr.lease_id where mr.id=? and l.status in ('frozen','revoked','interrupted')`, mrID).Scan(&eligible); err != nil {
			return MRDetail{}, err
		}
		if eligible == 0 {
			var authorized int
			if err := tx.QueryRowContext(ctx, `select count(*) from audit_logs where action='mr.takeover.authorize' and target=?`, fmt.Sprintf("mr:%d", mrID)).Scan(&authorized); err != nil || authorized == 0 {
				return MRDetail{}, ErrIdentityMismatch
			}
		}
	}
	allowed := status == MRPendingReview || (status == MRApproved && input.Status == MRChangesRequested)
	if !allowed {
		return MRDetail{}, ErrStateConflict
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `insert into mr_reviews(mr_id,reviewer,role,status,body,created_at) values(?,?,?,?,?,?)`, mrID, principal.Name, principal.Role, input.Status, input.Body, now); err != nil {
		return MRDetail{}, err
	}
	if takeover {
		payload, _ := json.Marshal(map[string]any{"reason": input.TakeoverReason, "status": input.Status})
		if _, err := tx.ExecContext(ctx, `insert into audit_logs(actor,action,target,payload_json,created_at) values(?,?,?,?,?)`, principal.Name, "mr.takeover.review", fmt.Sprintf("mr:%d", mrID), payload, now); err != nil {
			return MRDetail{}, err
		}
	}
	var result sql.Result
	if input.Status == MRApproved {
		result, err = tx.ExecContext(ctx, `update merge_requests set status=?,reviewed_at=?,approved_at=? where id=? and status=?`, input.Status, now, now, mrID, status)
	} else {
		result, err = tx.ExecContext(ctx, `update merge_requests set status=?,reviewed_at=?,approved_at=null where id=? and status=?`, input.Status, now, mrID, status)
	}
	if err != nil {
		return MRDetail{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return MRDetail{}, ErrStateConflict
	}
	if input.Status == MRApproved {
		allowTakeoverLease := 0
		if takeover {
			allowTakeoverLease = 1
		}
		result, err = tx.ExecContext(ctx, `update task_step_leases
			set status='review',interrupted_at=null,resume_deadline=null,updated_at=?
			where lease_id=? and lease_version=?
				and (status in ('active','review','interrupted')
					or (?=1 and status in ('frozen','revoked')))`,
			now, leaseID, leaseVersion, allowTakeoverLease)
		if err != nil {
			return MRDetail{}, err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return MRDetail{}, ErrStateConflict
		}
		result, err = tx.ExecContext(ctx, `update task_steps set status='review',interrupted_at=null,resume_deadline=null
			where task_id=? and id=? and lease_id=? and lease_version=?
				and (status in ('review','interrupted') or (?=1 and status='blocked'))`,
			taskID, stepID, leaseID, leaseVersion, allowTakeoverLease)
		if err != nil {
			return MRDetail{}, err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return MRDetail{}, ErrStateConflict
		}
	} else {
		result, err = tx.ExecContext(ctx, `update task_step_leases
			set status='revoked',revoked_at=?,revoked_reason='changes_requested',updated_at=?
			where lease_id=? and lease_version=? and status in ('active','review','interrupted')`,
			now, now, leaseID, leaseVersion)
		if err != nil {
			return MRDetail{}, err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return MRDetail{}, ErrStateConflict
		}
		result, err = tx.ExecContext(ctx, `update task_steps
			set status='assigned',lease_id='',lease_expires_at=null,last_heartbeat_at=null,
				checkpoint_id=null,interrupted_at=null,resume_deadline=null
			where task_id=? and id=? and lease_id=? and lease_version=? and status in ('review','interrupted')`,
			taskID, stepID, leaseID, leaseVersion)
		if err != nil {
			return MRDetail{}, err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return MRDetail{}, ErrStateConflict
		}
		result, err = tx.ExecContext(ctx, `update task_assignments set status='assigned'
			where task_id=? and step_id=? and status='review'`, taskID, stepID)
		if err != nil {
			return MRDetail{}, err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return MRDetail{}, ErrStateConflict
		}
	}
	event, err := insertReportEvent(ctx, tx, taskID, "mr.reviewed", principal.Name, map[string]any{"mr_id": mrID, "status": input.Status, "takeover_reason": input.TakeoverReason})
	if err != nil {
		return MRDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return MRDetail{}, err
	}
	s.bus.Notify(event)
	return s.Detail(ctx, principal, mrID)
}
