package mr

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"wanxiang-agent/server/internal/testutil"
)

func TestR007ReviewPayloadRedactsCredentials(t *testing.T) {
	input := strings.Join([]string{
		`api_key = "secret-value-that-must-not-leak"`,
		`Authorization: Bearer abcdefghijklmnopqrstuvwxyz.123456`,
		`cloud=AKIAABCDEFGHIJKLMNOP`,
		"-----BEGIN PRIVATE KEY-----\nprivate-material\n-----END PRIVATE KEY-----",
		`eyJabcdefghijk.abcdefghijkl.abcdefghijkl`,
		strings.Repeat("a", 80),
	}, "\n")
	got := redactReviewText(input)
	for _, secret := range []string{
		"secret-value-that-must-not-leak",
		"abcdefghijklmnopqrstuvwxyz.123456",
		"AKIAABCDEFGHIJKLMNOP",
		"private-material",
		"eyJabcdefghijk.abcdefghijkl.abcdefghijkl",
		strings.Repeat("a", 80),
	} {
		if strings.Contains(got, secret) {
			t.Fatalf("review payload leaked %q in %q", secret, got)
		}
	}
	if !strings.Contains(got, "[REDACTED") {
		t.Fatalf("review payload was not marked as redacted: %q", got)
	}
}

func TestR007ReviewRejectsSensitivePaths(t *testing.T) {
	for _, path := range []string{
		".env",
		".env.production",
		"agents/backend/env",
		"config/credentials.json",
		"certs/client.key",
		"secrets/provider.txt",
	} {
		if !sensitiveReviewPath(path) {
			t.Fatalf("sensitive path %q was accepted", path)
		}
	}
	for _, path := range []string{"src/main.go", "web/src/config.ts", "docs/security.md"} {
		if sensitiveReviewPath(path) {
			t.Fatalf("ordinary path %q was rejected", path)
		}
	}
}

func TestR007ApprovedMergeBlockIsNotRequeued(t *testing.T) {
	conn := testutil.OpenDB(t)
	insertReviewJobFixture(t, conn, "approved", reviewJobBlocked, "merge_source_invalid")

	worker := &ReviewWorker{db: conn}
	if err := worker.enqueueAndReconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	var status, code string
	if err := conn.QueryRow(`select status,blocked_code from mr_review_jobs where mr_id=1`).Scan(&status, &code); err != nil {
		t.Fatal(err)
	}
	if status != reviewJobBlocked || code != "merge_source_invalid" {
		t.Fatalf("merge block was unexpectedly requeued: status=%q code=%q", status, code)
	}
}

func TestR007HumanReviewBlockCanAdvanceAfterApproval(t *testing.T) {
	conn := testutil.OpenDB(t)
	insertReviewJobFixture(t, conn, "approved", reviewJobBlocked, "human_decision_required")

	worker := &ReviewWorker{db: conn}
	if err := worker.enqueueAndReconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	var status, code string
	if err := conn.QueryRow(`select status,blocked_code from mr_review_jobs where mr_id=1`).Scan(&status, &code); err != nil {
		t.Fatal(err)
	}
	if status != reviewJobMergePending || code != "" {
		t.Fatalf("approved human review was not advanced: status=%q code=%q", status, code)
	}
}

func insertReviewJobFixture(t *testing.T, conn *sql.DB, mrStatus, jobStatus, blockedCode string) {
	t.Helper()
	if _, err := conn.Exec(`insert into merge_requests(
			id,project_id,task_id,title,source_branch,target_branch,status,created_by,created_at,report_id
		) values(1,1,1,'review','agent/test/work','main',?,'agent-test','now',1)`, mrStatus); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`insert into mr_review_jobs(
			id,mr_id,status,blocked_code,created_at,updated_at
		) values(1,1,?,?, 'now','now')`, jobStatus, blockedCode); err != nil {
		t.Fatal(err)
	}
}
