package planning

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

var workKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func ParsePlan(data []byte) (Plan, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var plan Plan
	if err := decoder.Decode(&plan); err != nil {
		return Plan{}, fmt.Errorf("invalid plan json: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Plan{}, errors.New("invalid plan json: trailing content")
	}
	plan.Summary = strings.TrimSpace(plan.Summary)
	if plan.Summary == "" {
		return Plan{}, errors.New("plan summary is required")
	}
	if len(plan.WorkItems) == 0 {
		return Plan{}, errors.New("at least one work item is required")
	}
	items := make(map[string]WorkItem, len(plan.WorkItems))
	for index := range plan.WorkItems {
		item := &plan.WorkItems[index]
		item.Key, item.Title, item.Description, item.Kind = strings.TrimSpace(item.Key), strings.TrimSpace(item.Title), strings.TrimSpace(item.Description), strings.TrimSpace(item.Kind)
		if !workKeyPattern.MatchString(item.Key) {
			return Plan{}, fmt.Errorf("invalid work item key %q", item.Key)
		}
		if _, exists := items[item.Key]; exists {
			return Plan{}, fmt.Errorf("duplicate work item key %q", item.Key)
		}
		if item.Title == "" || item.Description == "" || item.Kind == "" {
			return Plan{}, fmt.Errorf("work item %q requires title, description, and kind", item.Key)
		}
		if len(item.AcceptanceCriteria) == 0 {
			return Plan{}, fmt.Errorf("work item %q requires acceptance criteria", item.Key)
		}
		items[item.Key] = *item
	}
	for _, item := range plan.WorkItems {
		for _, dependency := range item.DependsOn {
			if _, ok := items[dependency]; !ok {
				return Plan{}, fmt.Errorf("work item %q has unknown dependency %q", item.Key, dependency)
			}
		}
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(key string) error {
		if visiting[key] {
			return errors.New("dependency cycle detected")
		}
		if visited[key] {
			return nil
		}
		visiting[key] = true
		for _, dep := range items[key].DependsOn {
			if err := visit(dep); err != nil {
				return err
			}
		}
		visiting[key] = false
		visited[key] = true
		return nil
	}
	for key := range items {
		if err := visit(key); err != nil {
			return Plan{}, err
		}
	}
	return plan, nil
}
