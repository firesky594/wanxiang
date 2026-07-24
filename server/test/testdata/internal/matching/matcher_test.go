package matching

import (
	"strings"
	"testing"

	"wanxiang-agent/server/internal/planning"
)

func TestMatchRejectsAgentsThatFailHardRequirements(t *testing.T) {
	work := planning.WorkItem{Key: "api", Title: "Payment API", Description: "Build Go payment service", RequiredCapabilities: []string{"go"}, RequiredSkills: []string{"tdd"}, RequiredMCPs: []string{"github"}}
	base := AgentDefinition{Name: "ok", Role: "backend", Capabilities: []string{"go"}, Skills: []string{"tdd"}, MCPs: []string{"github"}, ProjectAccess: []string{"billing"}, MaxConcurrency: 2}
	candidates := []Candidate{
		{Definition: base, Status: "online", ActiveTasks: 0},
		{Definition: withName(base, "offline"), Status: "offline"},
		{Definition: AgentDefinition{Name: "missing-skill", Capabilities: []string{"go"}, MCPs: []string{"github"}, ProjectAccess: []string{"billing"}, MaxConcurrency: 1}, Status: "online"},
		{Definition: withName(base, "busy"), Status: "online", ActiveTasks: 2},
		{Definition: func() AgentDefinition {
			d := withName(base, "wrong-project")
			d.ProjectAccess = []string{"other"}
			return d
		}(), Status: "online"},
	}
	got := Match(MatchRequest{Project: "billing", WorkItem: work}, candidates)
	if len(got.Candidates) != 1 || got.Candidates[0].Name != "ok" {
		t.Fatalf("candidates=%+v", got.Candidates)
	}
	if len(got.Rejections) != 4 {
		t.Fatalf("rejections=%+v", got.Rejections)
	}
	joined := ""
	for _, item := range got.Rejections {
		joined += strings.Join(item.Reasons, " ") + " "
	}
	for _, want := range []string{"offline", "skill", "concurrency", "project"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("reasons=%q missing=%q", joined, want)
		}
	}
}

func TestMatchScoresMemoryQualityCapacityAndSortsStably(t *testing.T) {
	work := planning.WorkItem{Key: "api", Title: "Payment API", Description: "Go payment", RequiredCapabilities: []string{"go"}}
	base := AgentDefinition{Role: "backend", Capabilities: []string{"go"}, ProjectAccess: []string{"*"}, MaxConcurrency: 2}
	candidates := []Candidate{
		{Definition: func() AgentDefinition { d := withName(base, "beta"); d.MemorySummary = "payment api go"; return d }(), Status: "online", ActiveTasks: 0, QualityScore: .9},
		{Definition: withName(base, "alpha"), Status: "online", ActiveTasks: 1, QualityScore: .5},
		{Definition: withName(base, "gamma"), Status: "online", ActiveTasks: 1, QualityScore: .5},
	}
	got := Match(MatchRequest{Project: "billing", WorkItem: work}, candidates)
	if names := []string{got.Candidates[0].Name, got.Candidates[1].Name, got.Candidates[2].Name}; strings.Join(names, ",") != "beta,alpha,gamma" {
		t.Fatalf("candidates=%+v", got.Candidates)
	}
	if len(got.Candidates[0].Reasons) == 0 || got.Candidates[0].Score <= got.Candidates[1].Score {
		t.Fatalf("scores=%+v", got.Candidates)
	}
}

func withName(input AgentDefinition, name string) AgentDefinition { input.Name = name; return input }
