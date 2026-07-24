package mr

import "testing"

func TestDetailAndAdminListReturnReportAndReviewHistory(t *testing.T) {
	fixture := newReportFixture(t)
	created, err := fixture.service.SubmitReport(t.Context(), Principal{Name: "worker", Role: "worker"}, fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Review(t.Context(), Principal{Name: "lead", Role: "project_lead"}, created.MergeRequest.ID, ReviewInput{AgentName: "lead", Role: "project_lead", Status: MRApproved}); err != nil {
		t.Fatal(err)
	}
	detail, err := fixture.service.Detail(t.Context(), Principal{Name: "lead", Role: "project_lead"}, created.MergeRequest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Report.ID == 0 || detail.Report.Completed[0] != "完成" || len(detail.Reviews) != 1 {
		t.Fatalf("detail=%+v", detail)
	}
	list, err := fixture.service.AdminList(t.Context(), &fixture.input.TaskID, 20, 0)
	if err != nil || len(list) != 1 || list[0].Report.ID != detail.Report.ID {
		t.Fatalf("list=%+v err=%v", list, err)
	}
}
