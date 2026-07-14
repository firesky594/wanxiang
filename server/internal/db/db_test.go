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
	} {
		var name string
		err := conn.QueryRow(`select name from sqlite_master where type='table' and name=?`, table).Scan(&name)
		if err != nil {
			t.Fatalf("table %s not created: %v", table, err)
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
