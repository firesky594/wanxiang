package db

import (
	"context"
	"database/sql"
	"fmt"
)

func Migrate(ctx context.Context, conn *sql.DB) error {
	stmts := []string{
		`create table if not exists users (id integer primary key, username text not null unique, password_hash text not null, created_at text not null)`,
		`create table if not exists admin_sessions (id integer primary key, user_id integer not null, token_hash text not null unique, expires_at text not null, created_at text not null)`,
		`create table if not exists admin_bootstrap (id integer primary key check(id = 1), completed_at text not null)`,
		`create table if not exists agent_registry (id integer primary key, name text not null unique, role text not null, dir text not null, status text not null, last_heartbeat text, current_task_id integer, current_model text)`,
		`create table if not exists agent_tokens (id integer primary key, agent_name text not null, token_hash text not null, scopes text not null, expires_at text, created_at text not null)`,
		`create table if not exists agent_config_versions (id integer primary key, agent_name text not null, commit_hash text not null, summary text not null, task_id integer, created_at text not null)`,
		`create table if not exists projects (id integer primary key, slug text not null unique, dir text not null, status text not null, main_commit text, remote_url text not null, created_at text not null)`,
		`create table if not exists tasks (id integer primary key, project_id integer, title text not null, description text not null, status text not null, priority integer not null default 0, manager_summary text not null default '', created_at text not null)`,
		`create table if not exists task_steps (id integer primary key, task_id integer not null, agent_name text not null, kind text not null, status text not null, input text not null default '', output text not null default '', created_at text not null, completed_at text, lease_id text not null default '', lease_version integer not null default 0, lease_expires_at text, last_heartbeat_at text, checkpoint_id integer, attempt integer not null default 0, interrupted_at text, resume_deadline text)`,
		`create table if not exists workflow_edges (id integer primary key, task_id integer not null, from_step_id integer, to_step_id integer, label text not null, created_at text not null)`,
		`create table if not exists merge_requests (id integer primary key, project_id integer not null, task_id integer not null, title text not null, source_branch text not null, target_branch text not null, status text not null, test_status text not null default 'pending', manager_status text not null default 'pending', created_by text not null, created_at text not null, merged_at text)`,
		`create table if not exists mr_reviews (id integer primary key, mr_id integer not null, reviewer text not null, role text not null, status text not null, body text not null, created_at text not null)`,
		`create table if not exists completion_reports (id integer primary key, project_id integer not null, task_id integer not null, step_id integer not null, lease_id text not null, lease_version integer not null, agent_name text not null, agent_role text not null, version integer not null, source_branch text not null, checkpoint_commit text not null, head_commit text not null, completed_json text not null, incomplete_json text not null, key_files_json text not null, tests_json text not null, risks_json text not null, dependencies_json text not null, merge_order_json text not null, user_decision text not null default '', created_at text not null, unique(task_id,step_id,version), unique(lease_id,version))`,
		`create index if not exists idx_completion_reports_task_step on completion_reports(task_id,step_id,version)`,
		`create table if not exists manager_notifications (id integer primary key, project_id integer not null, task_id integer not null, mr_id integer not null unique, report_id integer not null, project_lead text not null, main_commit text not null, payload_json text not null, status text not null default 'pending', created_at text not null, consumed_at text)`,
		`create index if not exists idx_manager_notifications_status on manager_notifications(status,created_at)`,
		`create table if not exists task_plan_versions (id integer primary key, task_id integer not null, version integer not null, source_snapshot_id integer, source_decision_id integer, status text not null, summary text not null default '', created_at text not null, unique(task_id,version))`,
		`create table if not exists delivery_snapshots (id integer primary key, task_id integer not null, project_id integer not null, version integer not null, manager_notification_id integer not null unique, main_commit text not null, status text not null, summary text not null, summary_hash text not null, evidence_json text not null, created_by text not null, created_at text not null, unique(task_id,version))`,
		`create index if not exists idx_delivery_snapshots_task_status on delivery_snapshots(task_id,status,version)`,
		`create table if not exists acceptance_decisions (id integer primary key, snapshot_id integer not null, task_id integer not null, decision text not null, comment text not null default '', idempotency_key text not null unique, created_by text not null, created_at text not null)`,
		`create table if not exists rework_rounds (id integer primary key, task_id integer not null, source_snapshot_id integer not null, decision_id integer not null unique, round integer not null, plan_version integer not null, reason text not null, status text not null, checkpoint_json text not null default '{}', last_error text not null default '', created_by text not null, created_at text not null, completed_at text, unique(task_id,round), unique(task_id,plan_version))`,
		`create index if not exists idx_rework_rounds_status on rework_rounds(status,created_at)`,
		`create table if not exists issues (id integer primary key, task_id integer, mr_id integer, title text not null, body text not null, status text not null, blocking integer not null default 0, created_by text not null, created_at text not null, closed_at text)`,
		`create table if not exists runtime_events (id integer primary key, task_id integer, event_type text not null, actor text not null, payload_json text not null, created_at text not null)`,
		`create table if not exists token_usage (id integer primary key, task_id integer, step_id integer, agent_name text not null, model text not null, input_tokens integer not null, output_tokens integer not null, created_at text not null)`,
		`create table if not exists remote_sync_jobs (id integer primary key, project_id integer not null, mr_id integer, kind text not null, status text not null, requested_by text not null, created_at text not null, completed_at text)`,
		`create table if not exists audit_logs (id integer primary key, actor text not null, action text not null, target text not null, payload_json text not null, created_at text not null)`,
		`create table if not exists agent_match_decisions (id integer primary key, task_id integer not null, step_id integer not null, selected_agent text, score real not null default 0, reasons_json text not null, rejections_json text not null, created_by text not null, status text not null, created_at text not null)`,
		`create table if not exists task_assignments (id integer primary key, task_id integer not null, step_id integer not null unique, agent_name text not null, reports_to text, status text not null, decision_id integer not null, created_at text not null)`,
		`create table if not exists team_decisions (id integer primary key, task_id integer not null unique, project_lead text, requires_lead integer not null default 0, reason text not null, created_at text not null)`,
		`create table if not exists project_workspaces (id integer primary key, project_id integer not null, task_id integer not null, step_id integer not null unique, assignment_id integer not null, agent_name text not null, reports_to text, branch_name text not null unique, worktree_path text not null unique, base_commit text not null, provision_commit text not null default '', write_scope_json text not null, metadata_hash text not null, status text not null, last_error text not null default '', created_at text not null, updated_at text not null, cleaned_at text)`,
		`create table if not exists task_step_leases (id integer primary key, task_id integer not null, step_id integer not null, agent_name text not null, lease_id text not null unique, lease_version integer not null, status text not null, branch_name text not null default '', worktree_path text not null default '', acquired_at text not null, expires_at text not null, last_heartbeat_at text, interrupted_at text, resume_deadline text, revoked_at text, revoked_reason text not null default '', created_at text not null, updated_at text not null, unique(step_id, lease_version))`,
		`create index if not exists idx_task_step_leases_step_status on task_step_leases(step_id, status)`,
		`create index if not exists idx_task_step_leases_expiry on task_step_leases(status, expires_at)`,
		`create table if not exists task_checkpoints (id integer primary key, task_id integer not null, step_id integer not null, lease_id text not null, idempotency_key text not null, git_commit text not null default '', branch_name text not null default '', clean integer not null default 0, files_json text not null default '[]', tests_json text not null default '[]', summary_json text not null, summary_hash text not null, high_risk integer not null default 0, created_at text not null, unique(lease_id, idempotency_key))`,
		`create index if not exists idx_task_checkpoints_step_created on task_checkpoints(step_id, created_at)`,
		`create table if not exists step_reassignments (id integer primary key, task_id integer not null, step_id integer not null, from_agent text not null, to_agent text not null, from_lease_id text not null, to_lease_id text not null default '', checkpoint_id integer, attempt integer not null, reason text not null, status text not null, from_branch text not null default '', from_worktree text not null default '', to_branch text not null default '', to_worktree text not null default '', created_by text not null, created_at text not null, completed_at text)`,
		`create index if not exists idx_step_reassignments_step on step_reassignments(step_id, created_at)`,
		`create table if not exists executor_runs (id integer primary key, task_id integer not null, step_id integer not null, agent_name text not null, lease_id text not null unique, lease_version integer not null, pid integer, status text not null, request_count integer not null default 0, input_tokens integer not null default 0, output_tokens integer not null default 0, exit_code integer, last_heartbeat_at text, error_summary text not null default '', created_at text not null, started_at text, exited_at text, updated_at text not null)`,
		`create index if not exists idx_executor_runs_step_status on executor_runs(step_id, status)`,
		`create index if not exists idx_executor_runs_agent_status on executor_runs(agent_name, status)`,
		`create table if not exists executor_actions (id integer primary key, run_id integer not null, sequence integer not null, action_type text not null, relative_path text not null default '', status text not null, result_summary text not null default '', result_hash text not null default '', created_at text not null, unique(run_id, sequence))`,
		`create index if not exists idx_executor_actions_run on executor_actions(run_id, sequence)`,
	}
	for _, stmt := range stmts {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	columns := []struct {
		name       string
		definition string
	}{
		{"lease_id", "text not null default ''"},
		{"lease_version", "integer not null default 0"},
		{"lease_expires_at", "text"},
		{"last_heartbeat_at", "text"},
		{"checkpoint_id", "integer"},
		{"attempt", "integer not null default 0"},
		{"interrupted_at", "text"},
		{"resume_deadline", "text"},
	}
	for _, column := range columns {
		if err := ensureColumn(ctx, conn, "task_steps", column.name, column.definition); err != nil {
			return err
		}
	}
	for _, column := range []struct {
		table, name, definition string
	}{
		{"task_step_leases", "branch_name", "text not null default ''"},
		{"task_step_leases", "worktree_path", "text not null default ''"},
		{"task_step_leases", "revoked_reason", "text not null default ''"},
		{"step_reassignments", "from_branch", "text not null default ''"},
		{"step_reassignments", "from_worktree", "text not null default ''"},
		{"step_reassignments", "to_branch", "text not null default ''"},
		{"step_reassignments", "to_worktree", "text not null default ''"},
		{"merge_requests", "report_id", "integer"},
		{"merge_requests", "step_id", "integer"},
		{"merge_requests", "lease_id", "text not null default ''"},
		{"merge_requests", "report_version", "integer not null default 0"},
		{"merge_requests", "source_commit", "text not null default ''"},
		{"merge_requests", "project_lead", "text not null default ''"},
		{"merge_requests", "reviewed_at", "text"},
		{"merge_requests", "approved_at", "text"},
		{"merge_requests", "merged_by", "text not null default ''"},
		{"merge_requests", "merge_commit", "text not null default ''"},
		{"manager_notifications", "last_error", "text not null default ''"},
		{"manager_notifications", "next_retry_at", "text"},
		{"task_steps", "plan_version", "integer not null default 1"},
		{"workflow_edges", "plan_version", "integer not null default 1"},
	} {
		if err := ensureColumn(ctx, conn, column.table, column.name, column.definition); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, `create unique index if not exists idx_merge_requests_report_id on merge_requests(report_id) where report_id is not null`); err != nil {
		return err
	}
	return nil
}

func ensureColumn(ctx context.Context, conn *sql.DB, table, column, definition string) error {
	// All identifiers and definitions are migration constants owned by this package.
	query := fmt.Sprintf("select count(*) from pragma_table_info('%s') where name=?", table)
	var count int
	if err := conn.QueryRowContext(ctx, query, column).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	_, err := conn.ExecContext(ctx, fmt.Sprintf("alter table %s add column %s %s", table, column, definition))
	return err
}
