package deliveries

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"wanxiang-agent/server/internal/events"
)

type Service struct {
	db  *sql.DB
	bus *events.Bus
}

func NewService(db *sql.DB, buses ...*events.Bus) *Service {
	var bus *events.Bus
	if len(buses) > 0 {
		bus = buses[0]
	}
	return &Service{db: db, bus: bus}
}

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
	res, err := tx.ExecContext(ctx, `insert into delivery_snapshots(task_id,project_id,version,manager_notification_id,main_commit,status,summary,summary_hash,evidence_json,created_by,created_at) values(?,?,?,?,?,'awaiting_acceptance',?,?,?,'manager',?)`, taskID, projectID, version, notificationID, mainCommit, summary, hash, string(encoded), now)
	if err != nil {
		if item, findErr := s.snapshotByNotification(ctx, notificationID); findErr == nil {
			return item, nil
		}
		return Snapshot{}, err
	}
	id, _ := res.LastInsertId()
	if _, err = tx.ExecContext(ctx, `update manager_notifications set status='consumed',consumed_at=?,last_error='',next_retry_at=null where task_id=? and status='pending'`, now, taskID); err != nil {
		return Snapshot{}, err
	}
	if _, err = tx.ExecContext(ctx, `update tasks set status='awaiting_acceptance' where id=?`, taskID); err != nil {
		return Snapshot{}, err
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
	result := Evidence{MergeRequests: []MergeEvidence{}, Reports: []ReportEvidence{}, Tests: []TestEvidence{}, Risks: []string{}, Incomplete: []string{}}
	rows, err := s.db.QueryContext(ctx, `select mr.id,mr.step_id,mr.status,mr.source_commit,mr.merge_commit,cr.id,cr.agent_name,cr.completed_json,cr.incomplete_json,cr.key_files_json,cr.tests_json,cr.risks_json from merge_requests mr join completion_reports cr on cr.id=mr.report_id join task_steps st on st.id=mr.step_id where mr.task_id=? and st.plan_version=? order by mr.id`, taskID, planVersion)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var m MergeEvidence
		var r ReportEvidence
		var completed, incomplete, keyFiles, tests, risks string
		if err := rows.Scan(&m.ID, &m.StepID, &m.Status, &m.SourceCommit, &m.MergeCommit, &r.ID, &m.AgentName, &completed, &incomplete, &keyFiles, &tests, &risks); err != nil {
			return result, err
		}
		r.StepID = m.StepID
		r.AgentName = m.AgentName
		r.Completed = decodeStrings(completed)
		r.KeyFiles = decodeStrings(keyFiles)
		result.MergeRequests = append(result.MergeRequests, m)
		result.Reports = append(result.Reports, r)
		result.Tests = append(result.Tests, decodeTests(tests)...)
		result.Risks = append(result.Risks, decodeStrings(risks)...)
		result.Incomplete = append(result.Incomplete, decodeStrings(incomplete)...)
	}
	return result, rows.Err()
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
	value := err.Error()
	for _, marker := range []string{"sk-", "Bearer ", "api_key"} {
		if i := strings.Index(strings.ToLower(value), strings.ToLower(marker)); i >= 0 {
			value = value[:i] + "[REDACTED]"
		}
	}
	if len(value) > 1024 {
		value = value[:1024]
	}
	return value
}
