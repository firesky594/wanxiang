package deliveries

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"wanxiang-agent/server/internal/events"
)

type Service struct {
	db  *sql.DB
	bus *events.Bus
}

// NewService 创建交付快照服务。
func NewService(db *sql.DB, buses ...*events.Bus) *Service {
	var bus *events.Bus
	if len(buses) > 0 {
		bus = buses[0]
	}
	return &Service{db: db, bus: bus}
}

// BuildSnapshot 汇总通知、合并请求与证据生成交付快照。
func (s *Service) BuildSnapshot(ctx context.Context, notificationID int64) (Snapshot, error) {
	if existing, err := s.snapshotByNotification(ctx, notificationID); err == nil {
		return existing, nil
	}
	var taskID, projectID int64
	var mainCommit, notificationStatus string
	err := s.db.QueryRowContext(ctx, `select task_id,project_id,main_commit,status from manager_notifications where id=?`, notificationID).Scan(&taskID, &projectID, &mainCommit, &notificationStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, ErrNotFound
	}
	if err != nil {
		return Snapshot{}, err
	}
	if notificationStatus == "consumed" {
		if item, err := s.latestForTask(ctx, taskID); err == nil {
			return item, nil
		}
	}
	if err := s.db.QueryRowContext(ctx, `select main_commit from manager_notifications where task_id=? and status in ('pending','processing') order by id desc limit 1`, taskID).Scan(&mainCommit); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, err
	}
	var planVersion int64
	if err := s.db.QueryRowContext(ctx, `select coalesce(max(version),1) from task_plan_versions where task_id=?`, taskID).Scan(&planVersion); err != nil {
		return Snapshot{}, err
	}
	var incomplete int
	if err := s.db.QueryRowContext(ctx, `select count(*) from task_steps where task_id=? and plan_version=? and status!='completed'`, taskID, planVersion).Scan(&incomplete); err != nil {
		return Snapshot{}, err
	}
	if incomplete > 0 {
		return Snapshot{}, ErrNotReady
	}
	var unmerged int
	if err := s.db.QueryRowContext(ctx, `select count(*) from task_steps st where st.task_id=? and st.plan_version=? and not exists(select 1 from merge_requests mr where mr.task_id=st.task_id and mr.step_id=st.id and mr.status='merged')`, taskID, planVersion).Scan(&unmerged); err != nil {
		return Snapshot{}, err
	}
	if unmerged > 0 {
		return Snapshot{}, ErrNotReady
	}
	var blockers int
	if err := s.db.QueryRowContext(ctx, `select count(*) from issues where task_id=? and blocking=1 and status not in ('resolved','closed')`, taskID).Scan(&blockers); err != nil {
		return Snapshot{}, err
	}
	if blockers > 0 {
		return Snapshot{}, ErrNotReady
	}
	evidence, err := s.collectEvidence(ctx, taskID, planVersion)
	if err != nil {
		return Snapshot{}, err
	}
	encoded, _ := json.Marshal(evidence)
	var version int64
	_ = s.db.QueryRowContext(ctx, `select coalesce(max(version),0)+1 from delivery_snapshots where task_id=?`, taskID).Scan(&version)
	summary := fmt.Sprintf("交付版本 %d：已合并 %d 个工作包，测试证据 %d 项，风险 %d 项，未完成项 %d 项。", version, len(evidence.MergeRequests), len(evidence.Tests), len(evidence.Risks), len(evidence.Incomplete))
	hashBytes := sha256.Sum256(append([]byte(summary), encoded...))
	hash := hex.EncodeToString(hashBytes[:])
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer tx.Rollback()
	var taskStatus string
	var currentVersion, stepCount, txIncomplete, txUnmerged, txBlockers int64
	if err = tx.QueryRowContext(ctx, `select status from tasks where id=?`, taskID).Scan(&taskStatus); err != nil {
		return Snapshot{}, err
	}
	if taskStatus != "merged" && taskStatus != "workspace_ready" {
		return Snapshot{}, ErrNotReady
	}
	if err = tx.QueryRowContext(ctx, `select coalesce(max(version),1) from task_plan_versions where task_id=?`, taskID).Scan(&currentVersion); err != nil || currentVersion != planVersion {
		return Snapshot{}, ErrNotReady
	}
	if err = tx.QueryRowContext(ctx, `select count(*),sum(case when status!='completed' then 1 else 0 end) from task_steps where task_id=? and plan_version=?`, taskID, planVersion).Scan(&stepCount, &txIncomplete); err != nil || stepCount == 0 || txIncomplete > 0 {
		return Snapshot{}, ErrNotReady
	}
	if err = tx.QueryRowContext(ctx, `select count(*) from task_steps st where st.task_id=? and st.plan_version=? and not exists(select 1 from merge_requests mr where mr.task_id=st.task_id and mr.step_id=st.id and mr.status='merged')`, taskID, planVersion).Scan(&txUnmerged); err != nil || txUnmerged > 0 {
		return Snapshot{}, ErrNotReady
	}
	if err = tx.QueryRowContext(ctx, `select count(*) from issues where task_id=? and blocking=1 and status not in ('resolved','closed')`, taskID).Scan(&txBlockers); err != nil || txBlockers > 0 {
		return Snapshot{}, ErrNotReady
	}
	res, err := tx.ExecContext(ctx, `insert into delivery_snapshots(task_id,project_id,version,manager_notification_id,main_commit,status,summary,summary_hash,evidence_json,created_by,created_at) values(?,?,?,?,?,'awaiting_acceptance',?,?,?,'manager',?)`, taskID, projectID, version, notificationID, mainCommit, summary, hash, string(encoded), now)
	if err != nil {
		if item, findErr := s.snapshotByNotification(ctx, notificationID); findErr == nil {
			return item, nil
		}
		return Snapshot{}, err
	}
	id, _ := res.LastInsertId()
	notificationRows, err := tx.QueryContext(ctx, `select id from manager_notifications where task_id=? and status in ('pending','processing') order by id`, taskID)
	if err != nil {
		return Snapshot{}, err
	}
	var notificationIDs []int64
	for notificationRows.Next() {
		var nid int64
		if err = notificationRows.Scan(&nid); err != nil {
			notificationRows.Close()
			return Snapshot{}, err
		}
		notificationIDs = append(notificationIDs, nid)
	}
	notificationRows.Close()
	for _, nid := range notificationIDs {
		if _, err = tx.ExecContext(ctx, `insert into delivery_snapshot_notifications(snapshot_id,notification_id) values(?,?)`, id, nid); err != nil {
			return Snapshot{}, err
		}
	}
	for _, risk := range evidence.HighRisk {
		if isHighRisk(risk) {
			title := fmt.Sprintf("交付版本 %d 高风险事项需单独确认", version)
			if _, err = tx.ExecContext(ctx, `insert into issues(task_id,title,body,status,blocking,created_by,created_at) values(?,?,?,'blocking',1,'manager',?)`, taskID, title, risk, now); err != nil {
				return Snapshot{}, err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `update manager_notifications set status='consumed',consumed_at=?,processing_started_at=null,last_error='',next_retry_at=null where task_id=? and status in ('pending','processing')`, now, taskID); err != nil {
		return Snapshot{}, err
	}
	updated, err := tx.ExecContext(ctx, `update tasks set status='awaiting_acceptance' where id=? and status=?`, taskID, taskStatus)
	if err != nil {
		return Snapshot{}, err
	}
	if changed, _ := updated.RowsAffected(); changed != 1 {
		return Snapshot{}, ErrNotReady
	}
	payload, _ := json.Marshal(map[string]any{"snapshot_id": id, "version": version, "main_commit": mainCommit})
	if _, err = tx.ExecContext(ctx, `insert into runtime_events(task_id,event_type,actor,payload_json,created_at) values(?,'delivery.snapshot.created','manager',?,?)`, taskID, string(payload), now); err != nil {
		return Snapshot{}, err
	}
	if err = tx.Commit(); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{ID: id, TaskID: taskID, ProjectID: projectID, Version: version, ManagerNotificationID: notificationID, MainCommit: mainCommit, Status: "awaiting_acceptance", Summary: summary, SummaryHash: hash, Evidence: evidence, CreatedBy: "manager", CreatedAt: now}, nil
}

func (s *Service) collectEvidence(ctx context.Context, taskID, planVersion int64) (Evidence, error) {
	result := Evidence{MergeRequests: []MergeEvidence{}, Reports: []ReportEvidence{}, Tests: []TestEvidence{}, Risks: []string{}, Incomplete: []string{}, WorkItems: []WorkItemEvidence{}, Reviews: []ReviewEvidence{}, UserDecisions: []string{}, HighRisk: []string{}}
	rows, err := s.db.QueryContext(ctx, `select mr.id,mr.step_id,mr.status,mr.source_commit,mr.merge_commit,cr.id,cr.agent_name,cr.completed_json,cr.incomplete_json,cr.key_files_json,cr.tests_json,cr.risks_json,cr.user_decision from merge_requests mr join completion_reports cr on cr.id=mr.report_id join task_steps st on st.id=mr.step_id where mr.task_id=? and st.plan_version=? order by mr.id`, taskID, planVersion)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var m MergeEvidence
		var r ReportEvidence
		var completed, incomplete, keyFiles, tests, risks, userDecision string
		if err := rows.Scan(&m.ID, &m.StepID, &m.Status, &m.SourceCommit, &m.MergeCommit, &r.ID, &m.AgentName, &completed, &incomplete, &keyFiles, &tests, &risks, &userDecision); err != nil {
			return result, err
		}
		r.StepID = m.StepID
		r.AgentName = m.AgentName
		r.Completed = decodeStrings(completed)
		r.KeyFiles = decodeStrings(keyFiles)
		m.AgentName = scrub(m.AgentName)
		r.AgentName = scrub(r.AgentName)
		for i := range r.Completed {
			r.Completed[i] = scrub(r.Completed[i])
		}
		for i := range r.KeyFiles {
			r.KeyFiles[i] = scrub(r.KeyFiles[i])
		}
		newTests := decodeTests(tests)
		for i := range newTests {
			newTests[i].Command = scrub(newTests[i].Command)
			newTests[i].Summary = scrub(newTests[i].Summary)
		}
		newRisks := decodeStrings(risks)
		for i := range newRisks {
			newRisks[i] = scrub(newRisks[i])
		}
		newIncomplete := decodeStrings(incomplete)
		for i := range newIncomplete {
			newIncomplete[i] = scrub(newIncomplete[i])
		}
		result.MergeRequests = append(result.MergeRequests, m)
		result.Reports = append(result.Reports, r)
		result.Tests = append(result.Tests, newTests...)
		result.Risks = append(result.Risks, newRisks...)
		result.Incomplete = append(result.Incomplete, newIncomplete...)
		if strings.TrimSpace(userDecision) != "" {
			result.UserDecisions = append(result.UserDecisions, scrub(userDecision))
		}
		for _, risk := range newRisks {
			if isHighRisk(risk) {
				result.HighRisk = append(result.HighRisk, risk)
			}
		}
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	stepRows, err := s.db.QueryContext(ctx, `select id,agent_name,kind,status,input from task_steps where task_id=? and plan_version=? order by id`, taskID, planVersion)
	if err != nil {
		return result, err
	}
	for stepRows.Next() {
		var w WorkItemEvidence
		var raw string
		if err = stepRows.Scan(&w.StepID, &w.AgentName, &w.Kind, &w.Status, &raw); err != nil {
			stepRows.Close()
			return result, err
		}
		w.AgentName = scrub(w.AgentName)
		w.Input = sanitizeJSON(raw)
		result.WorkItems = append(result.WorkItems, w)
	}
	stepRows.Close()
	reviewRows, err := s.db.QueryContext(ctx, `select r.mr_id,r.reviewer,r.role,r.status,r.body,r.created_at from mr_reviews r join merge_requests mr on mr.id=r.mr_id join task_steps st on st.id=mr.step_id where mr.task_id=? and st.plan_version=? order by r.id`, taskID, planVersion)
	if err != nil {
		return result, err
	}
	for reviewRows.Next() {
		var r ReviewEvidence
		if err = reviewRows.Scan(&r.MRID, &r.Reviewer, &r.Role, &r.Status, &r.Body, &r.CreatedAt); err != nil {
			reviewRows.Close()
			return result, err
		}
		r.Reviewer = scrub(r.Reviewer)
		r.Body = scrub(r.Body)
		result.Reviews = append(result.Reviews, r)
	}
	reviewRows.Close()
	all := append([]string{}, result.Risks...)
	all = append(all, result.Incomplete...)
	all = append(all, result.UserDecisions...)
	for _, test := range result.Tests {
		all = append(all, test.Command, test.Summary)
	}
	for _, work := range result.WorkItems {
		all = append(all, string(work.Input))
	}
	seen := map[string]bool{}
	result.HighRisk = result.HighRisk[:0]
	for _, value := range all {
		if isHighRisk(value) && !seen[value] {
			result.HighRisk = append(result.HighRisk, value)
			seen[value] = true
		}
	}
	return result, reviewRows.Err()
}

func (s *Service) snapshotByNotification(ctx context.Context, id int64) (Snapshot, error) {
	return s.loadSnapshot(ctx, `where manager_notification_id=?`, id)
}
func (s *Service) latestForTask(ctx context.Context, id int64) (Snapshot, error) {
	return s.loadSnapshot(ctx, `where task_id=? order by version desc limit 1`, id)
}
func (s *Service) loadSnapshot(ctx context.Context, suffix string, arg any) (Snapshot, error) {
	var item Snapshot
	var evidence string
	err := s.db.QueryRowContext(ctx, `select id,task_id,project_id,version,manager_notification_id,main_commit,status,summary,summary_hash,evidence_json,created_by,created_at from delivery_snapshots `+suffix, arg).Scan(&item.ID, &item.TaskID, &item.ProjectID, &item.Version, &item.ManagerNotificationID, &item.MainCommit, &item.Status, &item.Summary, &item.SummaryHash, &evidence, &item.CreatedBy, &item.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, ErrNotFound
	}
	if err != nil {
		return Snapshot{}, err
	}
	_ = json.Unmarshal([]byte(evidence), &item.Evidence)
	return item, nil
}

func redactError(err error) string {
	if err == nil {
		return ""
	}
	value := scrub(err.Error())
	if len(value) > 1024 {
		value = value[:1024]
	}
	return value
}

func isHighRisk(value string) bool {
	lower := strings.ToLower(value)
	for _, word := range []string{"部署", "生产", "删除", "删库", "迁移", "权限", "扩权", "密钥", "secret", "credential", "deploy", "production", "drop table", "truncate table"} {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

func scrub(value string) string {
	lower := strings.ToLower(value)
	for _, marker := range []string{"authorization=", "authorization:", "bearer ", "bearer:", "sk-", "api_key=", "api-key=", "apikey=", "token=", "access_token=", "password=", "secret=", "access_key=", "private_key=", "aws_access_key_id=", "aws_secret_access_key="} {
		if i := strings.Index(lower, marker); i >= 0 {
			return value[:i] + "[REDACTED]"
		}
	}
	value = jwtPattern.ReplaceAllString(value, "[REDACTED]")
	return value
}

var jwtPattern = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)

func sanitizeJSON(raw string) json.RawMessage {
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		encoded, _ := json.Marshal(scrub(raw))
		return encoded
	}
	value = sanitizeValue(value)
	encoded, _ := json.Marshal(value)
	return encoded
}

func sanitizeValue(value any) any {
	switch v := value.(type) {
	case string:
		return scrub(v)
	case []any:
		for i := range v {
			v[i] = sanitizeValue(v[i])
		}
		return v
	case map[string]any:
		for key, item := range v {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "password") || strings.Contains(lower, "api_key") || strings.Contains(lower, "env") {
				v[key] = "[REDACTED]"
			} else {
				v[key] = sanitizeValue(item)
			}
		}
		return v
	default:
		return value
	}
}

// List 分页查询交付快照。
func (s *Service) List(ctx context.Context, taskID *int64, limit, offset int) ([]Snapshot, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	query := `select id,task_id,project_id,version,manager_notification_id,main_commit,status,summary,summary_hash,evidence_json,created_by,created_at from delivery_snapshots`
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
	items := []Snapshot{}
	for rows.Next() {
		var item Snapshot
		var raw string
		if err = rows.Scan(&item.ID, &item.TaskID, &item.ProjectID, &item.Version, &item.ManagerNotificationID, &item.MainCommit, &item.Status, &item.Summary, &item.SummaryHash, &raw, &item.CreatedBy, &item.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(raw), &item.Evidence)
		items = append(items, item)
	}
	return items, rows.Err()
}

// Detail 查询交付快照及关联明细。
func (s *Service) Detail(ctx context.Context, id int64) (Detail, error) {
	snap, err := s.loadSnapshot(ctx, `where id=?`, id)
	if err != nil {
		return Detail{}, err
	}
	detail := Detail{Snapshot: snap, Decisions: []AcceptanceDecision{}, ReworkRounds: []ReworkRound{}}
	rows, err := s.db.QueryContext(ctx, `select id,snapshot_id,task_id,decision,comment,created_by,created_at from acceptance_decisions where snapshot_id=? order by id`, id)
	if err != nil {
		return Detail{}, err
	}
	for rows.Next() {
		var d AcceptanceDecision
		if err = rows.Scan(&d.ID, &d.SnapshotID, &d.TaskID, &d.Decision, &d.Comment, &d.CreatedBy, &d.CreatedAt); err != nil {
			rows.Close()
			return Detail{}, err
		}
		detail.Decisions = append(detail.Decisions, d)
		detail.Decisions[len(detail.Decisions)-1].Comment = scrub(detail.Decisions[len(detail.Decisions)-1].Comment)
	}
	rows.Close()
	rounds, err := s.ListRework(ctx, snap.TaskID)
	if err != nil {
		return Detail{}, err
	}
	for _, r := range rounds {
		if r.SourceSnapshotID == id {
			detail.ReworkRounds = append(detail.ReworkRounds, r)
		}
	}
	return detail, nil
}

// ListRework 查询任务的返工轮次记录。
func (s *Service) ListRework(ctx context.Context, taskID int64) ([]ReworkRound, error) {
	rows, err := s.db.QueryContext(ctx, `select id,task_id,source_snapshot_id,decision_id,round,plan_version,reason,status,last_error,created_by,created_at from rework_rounds where task_id=? order by round`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ReworkRound{}
	for rows.Next() {
		var r ReworkRound
		if err = rows.Scan(&r.ID, &r.TaskID, &r.SourceSnapshotID, &r.DecisionID, &r.Round, &r.PlanVersion, &r.Reason, &r.Status, &r.LastError, &r.CreatedBy, &r.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}
