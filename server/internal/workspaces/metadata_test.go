package workspaces

import (
	"strings"
	"testing"
)

func TestMetadataEncodingIsDeterministicAndAuditable(t *testing.T) {
	project := ProjectMetadata{MetadataVersion: 1, Project: "demo", Manager: "manager", ProjectLead: "lead", Agents: []ProjectAgent{{Name: "api", ReportsTo: "lead"}, {Name: "lead"}}, BranchPolicy: "agent/<agent>/<task>-<work-item>", MergeTarget: "main"}
	got, err := EncodeProject(project)
	if err != nil {
		t.Fatal(err)
	}
	want := "metadata_version: 1\nproject: \"demo\"\nmanager: \"manager\"\nproject_lead: \"lead\"\nagents:\n  - name: \"api\"\n    reports_to: \"lead\"\n  - name: \"lead\"\n    reports_to: \"\"\nbranch_policy: \"agent/<agent>/<task>-<work-item>\"\nmerge_target: \"main\"\n"
	if string(got) != want {
		t.Fatalf("metadata:\n%s\nwant:\n%s", got, want)
	}

	assignment := AssignmentMetadata{MetadataVersion: 1, TaskID: 12, StepID: 34, AssignmentID: 56, WorkItemKey: "api", AgentName: "api", ReportsTo: "lead", BranchName: "agent/api/12-api", WorktreeID: "task-12-step-34", BaseCommit: "abc123", WriteScope: []string{"."}, Status: "ready"}
	first, hash1, err := EncodeAssignment(assignment)
	if err != nil {
		t.Fatal(err)
	}
	second, hash2, err := EncodeAssignment(assignment)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) || hash1 != hash2 || len(hash1) != 64 {
		t.Fatalf("first=%q second=%q hashes=%q/%q", first, second, hash1, hash2)
	}
	if strings.Contains(string(first), "/tmp/") {
		t.Fatalf("absolute worktree leaked: %s", first)
	}
	decoded, err := DecodeAssignment(first)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.BranchName != assignment.BranchName || decoded.WriteScope[0] != "." {
		t.Fatalf("decoded=%+v", decoded)
	}
}

func TestAssignmentMetadataRejectsUnsafeOrUnknownValues(t *testing.T) {
	base := AssignmentMetadata{MetadataVersion: 1, TaskID: 1, StepID: 2, AssignmentID: 3, WorkItemKey: "api", AgentName: "worker", BranchName: "agent/worker/1-api", WorktreeID: "task-1-step-2", BaseCommit: "abc", WriteScope: []string{"."}, Status: "ready"}
	for name, mutate := range map[string]func(*AssignmentMetadata){
		"absolute scope": func(v *AssignmentMetadata) { v.WriteScope = []string{"/tmp"} },
		"parent scope":   func(v *AssignmentMetadata) { v.WriteScope = []string{"../secret"} },
		"invalid agent":  func(v *AssignmentMetadata) { v.AgentName = "../worker" },
		"invalid branch": func(v *AssignmentMetadata) { v.BranchName = "feature/api" },
	} {
		t.Run(name, func(t *testing.T) {
			value := base
			mutate(&value)
			if _, _, err := EncodeAssignment(value); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	encoded, _, err := EncodeAssignment(base)
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, []byte("unknown_field: \"value\"\n")...)
	if _, err := DecodeAssignment(encoded); err == nil {
		t.Fatal("expected unknown field error")
	}
}
