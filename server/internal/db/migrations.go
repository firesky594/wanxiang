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
	} {
		if err := ensureColumn(ctx, conn, column.table, column.name, column.definition); err != nil {
			return err
		}
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
