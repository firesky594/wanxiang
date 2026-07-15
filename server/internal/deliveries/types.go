package deliveries

import (
	"encoding/json"
	"errors"
)

var (
	ErrNotFound                = errors.New("delivery_not_found")
	ErrNotReady                = errors.New("delivery_not_ready")
	ErrStaleSnapshot           = errors.New("stale_snapshot")
	ErrAcceptanceClosed        = errors.New("acceptance_closed")
	ErrDecisionCommentRequired = errors.New("decision_comment_required")
)

type Evidence struct {
	MergeRequests []MergeEvidence    `json:"merge_requests"`
	Reports       []ReportEvidence   `json:"reports"`
	Tests         []TestEvidence     `json:"tests"`
	Risks         []string           `json:"risks"`
	Incomplete    []string           `json:"incomplete"`
	WorkItems     []WorkItemEvidence `json:"work_items"`
	Reviews       []ReviewEvidence   `json:"reviews"`
	UserDecisions []string           `json:"user_decisions"`
	HighRisk      []string           `json:"high_risk"`
}

type MergeEvidence struct {
	ID           int64  `json:"id"`
	StepID       int64  `json:"step_id"`
	Status       string `json:"status"`
	SourceCommit string `json:"source_commit"`
	MergeCommit  string `json:"merge_commit"`
	AgentName    string `json:"agent_name"`
}
type ReportEvidence struct {
	ID        int64    `json:"id"`
	StepID    int64    `json:"step_id"`
	AgentName string   `json:"agent_name"`
	Completed []string `json:"completed"`
	KeyFiles  []string `json:"key_files"`
}
type TestEvidence struct {
	Command string `json:"command"`
	Status  string `json:"status"`
	Summary string `json:"summary,omitempty"`
}
type WorkItemEvidence struct {
	StepID    int64           `json:"step_id"`
	AgentName string          `json:"agent_name"`
	Kind      string          `json:"kind"`
	Status    string          `json:"status"`
	Input     json.RawMessage `json:"input"`
}
type ReviewEvidence struct {
	MRID      int64  `json:"mr_id"`
	Reviewer  string `json:"reviewer"`
	Role      string `json:"role"`
	Status    string `json:"status"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

type Snapshot struct {
	ID                    int64    `json:"id"`
	TaskID                int64    `json:"task_id"`
	ProjectID             int64    `json:"project_id"`
	Version               int64    `json:"version"`
	ManagerNotificationID int64    `json:"manager_notification_id"`
	MainCommit            string   `json:"main_commit"`
	Status                string   `json:"status"`
	Summary               string   `json:"summary"`
	SummaryHash           string   `json:"summary_hash"`
	Evidence              Evidence `json:"evidence"`
	CreatedBy             string   `json:"created_by"`
	CreatedAt             string   `json:"created_at"`
}

type DecisionInput struct {
	Decision       string `json:"decision"`
	Comment        string `json:"comment"`
	IdempotencyKey string `json:"idempotency_key"`
	CreatedBy      string `json:"-"`
}
type AcceptanceDecision struct {
	ID         int64  `json:"id"`
	SnapshotID int64  `json:"snapshot_id"`
	TaskID     int64  `json:"task_id"`
	Decision   string `json:"decision"`
	Comment    string `json:"comment"`
	CreatedBy  string `json:"created_by"`
	CreatedAt  string `json:"created_at"`
}
type ReworkRound struct {
	ID               int64  `json:"id"`
	TaskID           int64  `json:"task_id"`
	SourceSnapshotID int64  `json:"source_snapshot_id"`
	DecisionID       int64  `json:"decision_id"`
	Round            int64  `json:"round"`
	PlanVersion      int64  `json:"plan_version"`
	Reason           string `json:"reason"`
	Status           string `json:"status"`
	LastError        string `json:"last_error"`
	CreatedBy        string `json:"created_by"`
	CreatedAt        string `json:"created_at"`
}
type Detail struct {
	Snapshot     Snapshot             `json:"snapshot"`
	Decisions    []AcceptanceDecision `json:"decisions"`
	ReworkRounds []ReworkRound        `json:"rework_rounds"`
}
type DecisionResult struct {
	Decision    AcceptanceDecision `json:"decision"`
	ReworkRound *ReworkRound       `json:"rework_round,omitempty"`
	TaskStatus  string             `json:"task_status"`
}

func decodeStrings(raw string) []string {
	var out []string
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}
func decodeTests(raw string) []TestEvidence {
	var out []TestEvidence
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}
