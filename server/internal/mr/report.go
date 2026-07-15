package mr

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"wanxiang-agent/server/internal/events"
)

func (s *Service) SubmitReport(ctx context.Context, principal Principal, input CompletionReportInput) (MRDetail, error) {
	authorized, err := s.authorizeSubmission(ctx, principal, input)
	if err != nil {
		return MRDetail{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MRDetail{}, err
	}
	defer tx.Rollback()

	var previousID int64
	var previousStatus string
	err = tx.QueryRowContext(ctx, `select id,status from merge_requests where task_id=? and step_id=? order by id desc limit 1`, input.TaskID, input.StepID).Scan(&previousID, &previousStatus)
	if err != nil && err != sql.ErrNoRows {
		return MRDetail{}, err
	}
	if err == nil {
		if previousStatus != MRChangesRequested {
			return MRDetail{}, ErrStateConflict
		}
		if result, updateErr := tx.ExecContext(ctx, `update merge_requests set status=? where id=? and status=?`, MRClosed, previousID, MRChangesRequested); updateErr != nil {
			return MRDetail{}, updateErr
		} else if changed, _ := result.RowsAffected(); changed != 1 {
			return MRDetail{}, ErrStateConflict
		}
	}

	var version int64
	if err := tx.QueryRowContext(ctx, `select coalesce(max(version),0)+1 from completion_reports where task_id=? and step_id=?`, input.TaskID, input.StepID).Scan(&version); err != nil {
		return MRDetail{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	completed, _ := json.Marshal(input.Completed)
	incomplete, _ := json.Marshal(input.Incomplete)
	keyFiles, _ := json.Marshal(input.KeyFiles)
	tests, _ := json.Marshal(input.Tests)
	risks, _ := json.Marshal(input.Risks)
	dependencies, _ := json.Marshal(input.Dependencies)
	mergeOrder, _ := json.Marshal(input.MergeOrder)
	res, err := tx.ExecContext(ctx, `insert into completion_reports(project_id,task_id,step_id,lease_id,lease_version,agent_name,agent_role,version,source_branch,checkpoint_commit,head_commit,completed_json,incomplete_json,key_files_json,tests_json,risks_json,dependencies_json,merge_order_json,user_decision,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, input.ProjectID, input.TaskID, input.StepID, input.LeaseID, input.LeaseVersion, principal.Name, principal.Role, version, input.SourceBranch, input.CheckpointCommit, input.HeadCommit, completed, incomplete, keyFiles, tests, risks, dependencies, mergeOrder, input.UserDecision, now)
	if err != nil {
		return MRDetail{}, err
	}
	reportID, _ := res.LastInsertId()
	title := fmt.Sprintf("完成报告：任务 %d 步骤 %d", input.TaskID, input.StepID)
	res, err = tx.ExecContext(ctx, `insert into merge_requests(project_id,task_id,step_id,report_id,lease_id,report_version,title,source_branch,target_branch,source_commit,project_lead,status,created_by,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, input.ProjectID, input.TaskID, input.StepID, reportID, input.LeaseID, version, title, input.SourceBranch, "main", input.HeadCommit, authorized.ProjectLead, MRPendingReview, principal.Name, now)
	if err != nil {
		return MRDetail{}, err
	}
	mrID, _ := res.LastInsertId()
	reportEvent, err := insertReportEvent(ctx, tx, input.TaskID, "report.created", principal.Name, map[string]any{"report_id": reportID, "version": version, "step_id": input.StepID})
	if err != nil {
		return MRDetail{}, err
	}
	mrEvent, err := insertReportEvent(ctx, tx, input.TaskID, "mr.created", principal.Name, map[string]any{"mr_id": mrID, "report_id": reportID, "status": MRPendingReview})
	if err != nil {
		return MRDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return MRDetail{}, err
	}
	s.bus.Notify(reportEvent)
	s.bus.Notify(mrEvent)
	report := CompletionReport{ID: reportID, CompletionReportInput: input, AgentRole: principal.Role, Version: version, CreatedAt: now}
	mergeRequest := MergeRequest{ID: mrID, ProjectID: input.ProjectID, TaskID: input.TaskID, Title: title, SourceBranch: input.SourceBranch, TargetBranch: "main", Status: MRPendingReview}
	return MRDetail{MergeRequest: mergeRequest, Report: report}, nil
}

func insertReportEvent(ctx context.Context, tx *sql.Tx, taskID int64, eventType, actor string, payload any) (events.Event, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return events.Event{}, err
	}
	return events.InsertTx(ctx, tx, events.Event{TaskID: &taskID, Type: eventType, Actor: actor, Payload: encoded})
}
