package mr

import (
	"errors"
	"testing"
)

func TestR007MergeRequiresCompletedAndMergedDependency(t *testing.T) {
	fixture := newReportFixture(t)
	dependency, err := fixture.db.Exec(`insert into task_steps(task_id,agent_name,kind,status,input,created_at)
		values(?,'dependency','backend','completed','{}','now')`, fixture.input.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	dependencyID, _ := dependency.LastInsertId()
	if _, err := fixture.db.Exec(`insert into workflow_edges(task_id,from_step_id,to_step_id,label,created_at,plan_version)
		values(?,?,?,'blocks','now',1)`, fixture.input.TaskID, dependencyID, fixture.input.StepID); err != nil {
		t.Fatal(err)
	}

	created, err := fixture.service.SubmitReport(t.Context(), Principal{Name: "worker", Role: "worker"}, fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	lead := Principal{Name: "lead", Role: "project_lead"}
	if _, err := fixture.service.Review(t.Context(), lead, created.MergeRequest.ID, ReviewInput{
		AgentName: lead.Name,
		Role:      lead.Role,
		Status:    MRApproved,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Merge(t.Context(), lead, created.MergeRequest.ID, MergeInput{
		AgentName: lead.Name,
		Role:      lead.Role,
	}); !errors.Is(err, ErrMergeBlocked) {
		t.Fatalf("dependency without merged MR must block merge, got %v", err)
	}
}
