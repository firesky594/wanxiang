package testutil

import (
	"context"
	"database/sql"
	"testing"

	"wanxiang-agent/server/internal/db"
)

// OpenDB 打开并初始化临时测试数据库。
func OpenDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	if err := db.Migrate(context.Background(), conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}
