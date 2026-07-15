package leases

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	maxSummaryItem = 2000
	maxSummaryJSON = 32 * 1024
)

var sensitiveSummary = regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|refresh[_-]?token|token|password|passwd|secret|private[_-]?key)\s*[:=]`)

type RecoverySummary struct {
	Completed    []string `json:"completed"`
	NextAction   string   `json:"next_action"`
	FilesChanged []string `json:"files_changed,omitempty"`
	Decisions    []string `json:"decisions,omitempty"`
	Blockers     []string `json:"blockers,omitempty"`
	Risks        []string `json:"risks,omitempty"`
}

func normalizeSummary(summary RecoverySummary) (RecoverySummary, []byte, error) {
	summary.Completed = normalizeItems(summary.Completed)
	summary.FilesChanged = normalizeItems(summary.FilesChanged)
	summary.Decisions = normalizeItems(summary.Decisions)
	summary.Blockers = normalizeItems(summary.Blockers)
	summary.Risks = normalizeItems(summary.Risks)
	summary.NextAction = strings.TrimSpace(summary.NextAction)
	if summary.NextAction == "" {
		return RecoverySummary{}, nil, errors.New("next_action is required")
	}
	all := append([]string{summary.NextAction}, summary.Completed...)
	all = append(all, summary.FilesChanged...)
	all = append(all, summary.Decisions...)
	all = append(all, summary.Blockers...)
	all = append(all, summary.Risks...)
	for _, item := range all {
		if !utf8.ValidString(item) || len(item) > maxSummaryItem || hasControlCharacter(item) || sensitiveSummary.MatchString(item) {
			return RecoverySummary{}, nil, errors.New("summary contains unsafe content")
		}
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		return RecoverySummary{}, nil, err
	}
	if len(encoded) > maxSummaryJSON {
		return RecoverySummary{}, nil, errors.New("summary is too large")
	}
	return summary, encoded, nil
}

func normalizeItems(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func hasControlCharacter(value string) bool {
	for _, char := range value {
		if char < 32 || char == 127 {
			return true
		}
	}
	return false
}
