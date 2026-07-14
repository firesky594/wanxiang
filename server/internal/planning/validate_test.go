package planning

import (
	"strings"
	"testing"
)

func TestParsePlanAcceptsValidDependencyGraph(t *testing.T) {
	plan, err := ParsePlan([]byte(`{"summary":"ship auth","requires_project_lead":true,"work_items":[{"key":"api","title":"API","description":"build API","kind":"backend","required_capabilities":["go"],"acceptance_criteria":["tests pass"],"depends_on":[]},{"key":"ui","title":"UI","description":"build UI","kind":"frontend","required_capabilities":["vue"],"acceptance_criteria":["build passes"],"depends_on":["api"]}]}`))
	if err != nil || len(plan.WorkItems) != 2 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
}

func TestParsePlanRejectsInvalidWorkGraphs(t *testing.T) {
	tests := []struct{ name, body, want string }{
		{"duplicate", `{"summary":"x","work_items":[{"key":"a","title":"A","description":"d","kind":"backend","acceptance_criteria":["ok"]},{"key":"a","title":"B","description":"d","kind":"backend","acceptance_criteria":["ok"]}]}`, "duplicate work item key"},
		{"criteria", `{"summary":"x","work_items":[{"key":"a","title":"A","description":"d","kind":"backend","acceptance_criteria":[]}]}`, "acceptance criteria"},
		{"unknown dependency", `{"summary":"x","work_items":[{"key":"a","title":"A","description":"d","kind":"backend","acceptance_criteria":["ok"],"depends_on":["missing"]}]}`, "unknown dependency"},
		{"cycle", `{"summary":"x","work_items":[{"key":"a","title":"A","description":"d","kind":"backend","acceptance_criteria":["ok"],"depends_on":["b"]},{"key":"b","title":"B","description":"d","kind":"backend","acceptance_criteria":["ok"],"depends_on":["a"]}]}`, "dependency cycle"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParsePlan([]byte(tt.body))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
