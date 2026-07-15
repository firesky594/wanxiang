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
	} {
		var name string
		err := conn.QueryRow(`select name from sqlite_master where type='table' and name=?`, table).Scan(&name)
		if err != nil {
			t.Fatalf("table %s not created: %v", table, err)
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
