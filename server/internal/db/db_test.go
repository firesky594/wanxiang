package db

import (
	"context"
	"testing"
)

func TestMigrateCreatesCoreTables(t *testing.T) {
	conn, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()
	if err := Migrate(context.Background(), conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	for _, table := range []string{
		"users", "admin_sessions", "admin_bootstrap", "agent_registry", "agent_tokens", "agent_config_versions",
		"projects", "tasks", "task_steps", "workflow_edges",
		"merge_requests", "mr_reviews", "issues", "runtime_events",
		"token_usage", "remote_sync_jobs", "audit_logs",
		"agent_match_decisions", "task_assignments", "team_decisions", "project_workspaces",
		"task_step_leases", "task_checkpoints", "step_reassignments",
		"executor_runs", "executor_actions",
	} {
		var name string
		err := conn.QueryRow(`select name from sqlite_master where type='table' and name=?`, table).Scan(&name)
		if err != nil {
			t.Fatalf("table %s not created: %v", table, err)
		}
	}
}

func TestMigrateUpgradesLegacyTeamDecisionUniqueness(t *testing.T) {
	conn, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.Exec(`create table team_decisions (id integer primary key, task_id integer not null unique, project_lead text, requires_lead integer not null default 0, reason text not null, created_at text not null)`); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(`insert into team_decisions(task_id,project_lead,requires_lead,reason,created_at) values(1,'lead',1,'old','now')`); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(`insert into team_decisions(task_id,plan_version,project_lead,requires_lead,reason,created_at) values(1,2,'lead',1,'rework','now')`); err != nil {
		t.Fatalf("version 2 rejected: %v", err)
	}
	var count int
	_ = conn.QueryRow(`select count(*) from team_decisions where task_id=1`).Scan(&count)
	if count != 2 {
		t.Fatalf("count=%d", count)
	}
}

func TestMission07MigrationCreatesReportReviewAndNotificationSchema(t *testing.T) {
	conn, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := Migrate(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(t.Context(), conn); err != nil {
		t.Fatalf("second migration: %v", err)
	}

	required := map[string][]string{
		"completion_reports":    {"project_id", "task_id", "step_id", "lease_id", "lease_version", "agent_name", "agent_role", "version", "source_branch", "checkpoint_commit", "head_commit", "completed_json", "incomplete_json", "key_files_json", "tests_json", "risks_json", "dependencies_json", "merge_order_json", "user_decision", "created_at"},
		"merge_requests":        {"report_id", "step_id", "lease_id", "report_version", "source_commit", "project_lead", "reviewed_at", "approved_at", "merged_by", "merge_commit"},
		"manager_notifications": {"project_id", "task_id", "mr_id", "report_id", "project_lead", "main_commit", "payload_json", "status", "created_at"},
	}
	for table, columns := range required {
		for _, column := range columns {
			var count int
			if err := conn.QueryRow(`select count(*) from pragma_table_info(?) where name=?`, table, column).Scan(&count); err != nil {
				t.Fatalf("%s.%s: %v", table, column, err)
			}
			if count != 1 {
				t.Errorf("missing %s.%s", table, column)
			}
		}
	}

	if _, err := conn.Exec(`insert into completion_reports(project_id,task_id,step_id,lease_id,lease_version,agent_name,agent_role,version,source_branch,checkpoint_commit,head_commit,completed_json,incomplete_json,key_files_json,tests_json,risks_json,dependencies_json,merge_order_json,user_decision,created_at) values(1,1,1,'lease',1,'agent','worker',1,'agent/a/task','abc','abc','[]','[]','[]','[]','[]','[]','[]','','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`insert into completion_reports(project_id,task_id,step_id,lease_id,lease_version,agent_name,agent_role,version,source_branch,checkpoint_commit,head_commit,completed_json,incomplete_json,key_files_json,tests_json,risks_json,dependencies_json,merge_order_json,user_decision,created_at) values(1,1,1,'lease',1,'agent','worker',1,'agent/a/task','abc','abc','[]','[]','[]','[]','[]','[]','[]','','now')`); err == nil {
		t.Fatal("duplicate report version accepted")
	}
}

func TestExecutorMigrationIsIdempotentAndConstrained(t *testing.T) {
	conn, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for i := 0; i < 2; i++ {
		if err := Migrate(context.Background(), conn); err != nil {
			t.Fatalf("Migrate pass %d: %v", i+1, err)
		}
	}
	now := "2026-07-15T00:00:00Z"
	insertRun := `insert into executor_runs(task_id,step_id,agent_name,lease_id,lease_version,status,request_count,input_tokens,output_tokens,created_at,updated_at) values(1,2,'worker',?,1,'starting',0,0,0,?,?)`
	if _, err := conn.Exec(insertRun, "lease-1", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(insertRun, "lease-1", now, now); err == nil {
		t.Fatal("duplicate lease run was accepted")
	}
	insertAction := `insert into executor_actions(run_id,sequence,action_type,relative_path,status,result_summary,result_hash,created_at) values(1,1,'read_file','README.md','passed','ok','hash',?)`
	if _, err := conn.Exec(insertAction, now); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(insertAction, now); err == nil {
		t.Fatal("duplicate action sequence was accepted")
	}
	for table, forbidden := range map[string][]string{
		"executor_runs":    {"api_key", "provider_request", "provider_response", "prompt", "secret"},
		"executor_actions": {"file_content", "command_output", "provider_response", "secret"},
	} {
		for _, column := range forbidden {
			var count int
			query := `select count(*) from pragma_table_info('` + table + `') where name=?`
			if err := conn.QueryRow(query, column).Scan(&count); err != nil || count != 0 {
				t.Fatalf("forbidden column %s.%s count=%d err=%v", table, column, count, err)
			}
		}
	}
}

func TestMigrateAddsLeaseColumnsToExistingTaskSteps(t *testing.T) {
	conn, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Exec(`create table task_steps (
		id integer primary key, task_id integer not null, agent_name text not null,
		kind text not null, status text not null, input text not null default '',
		output text not null default '', created_at text not null, completed_at text
	)`); err != nil {
		t.Fatalf("create legacy task_steps: %v", err)
	}

	for i := 0; i < 2; i++ {
		if err := Migrate(context.Background(), conn); err != nil {
			t.Fatalf("Migrate pass %d: %v", i+1, err)
		}
	}

	for _, column := range []string{
		"lease_id", "lease_version", "lease_expires_at", "last_heartbeat_at",
		"checkpoint_id", "attempt", "interrupted_at", "resume_deadline",
	} {
		var count int
		if err := conn.QueryRow(`select count(*) from pragma_table_info('task_steps') where name=?`, column).Scan(&count); err != nil {
			t.Fatalf("inspect task_steps.%s: %v", column, err)
		}
		if count != 1 {
			t.Fatalf("task_steps.%s count=%d", column, count)
		}
	}
}

func TestMigrateCreatesLeaseUniqueConstraints(t *testing.T) {
	conn, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()
	if err := Migrate(context.Background(), conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	now := "2026-07-15T00:00:00Z"
	insertLease := `insert into task_step_leases(task_id,step_id,agent_name,lease_id,lease_version,status,acquired_at,expires_at,created_at,updated_at) values(1,2,'agent-a',?,?,?,?,?,?,?)`
	if _, err := conn.Exec(insertLease, "lease-1", 1, "active", now, now, now, now); err != nil {
		t.Fatalf("insert lease: %v", err)
	}
	if _, err := conn.Exec(insertLease, "lease-1", 2, "active", now, now, now, now); err == nil {
		t.Fatal("duplicate lease_id was accepted")
	}
	if _, err := conn.Exec(insertLease, "lease-2", 1, "active", now, now, now, now); err == nil {
		t.Fatal("duplicate step lease_version was accepted")
	}

	insertCheckpoint := `insert into task_checkpoints(task_id,step_id,lease_id,idempotency_key,git_commit,clean,summary_json,summary_hash,created_at) values(1,2,'lease-1',?,'abc',1,'{}','hash',?)`
	if _, err := conn.Exec(insertCheckpoint, "key-1", now); err != nil {
		t.Fatalf("insert checkpoint: %v", err)
	}
	if _, err := conn.Exec(insertCheckpoint, "key-1", now); err == nil {
		t.Fatal("duplicate lease checkpoint idempotency key was accepted")
	}
	for table, columns := range map[string][]string{
		"task_step_leases":   {"branch_name", "worktree_path", "revoked_reason"},
		"step_reassignments": {"from_branch", "from_worktree", "to_branch", "to_worktree"},
	} {
		for _, column := range columns {
			var count int
			query := `select count(*) from pragma_table_info('` + table + `') where name=?`
			if err := conn.QueryRow(query, column).Scan(&count); err != nil || count != 1 {
				t.Fatalf("%s.%s count=%d err=%v", table, column, count, err)
			}
		}
	}
}

func TestMigrateAuthTablesIsIdempotent(t *testing.T) {
	conn, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()

	for i := 0; i < 2; i++ {
		if err := Migrate(context.Background(), conn); err != nil {
			t.Fatalf("Migrate pass %d: %v", i+1, err)
		}
	}

	for _, column := range []string{"user_id", "token_hash", "expires_at", "created_at"} {
		var count int
		if err := conn.QueryRow(`select count(*) from pragma_table_info('admin_sessions') where name=?`, column).Scan(&count); err != nil {
			t.Fatalf("inspect admin_sessions.%s: %v", column, err)
		}
		if count != 1 {
			t.Fatalf("admin_sessions.%s count=%d", column, count)
		}
	}
}
