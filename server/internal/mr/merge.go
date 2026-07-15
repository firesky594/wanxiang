package mr

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"wanxiang-agent/server/internal/files"
	"wanxiang-agent/server/internal/gitx"
)

type approvedMerge struct {
	ProjectID    int64
	TaskID       int64
	StepID       int64
	ReportID     int64
	Lead         string
	Status       string
	SourceBranch string
	SourceCommit string
	LeaseID      string
	ProjectDir   string
}

func (s *Service) Merge(ctx context.Context, principal Principal, mrID int64, input MergeInput) (MergeResult, error) {
	if principal.Name != input.AgentName || principal.Role != input.Role {
		return MergeResult{}, ErrIdentityMismatch
	}
	record, err := s.loadApprovedMerge(ctx, mrID)
	if err != nil {
		return MergeResult{}, err
	}
	if record.Status != MRApproved {
		return MergeResult{}, ErrStateConflict
	}
	if principal.Name != record.Lead {
		return MergeResult{}, ErrIdentityMismatch
	}
	projectDir, err := files.UnderRoot(s.cfg.ProjectDir, record.ProjectDir)
	if err != nil {
		return MergeResult{}, ErrBranchOwnership
	}
	blocked, err := s.blocker.HasBlockingForMR(ctx, mrID)
	if err != nil {
		return MergeResult{}, err
	}
	if blocked {
		return MergeResult{}, ErrMergeBlocked
	}
	var dependencies int
	if err := s.db.QueryRowContext(ctx, `select count(*) from workflow_edges e join task_steps dep on dep.id=e.from_step_id where e.task_id=? and e.to_step_id=? and dep.status!='completed' and not exists(select 1 from merge_requests m where m.step_id=dep.id and m.status='merged')`, record.TaskID, record.StepID).Scan(&dependencies); err != nil {
		return MergeResult{}, err
	}
	if dependencies > 0 {
		return MergeResult{}, ErrMergeBlocked
	}
	var leaseOK int
	if err := s.db.QueryRowContext(ctx, `select count(*) from task_step_leases where lease_id=? and status='active' and expires_at>?`, record.LeaseID, time.Now().UTC().Format(time.RFC3339Nano)).Scan(&leaseOK); err != nil || leaseOK != 1 {
		return MergeResult{}, ErrLeaseInvalid
	}
	if err := validateSourceBranch(ctx, projectDir, record.SourceBranch); err != nil {
		return MergeResult{}, ErrBranchOwnership
	}
	branchCommit, err := gitx.Run(ctx, projectDir, "rev-parse", record.SourceBranch)
	if err != nil || strings.TrimSpace(branchCommit) != record.SourceCommit {
		return MergeResult{}, ErrCheckpointMismatch
	}
	if out, err := gitx.Run(ctx, projectDir, "status", "--porcelain"); err != nil || strings.TrimSpace(out) != "" {
		return MergeResult{}, ErrMergeBlocked
	}
	if out, err := gitx.Run(ctx, projectDir, "checkout", "main"); err != nil {
		return MergeResult{}, fmt.Errorf("checkout main: %w: %s", err, strings.TrimSpace(out))
	}
	if out, err := gitx.Run(ctx, projectDir, "merge", "--no-ff", "--no-edit", record.SourceBranch); err != nil {
		mergeErr := fmt.Errorf("merge conflict: %w: %s", err, strings.TrimSpace(out))
		if abortErr := abortMerge(ctx, projectDir); abortErr != nil {
			return MergeResult{}, fmt.Errorf("%v; abort: %w", mergeErr, abortErr)
		}
		return MergeResult{}, mergeErr
	}
	mergeCommitRaw, err := gitx.Run(ctx, projectDir, "rev-parse", "HEAD")
	if err != nil {
		return MergeResult{}, err
	}
	mergeCommit := strings.TrimSpace(mergeCommitRaw)
	return s.persistMerged(ctx, record, mrID, principal.Name, mergeCommit)
}

func (s *Service) ReconcileMerge(ctx context.Context, mrID int64) (MergeResult, error) {
	record, err := s.loadApprovedMerge(ctx, mrID)
	if err != nil || record.Status != MRApproved {
		return MergeResult{}, ErrStateConflict
	}
	projectDir, err := files.UnderRoot(s.cfg.ProjectDir, record.ProjectDir)
	if err != nil {
		return MergeResult{}, ErrBranchOwnership
	}
	if out, err := gitx.Run(ctx, projectDir, "merge-base", "--is-ancestor", record.SourceCommit, "main"); err != nil {
		return MergeResult{}, fmt.Errorf("source commit is not merged: %w: %s", err, strings.TrimSpace(out))
	}
	mainRaw, err := gitx.Run(ctx, projectDir, "rev-parse", "main")
	if err != nil {
		return MergeResult{}, err
	}
	mainCommit := strings.TrimSpace(mainRaw)
	if mainCommit == record.SourceCommit {
		return MergeResult{}, ErrStateConflict
	}
	return s.persistMerged(ctx, record, mrID, record.Lead, mainCommit)
}

func (s *Service) loadApprovedMerge(ctx context.Context, mrID int64) (approvedMerge, error) {
	var record approvedMerge
	err := s.db.QueryRowContext(ctx, `select mr.project_id,mr.task_id,coalesce(mr.step_id,0),coalesce(mr.report_id,0),mr.project_lead,mr.status,mr.source_branch,mr.source_commit,mr.lease_id,p.dir from merge_requests mr join projects p on p.id=mr.project_id where mr.id=?`, mrID).Scan(&record.ProjectID, &record.TaskID, &record.StepID, &record.ReportID, &record.Lead, &record.Status, &record.SourceBranch, &record.SourceCommit, &record.LeaseID, &record.ProjectDir)
	if err != nil {
		return approvedMerge{}, ErrStateConflict
	}
	return record, nil
}

func (s *Service) persistMerged(ctx context.Context, record approvedMerge, mrID int64, actor, mergeCommit string) (MergeResult, error) {
	detail, _, err := s.loadDetail(ctx, mrID)
	if err != nil {
		return MergeResult{}, err
	}
	payload, _ := json.Marshal(map[string]any{"mr_id": mrID, "report_id": record.ReportID, "merge_commit": mergeCommit, "tests": detail.Report.Tests, "risks": detail.Report.Risks, "incomplete": detail.Report.Incomplete, "user_decision": detail.Report.UserDecision})
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MergeResult{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `update merge_requests set status=?,merged_by=?,merge_commit=?,merged_at=? where id=? and status=?`, MRMerged, actor, mergeCommit, now, mrID, MRApproved)
	if err != nil {
		return MergeResult{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return MergeResult{}, ErrStateConflict
	}
	if _, err := tx.ExecContext(ctx, `update task_steps set status='completed',completed_at=? where id=? and task_id=?`, now, record.StepID, record.TaskID); err != nil {
		return MergeResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `update task_step_leases set status='completed',updated_at=? where lease_id=?`, now, record.LeaseID); err != nil {
		return MergeResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `insert into manager_notifications(project_id,task_id,mr_id,report_id,project_lead,main_commit,payload_json,status,created_at) values(?,?,?,?,?,?,?,'pending',?)`, record.ProjectID, record.TaskID, mrID, record.ReportID, record.Lead, mergeCommit, payload, now); err != nil {
		return MergeResult{}, err
	}
	event, err := insertReportEvent(ctx, tx, record.TaskID, "mr.merged", actor, map[string]any{"mr_id": mrID, "report_id": record.ReportID, "merge_commit": mergeCommit, "status": MRMerged})
	if err != nil {
		return MergeResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return MergeResult{}, err
	}
	s.bus.Notify(event)
	return MergeResult{MRID: mrID, Status: MRMerged, MergeCommit: mergeCommit}, nil
}
