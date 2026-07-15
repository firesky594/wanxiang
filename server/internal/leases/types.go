package leases

import "time"

const (
	LeaseTTL          = 60 * time.Second
	HeartbeatInterval = 15 * time.Second
	ResumeWindow      = 5 * time.Minute
)

type LeaseStatus string

const (
	LeaseActive      LeaseStatus = "active"
	LeaseInterrupted LeaseStatus = "interrupted"
	LeaseFrozen      LeaseStatus = "frozen"
	LeaseExpired     LeaseStatus = "expired"
	LeaseRevoked     LeaseStatus = "revoked"
)

func (s LeaseStatus) Valid() bool {
	switch s {
	case LeaseActive, LeaseInterrupted, LeaseFrozen, LeaseExpired, LeaseRevoked:
		return true
	default:
		return false
	}
}

type LeaseRef struct {
	TaskID       int64  `json:"task_id"`
	StepID       int64  `json:"step_id"`
	AgentName    string `json:"agent_name"`
	LeaseID      string `json:"lease_id"`
	LeaseVersion int64  `json:"lease_version"`
}

type Lease struct {
	LeaseRef
	Status          LeaseStatus `json:"status"`
	AcquiredAt      time.Time   `json:"acquired_at"`
	ExpiresAt       time.Time   `json:"expires_at"`
	LastHeartbeatAt *time.Time  `json:"last_heartbeat_at,omitempty"`
	InterruptedAt   *time.Time  `json:"interrupted_at,omitempty"`
	ResumeDeadline  *time.Time  `json:"resume_deadline,omitempty"`
}

type PublicLease struct {
	TaskID       int64       `json:"task_id"`
	StepID       int64       `json:"step_id"`
	LeaseID      string      `json:"lease_id,omitempty"`
	LeaseVersion int64       `json:"lease_version,omitempty"`
	Status       LeaseStatus `json:"status"`
	ExpiresAt    time.Time   `json:"expires_at"`
}

func (l Lease) PublicFor(agentName string) PublicLease {
	view := PublicLease{TaskID: l.TaskID, StepID: l.StepID, Status: l.Status, ExpiresAt: l.ExpiresAt}
	if agentName == l.AgentName {
		view.LeaseID = l.LeaseID
		view.LeaseVersion = l.LeaseVersion
	}
	return view
}

type Checkpoint struct {
	ID             int64     `json:"id"`
	TaskID         int64     `json:"task_id"`
	StepID         int64     `json:"step_id"`
	LeaseID        string    `json:"-"`
	IdempotencyKey string    `json:"-"`
	GitCommit      string    `json:"git_commit"`
	BranchName     string    `json:"branch_name"`
	Clean          bool      `json:"clean"`
	SummaryHash    string    `json:"summary_hash"`
	HighRisk       bool      `json:"high_risk"`
	CreatedAt      time.Time `json:"created_at"`
}
