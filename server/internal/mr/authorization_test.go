package mr

import (
	"errors"
	"testing"
)

func TestAuthorizeSubmissionRejectsIdentityLeaseBranchAndCheckpointMismatch(t *testing.T) {
	fixture := newReportFixture(t)
	tests := []struct {
		name      string
		principal Principal
		mutate    func(*CompletionReportInput)
		want      error
	}{
		{"token name", Principal{Name: "other", Role: "worker"}, func(*CompletionReportInput) {}, ErrIdentityMismatch},
		{"request role", Principal{Name: "worker", Role: "worker"}, func(in *CompletionReportInput) { in.Role = "manager" }, ErrIdentityMismatch},
		{"lease id", Principal{Name: "worker", Role: "worker"}, func(in *CompletionReportInput) { in.LeaseID = "wrong" }, ErrLeaseInvalid},
		{"lease version", Principal{Name: "worker", Role: "worker"}, func(in *CompletionReportInput) { in.LeaseVersion++ }, ErrLeaseInvalid},
		{"source branch", Principal{Name: "worker", Role: "worker"}, func(in *CompletionReportInput) { in.SourceBranch = "agent/other/task" }, ErrBranchOwnership},
		{"checkpoint", Principal{Name: "worker", Role: "worker"}, func(in *CompletionReportInput) { in.CheckpointCommit = "deadbeef" }, ErrCheckpointMismatch},
		{"head", Principal{Name: "worker", Role: "worker"}, func(in *CompletionReportInput) { in.HeadCommit = "deadbeef" }, ErrCheckpointMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := fixture.input
			tt.mutate(&input)
			_, err := fixture.service.authorizeSubmission(t.Context(), tt.principal, input)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err=%v want=%v", err, tt.want)
			}
		})
	}
}
