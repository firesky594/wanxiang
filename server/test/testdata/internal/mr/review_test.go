package mr

import (
	"errors"
	"testing"
)

func TestReviewAllowsProjectLeadAndRejectsOtherAgents(t *testing.T) {
	fixture := newReportFixture(t)
	detail, err := fixture.service.SubmitReport(t.Context(), Principal{Name: "worker", Role: "worker"}, fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	mrID := detail.MergeRequest.ID

	_, err = fixture.service.Review(t.Context(), Principal{Name: "worker", Role: "worker"}, mrID, ReviewInput{AgentName: "worker", Role: "worker", Status: MRApproved})
	if !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("worker err=%v", err)
	}
	_, err = fixture.service.Review(t.Context(), Principal{Name: "other-lead", Role: "project_lead"}, mrID, ReviewInput{AgentName: "other-lead", Role: "project_lead", Status: MRApproved})
	if !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("other lead err=%v", err)
	}

	approved, err := fixture.service.Review(t.Context(), Principal{Name: "lead", Role: "project_lead"}, mrID, ReviewInput{AgentName: "lead", Role: "project_lead", Status: MRApproved, Body: "通过"})
	if err != nil {
		t.Fatal(err)
	}
	if approved.MergeRequest.Status != MRApproved || len(approved.Reviews) != 1 || approved.Reviews[0].Reviewer != "lead" {
		t.Fatalf("approved=%+v", approved)
	}
	if _, err := fixture.service.Review(t.Context(), Principal{Name: "lead", Role: "project_lead"}, mrID, ReviewInput{AgentName: "lead", Role: "project_lead", Status: MRApproved}); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("repeat err=%v", err)
	}
}

func TestReviewRequiresBodyForChangesAndSupportsSoloLead(t *testing.T) {
	fixture := newReportFixture(t)
	if _, err := fixture.db.Exec(`update team_decisions set project_lead='worker',requires_lead=0 where task_id=?`, fixture.input.TaskID); err != nil {
		t.Fatal(err)
	}
	detail, err := fixture.service.SubmitReport(t.Context(), Principal{Name: "worker", Role: "worker"}, fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.service.Review(t.Context(), Principal{Name: "worker", Role: "worker"}, detail.MergeRequest.ID, ReviewInput{AgentName: "worker", Role: "worker", Status: MRChangesRequested})
	if err == nil {
		t.Fatal("empty changes body accepted")
	}
	changed, err := fixture.service.Review(t.Context(), Principal{Name: "worker", Role: "worker"}, detail.MergeRequest.ID, ReviewInput{AgentName: "worker", Role: "worker", Status: MRChangesRequested, Body: "补充测试"})
	if err != nil {
		t.Fatal(err)
	}
	if changed.MergeRequest.Status != MRChangesRequested {
		t.Fatalf("status=%s", changed.MergeRequest.Status)
	}
}

func TestReviewManagerTakeoverRequiresReasonAndRevokedLease(t *testing.T) {
	fixture := newReportFixture(t)
	detail, err := fixture.service.SubmitReport(t.Context(), Principal{Name: "worker", Role: "worker"}, fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	mrID := detail.MergeRequest.ID
	request := ReviewInput{AgentName: "manager", Role: "manager", Status: MRApproved, TakeoverReason: "负责人失联，租约已撤销"}
	if _, err := fixture.service.Review(t.Context(), Principal{Name: "manager", Role: "manager"}, mrID, request); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("active lease takeover err=%v", err)
	}
	if _, err := fixture.db.Exec(`update task_step_leases set status='frozen' where lease_id=?`, fixture.input.LeaseID); err != nil {
		t.Fatal(err)
	}
	request.TakeoverReason = ""
	if _, err := fixture.service.Review(t.Context(), Principal{Name: "manager", Role: "manager"}, mrID, request); err == nil {
		t.Fatal("empty takeover reason accepted")
	}
	request.TakeoverReason = "负责人失联，租约已撤销"
	if _, err := fixture.service.Review(t.Context(), Principal{Name: "manager", Role: "manager"}, mrID, request); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := fixture.db.QueryRow(`select count(*) from audit_logs where actor='manager' and action='mr.takeover.review'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit count=%d err=%v", count, err)
	}
}
