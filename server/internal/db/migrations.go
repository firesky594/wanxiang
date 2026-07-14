package db

import (
	"context"
	"database/sql"
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
		`create table if not exists task_steps (id integer primary key, task_id integer not null, agent_name text not null, kind text not null, status text not null, input text not null default '', output text not null default '', created_at text not null, completed_at text)`,
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
		`create table if not exists project_workspaces (id integer primary key, project_id integer not null, task_id integer not null, step_id integer not null unique, assignment_id integer not null, agent_name text not null, reports_to text, branch_name text not null unique, worktree_path text not null unique, base_commit text not null, write_scope_json text not null, metadata_hash text not null, status text not null, last_error text not null default '', created_at text not null, updated_at text not null, cleaned_at text)`,
	}
	for _, stmt := range stmts {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
