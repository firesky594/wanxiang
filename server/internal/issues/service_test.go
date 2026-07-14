package issues

import (
	"context"
	"strings"
	"testing"

	"wanxiang-agent/server/internal/testutil"
)

func TestBlockingIssuePreventsMRProgress(t *testing.T) {
	conn := testutil.OpenDB(t)
	svc := NewService(conn)
	issue, err := svc.Create(context.Background(), CreateIssueInput{
		MRID:      7,
		Title:     "Do not merge",
		Body:      "Manual review found missing requirement.",
		Blocking:  true,
		CreatedBy: "admin",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !issue.Blocking {
		t.Fatalf("issue should be blocking")
	}
	blocked, err := svc.HasBlockingForMR(context.Background(), 7)
	if err != nil {
		t.Fatalf("HasBlockingForMR: %v", err)
	}
	if !blocked {
		t.Fatalf("expected MR to be blocked")
	}
	var eventType, actor, payload string
	if err := conn.QueryRow(`select event_type,actor,payload_json from runtime_events where event_type='issue.created'`).Scan(&eventType, &actor, &payload); err != nil {
		t.Fatalf("load issue event: %v", err)
	}
	if eventType != "issue.created" || actor != "admin" || strings.Contains(payload, issue.Body) {
		t.Fatalf("event_type=%q actor=%q payload=%s", eventType, actor, payload)
	}
}
