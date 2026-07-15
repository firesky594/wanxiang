package mr

import (
	"strings"
	"testing"
)

func TestCompletionReportValidation(t *testing.T) {
	valid := CompletionReportInput{
		AgentName: "backend", Role: "worker", ProjectID: 1, TaskID: 2, StepID: 3,
		LeaseID: "lease_1", LeaseVersion: 1, SourceBranch: "agent/backend/task",
		CheckpointCommit: strings.Repeat("a", 40), HeadCommit: strings.Repeat("a", 40),
		Completed: []string{"完成接口"}, Tests: []TestEvidence{{Command: "go test ./...", Status: "passed"}},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid report rejected: %v", err)
	}

	tooMany := valid
	tooMany.Risks = make([]string, 101)
	if err := tooMany.Validate(); err == nil {
		t.Fatal("101 risks accepted")
	}

	tooLong := valid
	tooLong.Completed = []string{strings.Repeat("x", 2049)}
	if err := tooLong.Validate(); err == nil {
		t.Fatal("oversized list item accepted")
	}

	badStatus := ReviewInput{AgentName: "lead", Role: "project_lead", Status: "merged"}
	if err := badStatus.Validate(); err == nil {
		t.Fatal("invalid review status accepted")
	}
	changes := ReviewInput{AgentName: "lead", Role: "project_lead", Status: MRChangesRequested}
	if err := changes.Validate(); err == nil {
		t.Fatal("changes_requested without body accepted")
	}
}
