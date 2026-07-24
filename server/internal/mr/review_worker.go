package mr

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"wanxiang-agent/server/internal/events"
	"wanxiang-agent/server/internal/files"
	"wanxiang-agent/server/internal/gitx"
	"wanxiang-agent/server/internal/providers"
)

const (
	reviewJobPending       = "pending_review"
	reviewJobReviewing     = "reviewing"
	reviewJobRetryReview   = "retry_review"
	reviewJobWaitingReview = "waiting_review"
	reviewJobMergePending  = "merge_pending"
	reviewJobMerging       = "merging"
	reviewJobRetryMerge    = "retry_merge"
	reviewJobWaitingMerge  = "waiting_merge"
	reviewJobCompleted     = "completed"
	reviewJobBlocked       = "blocked"

	reviewClaimTimeout = 5 * time.Minute
	reviewCallTimeout  = 90 * time.Second
	maxReviewDiffBytes = 24 * 1024
	maxReviewFiles     = 50
)

var (
	reviewSecretPattern     = regexp.MustCompile(`(?i)\b((?:[a-z0-9]+[_-])*(?:api[_-]?key|access[_-]?key|access[_-]?token|refresh[_-]?token|authorization|cookie|credential|token|password|passwd|secret(?:[_-]?key)?|private[_-]?key))\b["'\x60]?\s*[:=]\s*[^\s,;}]+`)
	reviewSecretLinePattern = regexp.MustCompile(`(?im)^([^\r\n]{0,160}\b(?:[a-z0-9]+[_-])*(?:api[_-]?key|access[_-]?key|access[_-]?token|refresh[_-]?token|authorization|cookie|credential|token|password|passwd|secret(?:[_-]?key)?|private[_-]?key)\b["'\x60]?\s*[:=]).*$`)
	reviewSecretKeyPattern  = regexp.MustCompile(`(?i)\b(?:[a-z0-9]+[_-])*(?:api[_-]?key|access[_-]?key|access[_-]?token|refresh[_-]?token|authorization|cookie|credential|token|password|passwd|secret(?:[_-]?key)?|private[_-]?key)\b["'\x60]?\s*[:=]`)
	reviewSKPattern         = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{8,}|gh[pousr]_[A-Za-z0-9_]{8,}|github_pat_[A-Za-z0-9_]{8,})\b`)
	reviewJWTPattern        = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
	reviewBearerPattern     = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{12,}\b`)
	reviewCloudKeyPattern   = regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)
	reviewPrivateKeyPattern = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	reviewLongSecretPattern = regexp.MustCompile(`\b(?:[A-Fa-f0-9]{48,}|[A-Za-z0-9+/=_-]{64,})\b`)
)

// ReviewChatter 约束自动评审只调用数据库指定负责人的 Agent。
type ReviewChatter interface {
	ChatAgent(context.Context, string, []providers.Message, int) (providers.Result, error)
}

// ReviewWorker 持久化领取待评审 MR，并在确定性门禁通过后驱动负责人评审及合并。
type ReviewWorker struct {
	db       *sql.DB
	service  *Service
	chatter  ReviewChatter
	interval time.Duration

	lifecycleMu sync.Mutex
	scanMu      sync.Mutex
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

type reviewJobClaim struct {
	ID    int64
	MRID  int64
	Phase string
}

type reviewCandidate struct {
	Detail           MRDetail
	Lead             Principal
	LeadStatus       string
	CurrentLead      string
	StepStatus       string
	StepLeaseID      string
	StepLeaseVersion int64
	AssignmentStatus string
	LeaseStatus      string
	CheckpointID     int64
	CheckpointLease  string
	CheckpointCommit string
	CheckpointBranch string
	CheckpointClean  bool
	CheckpointRisk   bool
	ProjectDir       string
	WorkItem         string
}

type reviewGateResult struct {
	action string
	code   string
	body   string
	diff   string
}

type agentReviewDecision struct {
	Status string `json:"status"`
	Body   string `json:"body"`
}

// NewReviewWorker 创建具备数据库领取和崩溃恢复能力的 MR 自动评审器。
func NewReviewWorker(db *sql.DB, service *Service, chatter ReviewChatter, interval time.Duration) *ReviewWorker {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &ReviewWorker{db: db, service: service, chatter: chatter, interval: interval}
}

// Start 幂等启动 MR 自动评审轮询。
func (w *ReviewWorker) Start() {
	w.lifecycleMu.Lock()
	if w.cancel != nil {
		w.lifecycleMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.wg.Add(1)
	w.lifecycleMu.Unlock()

	go func() {
		defer w.wg.Done()
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		_ = w.Scan(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = w.Scan(ctx)
			}
		}
	}()
}

// Close 幂等停止自动评审并等待当前 Provider 调用退出。
func (w *ReviewWorker) Close() {
	w.lifecycleMu.Lock()
	cancel := w.cancel
	if cancel == nil {
		w.lifecycleMu.Unlock()
		return
	}
	cancel()
	w.wg.Wait()
	w.cancel = nil
	w.lifecycleMu.Unlock()
}

// Scan 扫描并处理有限数量的待评审或待合并任务。
func (w *ReviewWorker) Scan(ctx context.Context) error {
	if w == nil || w.db == nil || w.service == nil {
		return errors.New("mr review worker is unavailable")
	}
	w.scanMu.Lock()
	defer w.scanMu.Unlock()
	if err := w.enqueueAndReconcile(ctx); err != nil {
		return err
	}
	for processed := 0; processed < 10; processed++ {
		claim, ok, err := w.claimNext(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if claim.Phase == reviewJobMerging {
			w.processMerge(ctx, claim)
		} else {
			w.processReview(ctx, claim)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
}

func (w *ReviewWorker) enqueueAndReconcile(ctx context.Context) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `insert into mr_review_jobs(mr_id,status,created_at,updated_at)
		select id,case when status=? then ? else ? end,?,?
		from merge_requests
		where report_id is not null and status in (?,?)
		on conflict(mr_id) do nothing`,
		MRApproved, reviewJobMergePending, reviewJobPending, now, now, MRPendingReview, MRApproved); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update mr_review_jobs
		set status=?,processing_started_at=null,next_retry_at=null,last_error='',updated_at=?
		where mr_id in (select id from merge_requests where status in (?,?,?))
			and status<>?`,
		reviewJobCompleted, now, MRMerged, MRChangesRequested, MRClosed, reviewJobCompleted); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update mr_review_jobs
		set status=?,processing_started_at=null,next_retry_at=null,last_error='',blocked_code='',updated_at=?
		where mr_id in (select id from merge_requests where status=?)
			and (
				status in (?,?,?,?)
				or (status=? and blocked_code not like 'merge_%')
			)`,
		reviewJobMergePending, now, MRApproved,
		reviewJobPending, reviewJobReviewing, reviewJobRetryReview, reviewJobWaitingReview, reviewJobBlocked); err != nil {
		return err
	}
	return tx.Commit()
}

func (w *ReviewWorker) claimNext(ctx context.Context) (reviewJobClaim, bool, error) {
	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	stale := now.Add(-reviewClaimTimeout).Format(time.RFC3339Nano)
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return reviewJobClaim{}, false, err
	}
	defer tx.Rollback()
	var claim reviewJobClaim
	var currentStatus, mrStatus string
	err = tx.QueryRowContext(ctx, `select j.id,j.mr_id,j.status,mr.status
		from mr_review_jobs j
		join merge_requests mr on mr.id=j.mr_id
		where (
			j.status in (?,?,?,?,?,?) and (j.next_retry_at is null or j.next_retry_at<=?)
		) or (
			j.status in (?,?) and j.processing_started_at is not null and j.processing_started_at<=?
		)
		order by j.id
		limit 1`,
		reviewJobPending, reviewJobRetryReview, reviewJobWaitingReview,
		reviewJobMergePending, reviewJobRetryMerge, reviewJobWaitingMerge,
		nowText, reviewJobReviewing, reviewJobMerging, stale).
		Scan(&claim.ID, &claim.MRID, &currentStatus, &mrStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return reviewJobClaim{}, false, nil
	}
	if err != nil {
		return reviewJobClaim{}, false, err
	}
	switch mrStatus {
	case MRPendingReview:
		claim.Phase = reviewJobReviewing
	case MRApproved:
		claim.Phase = reviewJobMerging
	case MRMerged, MRChangesRequested, MRClosed:
		if _, err := tx.ExecContext(ctx, `update mr_review_jobs set status=?,processing_started_at=null,updated_at=? where id=? and status=?`,
			reviewJobCompleted, nowText, claim.ID, currentStatus); err != nil {
			return reviewJobClaim{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return reviewJobClaim{}, false, err
		}
		return reviewJobClaim{}, false, nil
	default:
		claim.Phase = reviewJobReviewing
	}
	result, err := tx.ExecContext(ctx, `update mr_review_jobs
		set status=?,processing_started_at=?,next_retry_at=null,updated_at=?
		where id=? and status=?`, claim.Phase, nowText, nowText, claim.ID, currentStatus)
	if err != nil {
		return reviewJobClaim{}, false, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return reviewJobClaim{}, false, nil
	}
	if err := tx.Commit(); err != nil {
		return reviewJobClaim{}, false, err
	}
	return claim, true, nil
}

func (w *ReviewWorker) processReview(ctx context.Context, claim reviewJobClaim) {
	candidate, err := w.loadReviewCandidate(ctx, claim.MRID)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrStateConflict) {
		_ = w.block(ctx, claim, 0, "review_context_invalid", "评审上下文与当前数据库状态不一致，需要人工核验")
		return
	}
	if err != nil {
		_, _ = w.retry(ctx, claim, false, "review context temporarily unavailable")
		return
	}
	gate := w.evaluateGates(ctx, candidate)
	switch gate.action {
	case "wait":
		_ = w.wait(ctx, claim, false, gate.code)
		return
	case "block":
		_ = w.block(ctx, claim, candidate.Detail.MergeRequest.TaskID, gate.code, gate.body)
		return
	case "changes_requested":
		w.applyReview(ctx, claim, candidate, agentReviewDecision{Status: MRChangesRequested, Body: gate.body})
		return
	case "review":
	default:
		_ = w.block(ctx, claim, candidate.Detail.MergeRequest.TaskID, "review_gate_invalid", "自动评审门禁返回了未知状态")
		return
	}
	if w.chatter == nil {
		_, _ = w.retry(ctx, claim, false, "project lead provider unavailable")
		return
	}
	messages, err := buildReviewMessages(candidate, gate.diff)
	if err != nil {
		_ = w.block(ctx, claim, candidate.Detail.MergeRequest.TaskID, "review_payload_invalid", "评审材料无法安全构造")
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, reviewCallTimeout)
	result, err := w.chatter.ChatAgent(callCtx, candidate.Lead.Name, messages, 2048)
	cancel()
	if err != nil {
		_, _ = w.retry(ctx, claim, false, "project lead provider request failed")
		return
	}
	decision, err := parseAgentReviewDecision(result.Content)
	if err != nil {
		attempts, retryErr := w.retry(ctx, claim, false, "project lead returned invalid structured review")
		if retryErr == nil && attempts >= 3 {
			_ = w.blockFromRetry(ctx, claim, candidate.Detail.MergeRequest.TaskID, "invalid_review_response", "负责人 Agent 连续返回无效评审 JSON，需要人工处理")
		}
		return
	}
	w.applyReview(ctx, claim, candidate, decision)
}

func (w *ReviewWorker) processMerge(ctx context.Context, claim reviewJobClaim) {
	lead, taskID, err := w.loadLeadPrincipal(ctx, claim.MRID)
	if err != nil {
		_ = w.block(ctx, claim, taskID, "merge_identity_invalid", "无法从数据库确认当前项目负责人身份")
		return
	}
	if _, err := w.service.ReconcileMerge(ctx, claim.MRID); err == nil {
		_ = w.complete(ctx, claim.ID)
		return
	}
	_, err = w.service.Merge(ctx, lead, claim.MRID, MergeInput{AgentName: lead.Name, Role: lead.Role})
	if err == nil {
		_ = w.complete(ctx, claim.ID)
		return
	}
	switch {
	case errors.Is(err, ErrMergeConflict):
		_, reviewErr := w.service.Review(ctx, lead, claim.MRID, ReviewInput{
			AgentName: lead.Name,
			Role:      lead.Role,
			Status:    MRChangesRequested,
			Body:      "自动合并检测到主线冲突；合并已安全中止，请同步主线、解决冲突并重新提交。",
		})
		if reviewErr == nil {
			_ = w.complete(ctx, claim.ID)
			return
		}
		if errors.Is(reviewErr, ErrStateConflict) {
			_, _ = w.retry(ctx, claim, true, "merge conflict review state changed")
			return
		}
		_ = w.block(ctx, claim, taskID, "merge_conflict_review_failed", "合并冲突已中止，但自动退回失败，需要人工核验")
	case errors.Is(err, ErrCheckpointMismatch), errors.Is(err, ErrBranchOwnership), errors.Is(err, ErrLeaseInvalid):
		_ = w.block(ctx, claim, taskID, "merge_source_invalid", "合并来源、检查点或评审租约不再一致，需要人工核验")
	case errors.Is(err, ErrMergeAbortFailed):
		_ = w.block(ctx, claim, taskID, "merge_abort_failed", "合并冲突后的安全中止失败，仓库可能仍处于合并状态，需要人工核验")
	case errors.Is(err, ErrMergeBlocked):
		_ = w.wait(ctx, claim, true, "merge_blocked")
	case errors.Is(err, ErrStateConflict):
		if reconcileErr := w.enqueueAndReconcile(ctx); reconcileErr != nil {
			_, _ = w.retry(ctx, claim, true, "merge state changed")
		}
	default:
		_, _ = w.retry(ctx, claim, true, "merge temporarily failed")
	}
}

func (w *ReviewWorker) loadReviewCandidate(ctx context.Context, mrID int64) (reviewCandidate, error) {
	detail, _, err := w.service.loadDetail(ctx, mrID)
	if err != nil {
		return reviewCandidate{}, err
	}
	var candidate reviewCandidate
	candidate.Detail = detail
	var checkpointClean, checkpointRisk int
	err = w.db.QueryRowContext(ctx, `select coalesce(ar.role,''),coalesce(ar.status,'missing'),coalesce(td.project_lead,''),ts.status,ts.lease_id,ts.lease_version,
			ta.status,l.status,coalesce(cp.id,0),coalesce(cp.lease_id,''),coalesce(cp.git_commit,''),
			coalesce(cp.branch_name,''),coalesce(cp.clean,0),coalesce(cp.high_risk,0),p.dir,ts.input
		from merge_requests mr
		join projects p on p.id=mr.project_id
		join task_steps ts on ts.task_id=mr.task_id and ts.id=mr.step_id
		join task_assignments ta on ta.task_id=ts.task_id and ta.step_id=ts.id and ta.agent_name=ts.agent_name
		join task_step_leases l on l.lease_id=mr.lease_id and l.task_id=ts.task_id and l.step_id=ts.id
		left join task_checkpoints cp on cp.id=ts.checkpoint_id
		left join team_decisions td on td.task_id=ts.task_id and td.plan_version=ts.plan_version
		left join agent_registry ar on ar.name=mr.project_lead
		where mr.id=?`,
		mrID).Scan(&candidate.Lead.Role, &candidate.LeadStatus, &candidate.CurrentLead, &candidate.StepStatus,
		&candidate.StepLeaseID, &candidate.StepLeaseVersion, &candidate.AssignmentStatus,
		&candidate.LeaseStatus, &candidate.CheckpointID, &candidate.CheckpointLease,
		&candidate.CheckpointCommit, &candidate.CheckpointBranch, &checkpointClean,
		&checkpointRisk, &candidate.ProjectDir, &candidate.WorkItem)
	if err != nil {
		return reviewCandidate{}, err
	}
	candidate.Lead.Name = detail.MergeRequest.ProjectLead
	candidate.CheckpointClean = checkpointClean == 1
	candidate.CheckpointRisk = checkpointRisk == 1
	return candidate, nil
}

func (w *ReviewWorker) loadLeadPrincipal(ctx context.Context, mrID int64) (Principal, int64, error) {
	var lead Principal
	var currentLead string
	var taskID int64
	err := w.db.QueryRowContext(ctx, `select mr.task_id,mr.project_lead,ar.role,coalesce(td.project_lead,'')
		from merge_requests mr
		join task_steps ts on ts.task_id=mr.task_id and ts.id=mr.step_id
		left join team_decisions td on td.task_id=ts.task_id and td.plan_version=ts.plan_version
		join agent_registry ar on ar.name=mr.project_lead
		where mr.id=?`, mrID).Scan(&taskID, &lead.Name, &lead.Role, &currentLead)
	if err != nil {
		return Principal{}, taskID, err
	}
	if strings.TrimSpace(lead.Name) == "" || strings.TrimSpace(lead.Role) == "" || currentLead != lead.Name {
		return Principal{}, taskID, ErrIdentityMismatch
	}
	return lead, taskID, nil
}

func (w *ReviewWorker) evaluateGates(ctx context.Context, candidate reviewCandidate) reviewGateResult {
	detail := candidate.Detail
	mr := detail.MergeRequest
	report := detail.Report
	if mr.Status != MRPendingReview ||
		mr.TargetBranch != "main" ||
		candidate.Lead.Name == "" || candidate.CurrentLead != candidate.Lead.Name ||
		candidate.StepStatus != "review" || candidate.AssignmentStatus != "review" ||
		candidate.LeaseStatus != "review" ||
		candidate.StepLeaseID != report.LeaseID || candidate.StepLeaseID != candidate.CheckpointLease ||
		candidate.StepLeaseVersion != report.LeaseVersion ||
		candidate.CheckpointID <= 0 || !candidate.CheckpointClean {
		return reviewGateResult{action: "block", code: "review_state_invalid", body: "评审步骤、分配、租约或检查点状态不一致，需要人工核验"}
	}
	if mr.SourceBranch != report.SourceBranch ||
		mr.SourceCommit == "" || mr.SourceCommit != report.HeadCommit ||
		report.HeadCommit != report.CheckpointCommit ||
		candidate.CheckpointCommit != report.CheckpointCommit ||
		candidate.CheckpointBranch != report.SourceBranch {
		return reviewGateResult{action: "block", code: "review_source_invalid", body: "完成报告、MR 来源与干净检查点不一致，需要人工核验"}
	}
	if candidate.CheckpointRisk || strings.TrimSpace(report.UserDecision) != "" || containsHighRisk(report.Risks) {
		return reviewGateResult{action: "block", code: "human_decision_required", body: "报告包含高风险操作或用户决策项，禁止自动批准，必须由人工负责人处理"}
	}
	if strings.TrimSpace(candidate.Lead.Role) == "" || candidate.LeadStatus != "online" {
		return reviewGateResult{action: "wait", code: "project_lead_unavailable"}
	}
	blocked, err := w.service.blocker.HasBlockingForMR(ctx, mr.ID)
	if err != nil {
		return reviewGateResult{action: "wait", code: "blocking_issue_check_failed"}
	}
	if blocked {
		return reviewGateResult{action: "wait", code: "blocking_issue"}
	}
	if len(nonEmptyStrings(report.Completed)) == 0 {
		return reviewGateResult{action: "changes_requested", code: "missing_completed_evidence", body: "完成报告缺少已完成事项，请补充可核验的完成证据后重新提交。"}
	}
	if len(nonEmptyStrings(report.Incomplete)) > 0 {
		return reviewGateResult{action: "changes_requested", code: "incomplete_work", body: "完成报告仍有未完成项，请完成或明确拆分后重新提交。"}
	}
	if ok, reason := validTestEvidence(report.Tests); !ok {
		return reviewGateResult{action: "changes_requested", code: "test_evidence_invalid", body: reason}
	}
	var dependencies int
	err = w.db.QueryRowContext(ctx, `select count(*)
		from workflow_edges e
		join task_steps current on current.task_id=e.task_id and current.id=e.to_step_id
		join task_steps dependency on dependency.task_id=e.task_id and dependency.id=e.from_step_id
		where e.task_id=? and e.to_step_id=? and e.from_step_id is not null
			and e.plan_version=current.plan_version
			and (
				dependency.status<>'completed'
				or not exists(select 1 from merge_requests merged where merged.task_id=e.task_id and merged.step_id=dependency.id and merged.status=?)
			)`,
		mr.TaskID, mr.StepID, MRMerged).Scan(&dependencies)
	if err != nil {
		return reviewGateResult{action: "wait", code: "dependency_check_failed"}
	}
	if dependencies > 0 {
		return reviewGateResult{action: "wait", code: "dependencies_not_merged"}
	}
	diff, err := w.restrictedDiff(ctx, candidate)
	if err != nil {
		return reviewGateResult{action: "block", code: "review_diff_invalid", body: "来源分支或受限差异无法与报告安全对应，需要人工核验"}
	}
	return reviewGateResult{action: "review", diff: diff}
}

func (w *ReviewWorker) restrictedDiff(ctx context.Context, candidate reviewCandidate) (string, error) {
	projectDir, err := files.UnderRoot(w.service.cfg.ProjectDir, candidate.ProjectDir)
	if err != nil {
		return "", err
	}
	branch := candidate.Detail.MergeRequest.SourceBranch
	if err := validateSourceBranch(ctx, projectDir, branch); err != nil {
		return "", err
	}
	head, err := gitx.Run(ctx, projectDir, "rev-parse", branch)
	if err != nil || strings.TrimSpace(head) != candidate.Detail.MergeRequest.SourceCommit {
		return "", ErrCheckpointMismatch
	}
	nameOutput, truncated, err := runBoundedGit(ctx, projectDir, 64*1024,
		"diff", "--no-ext-diff", "--no-textconv", "--name-only", "-z", "main..."+branch)
	if err != nil || truncated {
		return "", errors.New("changed path list is unavailable")
	}
	changed := splitNULPaths(nameOutput)
	if len(changed) > maxReviewFiles {
		return "", errors.New("too many changed files for automatic review")
	}
	keyFiles := make(map[string]struct{}, len(candidate.Detail.Report.KeyFiles))
	for _, item := range candidate.Detail.Report.KeyFiles {
		path, err := safeReviewPath(item)
		if err != nil {
			return "", err
		}
		keyFiles[path] = struct{}{}
	}
	for _, path := range changed {
		safe, err := safeReviewPath(path)
		if err != nil || safe != path || sensitiveReviewPath(path) {
			return "", errors.New("unsafe changed path")
		}
		if _, ok := keyFiles[path]; !ok {
			return "", errors.New("report does not cover all changed files")
		}
	}
	if len(changed) == 0 {
		return "(no textual file changes)", nil
	}
	sort.Strings(changed)
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--unified=1", "main..." + branch, "--"}
	for _, path := range changed {
		args = append(args, ":(literal)"+path)
	}
	diff, truncated, err := runBoundedGit(ctx, projectDir, maxReviewDiffBytes, args...)
	if err != nil {
		return "", err
	}
	if truncated {
		return "", errors.New("source diff exceeds automatic review limit")
	}
	return redactReviewText(diff), nil
}

func (w *ReviewWorker) applyReview(ctx context.Context, claim reviewJobClaim, candidate reviewCandidate, decision agentReviewDecision) {
	input := ReviewInput{
		AgentName: candidate.Lead.Name,
		Role:      candidate.Lead.Role,
		Status:    decision.Status,
		Body:      strings.TrimSpace(decision.Body),
	}
	if input.Status == MRApproved && input.Body == "" {
		input.Body = "负责人 Agent 自动评审通过。"
	}
	if _, err := w.service.Review(ctx, candidate.Lead, claim.MRID, input); err != nil {
		if errors.Is(err, ErrStateConflict) {
			_ = w.enqueueAndReconcile(ctx)
			return
		}
		_, _ = w.retry(ctx, claim, false, "persist review decision failed")
		return
	}
	decisionJSON, _ := json.Marshal(decision)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	status := reviewJobCompleted
	if decision.Status == MRApproved {
		status = reviewJobMergePending
	}
	_, _ = w.db.ExecContext(ctx, `update mr_review_jobs
		set status=?,decision_json=?,processing_started_at=null,next_retry_at=null,last_error='',updated_at=?
		where id=? and status=?`,
		status, string(decisionJSON), now, claim.ID, reviewJobReviewing)
}

func buildReviewMessages(candidate reviewCandidate, diff string) ([]providers.Message, error) {
	report := candidate.Detail.Report
	payload := struct {
		MRID       int64          `json:"mr_id"`
		TaskID     int64          `json:"task_id"`
		StepID     int64          `json:"step_id"`
		WorkItem   string         `json:"work_item"`
		Completed  []string       `json:"completed"`
		KeyFiles   []string       `json:"key_files"`
		Tests      []TestEvidence `json:"tests"`
		Risks      []string       `json:"risks"`
		SourceDiff string         `json:"bounded_source_diff"`
	}{
		MRID:       candidate.Detail.MergeRequest.ID,
		TaskID:     candidate.Detail.MergeRequest.TaskID,
		StepID:     candidate.Detail.MergeRequest.StepID,
		WorkItem:   limitReviewText(redactReviewText(candidate.WorkItem), 8*1024),
		Completed:  boundedReviewStrings(report.Completed, 50, 1024),
		KeyFiles:   boundedReviewStrings(report.KeyFiles, maxReviewFiles, 512),
		Tests:      boundedReviewTests(report.Tests),
		Risks:      boundedReviewStrings(report.Risks, 50, 1024),
		SourceDiff: limitReviewText(diff, maxReviewDiffBytes+64),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	system := `你是该任务在数据库中登记的项目负责人。只根据给定完成报告和受限源码差异评审，不得调用工具、假设未提供事实或要求扩大权限。若实现、验收或风险处理有任何不确定，必须返回 changes_requested。只允许返回一个严格 JSON 对象，不要 Markdown、代码围栏或额外文字：{"status":"approved|changes_requested","body":"简洁、可执行的中文评审意见"}。`
	return []providers.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: string(encoded)},
	}, nil
}

func parseAgentReviewDecision(content string) (agentReviewDecision, error) {
	var decision agentReviewDecision
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decision); err != nil {
		return agentReviewDecision{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return agentReviewDecision{}, errors.New("review response contains trailing content")
	}
	decision.Status = strings.TrimSpace(decision.Status)
	decision.Body = strings.TrimSpace(decision.Body)
	if decision.Status != MRApproved && decision.Status != MRChangesRequested {
		return agentReviewDecision{}, errors.New("review response has invalid status")
	}
	if decision.Status == MRChangesRequested && decision.Body == "" {
		return agentReviewDecision{}, errors.New("changes_requested requires body")
	}
	if len(decision.Body) > 8*1024 {
		return agentReviewDecision{}, errors.New("review body exceeds limit")
	}
	return decision, nil
}

func (w *ReviewWorker) wait(ctx context.Context, claim reviewJobClaim, merge bool, reason string) error {
	status := reviewJobWaitingReview
	delay := 15 * time.Second
	if merge {
		status = reviewJobWaitingMerge
		delay = 30 * time.Second
	}
	now := time.Now().UTC()
	_, err := w.db.ExecContext(ctx, `update mr_review_jobs
		set status=?,processing_started_at=null,next_retry_at=?,last_error=?,updated_at=?
		where id=? and status=?`,
		status, now.Add(delay).Format(time.RFC3339Nano), reason, now.Format(time.RFC3339Nano), claim.ID, claim.Phase)
	return err
}

func (w *ReviewWorker) retry(ctx context.Context, claim reviewJobClaim, merge bool, reason string) (int, error) {
	counter := "review_attempts"
	status := reviewJobRetryReview
	if merge {
		counter = "merge_attempts"
		status = reviewJobRetryMerge
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var attempts int
	if err := tx.QueryRowContext(ctx, `select `+counter+`+1 from mr_review_jobs where id=? and status=?`, claim.ID, claim.Phase).Scan(&attempts); err != nil {
		return 0, err
	}
	delay := boundedReviewBackoff(attempts)
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `update mr_review_jobs
		set status=?,`+counter+`=?,processing_started_at=null,next_retry_at=?,last_error=?,updated_at=?
		where id=? and status=?`,
		status, attempts, now.Add(delay).Format(time.RFC3339Nano), reason, now.Format(time.RFC3339Nano), claim.ID, claim.Phase)
	if err != nil {
		return attempts, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return attempts, ErrStateConflict
	}
	return attempts, tx.Commit()
}

func (w *ReviewWorker) blockFromRetry(ctx context.Context, claim reviewJobClaim, taskID int64, code, body string) error {
	expected := reviewJobRetryReview
	if claim.Phase == reviewJobMerging {
		expected = reviewJobRetryMerge
	}
	claim.Phase = expected
	return w.block(ctx, claim, taskID, code, body)
}

func (w *ReviewWorker) block(ctx context.Context, claim reviewJobClaim, taskID int64, code, body string) error {
	now := time.Now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `update mr_review_jobs
		set status=?,blocked_code=?,processing_started_at=null,next_retry_at=null,last_error='',updated_at=?
		where id=? and status=? and blocked_code=''`,
		reviewJobBlocked, code, nowText, claim.ID, claim.Phase)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil
	}
	if taskID == 0 {
		_ = tx.QueryRowContext(ctx, `select task_id from merge_requests where id=?`, claim.MRID).Scan(&taskID)
	}
	if taskID <= 0 {
		return errors.New("blocked review task is unavailable")
	}
	body = limitReviewText(redactReviewText(body), 2048)
	if _, err := tx.ExecContext(ctx, `insert into issues(task_id,mr_id,title,body,status,blocking,created_by,created_at)
		values(?,?,?,?, 'blocking',1,'mr-review-worker',?)`,
		taskID, claim.MRID, "自动评审需要人工处理", body, nowText); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"mr_id": claim.MRID, "code": code, "status": reviewJobBlocked})
	event, err := events.InsertTx(ctx, tx, events.Event{
		TaskID:  &taskID,
		Type:    "mr.auto_review.blocked",
		Actor:   "mr-review-worker",
		Payload: payload,
	})
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	w.service.bus.Notify(event)
	return nil
}

func (w *ReviewWorker) complete(ctx context.Context, jobID int64) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := w.db.ExecContext(ctx, `update mr_review_jobs
		set status=?,processing_started_at=null,next_retry_at=null,last_error='',updated_at=?
		where id=?`, reviewJobCompleted, now, jobID)
	return err
}

func boundedReviewBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := 15 * time.Second
	for i := 1; i < attempt && delay < 5*time.Minute; i++ {
		delay *= 2
	}
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}

func validTestEvidence(tests []TestEvidence) (bool, string) {
	success := false
	for _, test := range tests {
		switch strings.ToLower(strings.TrimSpace(test.Status)) {
		case "pass", "passed", "success", "succeeded", "ok":
			success = true
		case "skip", "skipped":
		default:
			return false, "测试证据包含失败、未完成或未知状态，请修复并提供明确通过的测试后重新提交。"
		}
	}
	if !success {
		return false, "完成报告至少需要一项明确通过的测试证据。"
	}
	return true, ""
}

func containsHighRisk(risks []string) bool {
	for _, risk := range risks {
		lower := strings.ToLower(risk)
		for _, marker := range []string{
			"高风险", "部署", "生产", "删除", "删库", "迁移", "权限", "扩权", "密钥",
			"high risk", "deploy", "production", "drop table", "truncate table", "credential", "secret",
		} {
			if strings.Contains(lower, marker) {
				return true
			}
		}
	}
	return false
}

func safeReviewPath(value string) (string, error) {
	value = filepath.ToSlash(strings.TrimSpace(value))
	if value == "" || strings.ContainsRune(value, 0) || filepath.IsAbs(value) {
		return "", errors.New("invalid review path")
	}
	clean := filepath.ToSlash(filepath.Clean(value))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("review path escapes repository")
	}
	return clean, nil
}

func sensitiveReviewPath(value string) bool {
	lower := strings.ToLower(filepath.ToSlash(value))
	base := filepath.Base(lower)
	switch base {
	case ".env", "env", "credentials", "credentials.json", "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519":
		return true
	}
	if strings.HasPrefix(base, ".env.") ||
		strings.HasSuffix(base, ".pem") ||
		strings.HasSuffix(base, ".key") ||
		strings.HasSuffix(base, ".p12") ||
		strings.HasSuffix(base, ".pfx") ||
		strings.HasSuffix(base, ".jks") {
		return true
	}
	for _, segment := range strings.Split(lower, "/") {
		if segment == "secrets" || segment == ".secrets" {
			return true
		}
	}
	return false
}

func splitNULPaths(value string) []string {
	parts := strings.Split(value, "\x00")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			result = append(result, filepath.ToSlash(part))
		}
	}
	return result
}

type cappedReviewWriter struct {
	buf       bytes.Buffer
	remaining int
	truncated bool
}

// Write 写入有容量上限的评审差异输出，并记录是否发生截断。
func (w *cappedReviewWriter) Write(value []byte) (int, error) {
	size := len(value)
	available := w.remaining
	if w.remaining > 0 {
		take := size
		if take > w.remaining {
			take = w.remaining
		}
		_, _ = w.buf.Write(value[:take])
		w.remaining -= take
	}
	if size > available {
		w.truncated = true
	}
	return size, nil
}

func runBoundedGit(ctx context.Context, dir string, limit int, args ...string) (string, bool, error) {
	if limit < 1 {
		return "", false, errors.New("invalid git output limit")
	}
	writer := &cappedReviewWriter{remaining: limit}
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	command.Stdout = writer
	command.Stderr = writer
	err := command.Run()
	return writer.buf.String(), writer.truncated, err
}

func nonEmptyStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, value)
		}
	}
	return result
}

func boundedReviewStrings(values []string, maxItems, maxLength int) []string {
	if len(values) > maxItems {
		values = values[:maxItems]
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, limitReviewText(redactReviewText(value), maxLength))
	}
	return result
}

func boundedReviewTests(values []TestEvidence) []TestEvidence {
	if len(values) > 50 {
		values = values[:50]
	}
	result := make([]TestEvidence, 0, len(values))
	for _, value := range values {
		result = append(result, TestEvidence{
			Command: limitReviewText(redactReviewText(value.Command), 1024),
			Status:  limitReviewText(redactReviewText(value.Status), 128),
			Summary: limitReviewText(redactReviewText(value.Summary), 1024),
		})
	}
	return result
}

func redactReviewText(value string) string {
	value = reviewPrivateKeyPattern.ReplaceAllString(value, "[REDACTED PRIVATE KEY]")
	value = redactReviewNestedSecrets(value)
	value = reviewSecretLinePattern.ReplaceAllString(value, "$1 [REDACTED]")
	value = reviewSecretPattern.ReplaceAllString(value, "$1=[REDACTED]")
	value = reviewSKPattern.ReplaceAllString(value, "[REDACTED]")
	value = reviewJWTPattern.ReplaceAllString(value, "[REDACTED]")
	value = reviewBearerPattern.ReplaceAllString(value, "Bearer [REDACTED]")
	value = reviewCloudKeyPattern.ReplaceAllString(value, "[REDACTED]")
	return reviewLongSecretPattern.ReplaceAllString(value, "[REDACTED]")
}

func redactReviewNestedSecrets(value string) string {
	lines := strings.Split(value, "\n")
	state := reviewMultilineSecretState{}
	for index, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			state = reviewMultilineSecretState{}
			continue
		}
		marker, content := reviewDiffContent(line)
		trimmed := strings.TrimSpace(content)
		indent := len(content) - len(strings.TrimLeft(content, " \t"))
		switch state.mode {
		case reviewSecretIndentBlock:
			if trimmed == "" {
				continue
			}
			if indent > state.indent || (indent == state.indent && strings.HasPrefix(trimmed, "-")) {
				lines[index] = redactReviewMultilineLine(marker, indent)
				continue
			}
			state = reviewMultilineSecretState{}
		case reviewSecretBracketBlock:
			if trimmed != "" {
				lines[index] = redactReviewMultilineLine(marker, indent)
			}
			state.depth += reviewBracketDelta(content)
			if state.depth <= 0 {
				state = reviewMultilineSecretState{}
			}
			continue
		case reviewSecretTripleQuoted:
			if trimmed != "" {
				lines[index] = redactReviewMultilineLine(marker, indent)
			}
			if strings.Count(content, state.delimiter)%2 == 1 {
				state = reviewMultilineSecretState{}
			}
			continue
		case reviewSecretBackslash:
			if trimmed != "" {
				lines[index] = redactReviewMultilineLine(marker, indent)
			}
			if !strings.HasSuffix(strings.TrimSpace(content), `\`) {
				state = reviewMultilineSecretState{}
			}
			continue
		}
		match := reviewSecretKeyPattern.FindStringIndex(content)
		if match == nil {
			continue
		}
		state = startReviewMultilineSecret(strings.TrimSpace(content[match[1]:]), indent)
	}
	return strings.Join(lines, "\n")
}

type reviewMultilineSecretMode uint8

const (
	reviewSecretSingleLine reviewMultilineSecretMode = iota
	reviewSecretIndentBlock
	reviewSecretBracketBlock
	reviewSecretTripleQuoted
	reviewSecretBackslash
)

type reviewMultilineSecretState struct {
	mode      reviewMultilineSecretMode
	indent    int
	depth     int
	delimiter string
}

func startReviewMultilineSecret(remainder string, indent int) reviewMultilineSecretState {
	switch remainder {
	case "", "|", "|-", "|+", ">", ">-", ">+":
		return reviewMultilineSecretState{mode: reviewSecretIndentBlock, indent: indent}
	}
	if strings.HasSuffix(remainder, `\`) {
		return reviewMultilineSecretState{mode: reviewSecretBackslash}
	}
	for _, delimiter := range []string{`"""`, `'''`} {
		if strings.HasPrefix(remainder, delimiter) && strings.Count(remainder, delimiter)%2 == 1 {
			return reviewMultilineSecretState{mode: reviewSecretTripleQuoted, delimiter: delimiter}
		}
	}
	if strings.HasPrefix(remainder, "{") || strings.HasPrefix(remainder, "[") {
		if depth := reviewBracketDelta(remainder); depth > 0 {
			return reviewMultilineSecretState{mode: reviewSecretBracketBlock, depth: depth}
		}
	}
	return reviewMultilineSecretState{}
}

func redactReviewMultilineLine(marker string, indent int) string {
	return marker + strings.Repeat(" ", indent) + "[REDACTED MULTILINE SECRET]"
}

func reviewBracketDelta(value string) int {
	depth := 0
	var quote rune
	escaped := false
	for _, current := range value {
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if current == '\\' {
				escaped = true
				continue
			}
			if current == quote {
				quote = 0
			}
			continue
		}
		if current == '"' || current == '\'' {
			quote = current
			continue
		}
		switch current {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		}
	}
	return depth
}

func reviewDiffContent(line string) (string, string) {
	if len(line) == 0 {
		return "", line
	}
	if (line[0] == '+' || line[0] == '-' || line[0] == ' ') &&
		!strings.HasPrefix(line, "+++") &&
		!strings.HasPrefix(line, "---") {
		return line[:1], line[1:]
	}
	return "", line
}

func limitReviewText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "\n[TRUNCATED]"
}
