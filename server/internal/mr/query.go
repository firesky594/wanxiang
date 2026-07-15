package mr

import (
	"context"
	"database/sql"
	"encoding/json"
)

func (s *Service) Detail(ctx context.Context, principal Principal, mrID int64) (MRDetail, error) {
	detail, createdBy, err := s.loadDetail(ctx, mrID)
	if err != nil {
		return MRDetail{}, err
	}
	if principal.Name != createdBy && principal.Name != detail.MergeRequest.ProjectLead && principal.Role != "manager" {
		return MRDetail{}, ErrIdentityMismatch
	}
	return detail, nil
}

func (s *Service) AdminList(ctx context.Context, taskID *int64, limit, offset int) ([]MRDetail, error) {
	items, err := s.List(ctx, taskID, limit, offset)
	if err != nil {
		return nil, err
	}
	result := make([]MRDetail, 0, len(items))
	for _, item := range items {
		detail, _, err := s.loadDetail(ctx, item.ID)
		if err != nil {
			return nil, err
		}
		result = append(result, detail)
	}
	return result, nil
}

func (s *Service) AdminDetail(ctx context.Context, mrID int64) (MRDetail, error) {
	detail, _, err := s.loadDetail(ctx, mrID)
	return detail, err
}

func (s *Service) ListNotifications(ctx context.Context, limit, offset int) ([]ManagerNotification, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `select id,project_id,task_id,mr_id,report_id,project_lead,main_commit,status from manager_notifications order by id desc limit ? offset ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ManagerNotification, 0)
	for rows.Next() {
		var item ManagerNotification
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.TaskID, &item.MRID, &item.ReportID, &item.ProjectLead, &item.MainCommit, &item.Status); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) loadDetail(ctx context.Context, mrID int64) (MRDetail, string, error) {
	var detail MRDetail
	var createdBy string
	var completed, incomplete, keyFiles, tests, risks, dependencies, mergeOrder string
	err := s.db.QueryRowContext(ctx, `select mr.id,mr.project_id,mr.task_id,mr.title,mr.source_branch,mr.target_branch,mr.status,coalesce(mr.report_id,0),coalesce(mr.step_id,0),mr.report_version,mr.source_commit,mr.project_lead,mr.created_by,
		cr.id,cr.project_id,cr.task_id,cr.step_id,cr.lease_id,cr.lease_version,cr.agent_name,cr.agent_role,cr.version,cr.source_branch,cr.checkpoint_commit,cr.head_commit,cr.completed_json,cr.incomplete_json,cr.key_files_json,cr.tests_json,cr.risks_json,cr.dependencies_json,cr.merge_order_json,cr.user_decision,cr.created_at
		from merge_requests mr join completion_reports cr on cr.id=mr.report_id where mr.id=?`, mrID).Scan(
		&detail.MergeRequest.ID, &detail.MergeRequest.ProjectID, &detail.MergeRequest.TaskID, &detail.MergeRequest.Title, &detail.MergeRequest.SourceBranch, &detail.MergeRequest.TargetBranch, &detail.MergeRequest.Status, &detail.MergeRequest.ReportID, &detail.MergeRequest.StepID, &detail.MergeRequest.ReportVersion, &detail.MergeRequest.SourceCommit, &detail.MergeRequest.ProjectLead, &createdBy,
		&detail.Report.ID, &detail.Report.ProjectID, &detail.Report.TaskID, &detail.Report.StepID, &detail.Report.LeaseID, &detail.Report.LeaseVersion, &detail.Report.AgentName, &detail.Report.AgentRole, &detail.Report.Version, &detail.Report.SourceBranch, &detail.Report.CheckpointCommit, &detail.Report.HeadCommit, &completed, &incomplete, &keyFiles, &tests, &risks, &dependencies, &mergeOrder, &detail.Report.UserDecision, &detail.Report.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return MRDetail{}, "", ErrStateConflict
		}
		return MRDetail{}, "", err
	}
	detail.Report.Role = detail.Report.AgentRole
	_ = json.Unmarshal([]byte(completed), &detail.Report.Completed)
	_ = json.Unmarshal([]byte(incomplete), &detail.Report.Incomplete)
	_ = json.Unmarshal([]byte(keyFiles), &detail.Report.KeyFiles)
	_ = json.Unmarshal([]byte(tests), &detail.Report.Tests)
	_ = json.Unmarshal([]byte(risks), &detail.Report.Risks)
	_ = json.Unmarshal([]byte(dependencies), &detail.Report.Dependencies)
	_ = json.Unmarshal([]byte(mergeOrder), &detail.Report.MergeOrder)
	rows, err := s.db.QueryContext(ctx, `select id,mr_id,reviewer,role,status,body,created_at from mr_reviews where mr_id=? order by id`, mrID)
	if err != nil {
		return MRDetail{}, "", err
	}
	defer rows.Close()
	for rows.Next() {
		var review MRReview
		if err := rows.Scan(&review.ID, &review.MRID, &review.Reviewer, &review.Role, &review.Status, &review.Body, &review.CreatedAt); err != nil {
			return MRDetail{}, "", err
		}
		detail.Reviews = append(detail.Reviews, review)
	}
	return detail, createdBy, rows.Err()
}
