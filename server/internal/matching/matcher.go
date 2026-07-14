package matching

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"wanxiang-agent/server/internal/planning"
)

type Candidate struct {
	Definition   AgentDefinition
	Status       string
	ActiveTasks  int
	QualityScore float64
}
type MatchRequest struct {
	Project  string
	WorkItem planning.WorkItem
}
type CandidateScore struct {
	Name    string
	Score   float64
	Reasons []string
}
type Rejection struct {
	Name    string
	Reasons []string
}
type MatchResult struct {
	Candidates []CandidateScore
	Rejections []Rejection
}

func Match(request MatchRequest, candidates []Candidate) MatchResult {
	result := MatchResult{Candidates: []CandidateScore{}, Rejections: []Rejection{}}
	for _, candidate := range candidates {
		rejected := hardRejections(request, candidate)
		if len(rejected) > 0 {
			result.Rejections = append(result.Rejections, Rejection{Name: candidate.Definition.Name, Reasons: rejected})
			continue
		}
		score, reasons := scoreCandidate(request, candidate)
		result.Candidates = append(result.Candidates, CandidateScore{Name: candidate.Definition.Name, Score: score, Reasons: reasons})
	}
	sort.Slice(result.Candidates, func(i, j int) bool {
		if result.Candidates[i].Score == result.Candidates[j].Score {
			return result.Candidates[i].Name < result.Candidates[j].Name
		}
		return result.Candidates[i].Score > result.Candidates[j].Score
	})
	sort.Slice(result.Rejections, func(i, j int) bool { return result.Rejections[i].Name < result.Rejections[j].Name })
	return result
}

func hardRejections(request MatchRequest, c Candidate) []string {
	reasons := []string{}
	if c.Status != "online" {
		reasons = append(reasons, "status_offline")
	}
	if c.Definition.MaxConcurrency < 1 || c.ActiveTasks >= c.Definition.MaxConcurrency {
		reasons = append(reasons, "concurrency_limit")
	}
	for _, item := range missing(request.WorkItem.RequiredCapabilities, c.Definition.Capabilities) {
		reasons = append(reasons, "missing_capability:"+item)
	}
	for _, item := range missing(request.WorkItem.RequiredSkills, c.Definition.Skills) {
		reasons = append(reasons, "missing_skill:"+item)
	}
	for _, item := range missing(request.WorkItem.RequiredMCPs, c.Definition.MCPs) {
		reasons = append(reasons, "missing_mcp:"+item)
	}
	if !contains(c.Definition.ProjectAccess, "*") && !contains(c.Definition.ProjectAccess, request.Project) {
		reasons = append(reasons, "project_access_denied")
	}
	return reasons
}

func scoreCandidate(request MatchRequest, c Candidate) (float64, []string) {
	score := float64(len(request.WorkItem.RequiredCapabilities)*10 + len(request.WorkItem.RequiredSkills)*8 + len(request.WorkItem.RequiredMCPs)*8)
	reasons := []string{fmt.Sprintf("hard_requirements=%.0f", score)}
	capacity := float64(c.Definition.MaxConcurrency-c.ActiveTasks) / float64(c.Definition.MaxConcurrency) * 10
	score += capacity
	reasons = append(reasons, fmt.Sprintf("capacity=%.2f", capacity))
	quality := c.QualityScore
	if quality < 0 {
		quality = 0
	}
	if quality > 1 {
		quality = 1
	}
	score += quality * 10
	reasons = append(reasons, fmt.Sprintf("quality=%.2f", quality*10))
	extra := len(c.Definition.Capabilities) - len(request.WorkItem.RequiredCapabilities)
	if extra > 0 {
		bonus := float64(extra * 2)
		score += bonus
		reasons = append(reasons, fmt.Sprintf("capability_bonus=%.2f", bonus))
	}
	memory := memoryScore(request, c.Definition.MemorySummary)
	score += memory
	reasons = append(reasons, fmt.Sprintf("memory=%.2f", memory))
	return score, reasons
}

var wordPattern = regexp.MustCompile(`[a-z0-9_-]+`)

func memoryScore(request MatchRequest, memory string) float64 {
	memory = strings.ToLower(memory)
	words := wordPattern.FindAllString(strings.ToLower(request.WorkItem.Title+" "+request.WorkItem.Description+" "+strings.Join(request.WorkItem.RequiredCapabilities, " ")), -1)
	seen := map[string]bool{}
	score := 0.0
	for _, word := range words {
		if len(word) < 2 || seen[word] {
			continue
		}
		seen[word] = true
		if strings.Contains(memory, word) {
			score++
		}
	}
	if score > 10 {
		return 10
	}
	return score
}
func missing(required, available []string) []string {
	result := []string{}
	for _, item := range required {
		if !contains(available, item) {
			result = append(result, item)
		}
	}
	return result
}
func contains(items []string, want string) bool {
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}
