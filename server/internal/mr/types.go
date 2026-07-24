package mr

import (
	"encoding/json"
	"errors"
	"strings"
)

var (
	ErrIdentityMismatch   = errors.New("identity_mismatch")
	ErrLeaseInvalid       = errors.New("lease_invalid")
	ErrCheckpointMismatch = errors.New("checkpoint_mismatch")
	ErrBranchOwnership    = errors.New("branch_ownership")
	ErrStateConflict      = errors.New("state_conflict")
	ErrMergeBlocked       = errors.New("merge_blocked")
)

type Principal struct {
	Name string `json:"agent_name"`
	Role string `json:"role"`
}

type MergeInput struct {
	AgentName      string `json:"agent_name"`
	Role           string `json:"role"`
	TakeoverReason string `json:"takeover_reason"`
}

type MergeResult struct {
	MRID        int64  `json:"mr_id"`
	Status      string `json:"status"`
	MergeCommit string `json:"merge_commit"`
}

const (
	MRPendingReview    = "pending_review"
	MRChangesRequested = "changes_requested"
	MRApproved         = "approved"
	MRMerged           = "merged"
	MRClosed           = "closed"
)

type TestEvidence struct {
	Command string `json:"command"`
	Status  string `json:"status"`
	Summary string `json:"summary,omitempty"`
}

type CompletionReportInput struct {
	AgentName        string         `json:"agent_name"`
	Role             string         `json:"role"`
	ProjectID        int64          `json:"project_id"`
	TaskID           int64          `json:"task_id"`
	StepID           int64          `json:"step_id"`
	LeaseID          string         `json:"lease_id"`
	LeaseVersion     int64          `json:"lease_version"`
	SourceBranch     string         `json:"source_branch"`
	CheckpointCommit string         `json:"checkpoint_commit"`
	HeadCommit       string         `json:"head_commit"`
	Completed        []string       `json:"completed"`
	Incomplete       []string       `json:"incomplete"`
	KeyFiles         []string       `json:"key_files"`
	Tests            []TestEvidence `json:"tests"`
	Risks            []string       `json:"risks"`
	Dependencies     []int64        `json:"dependencies"`
	MergeOrder       []int64        `json:"merge_order"`
	UserDecision     string         `json:"user_decision"`
}

// Validate 校验完成报告身份、内容及大小限制。
func (in CompletionReportInput) Validate() error {
	if strings.TrimSpace(in.AgentName) == "" || strings.TrimSpace(in.Role) == "" || in.ProjectID <= 0 || in.TaskID <= 0 || in.StepID <= 0 || strings.TrimSpace(in.LeaseID) == "" || in.LeaseVersion <= 0 || strings.TrimSpace(in.SourceBranch) == "" || strings.TrimSpace(in.CheckpointCommit) == "" || strings.TrimSpace(in.HeadCommit) == "" {
		return errors.New("invalid completion report identity")
	}
	for _, values := range [][]string{in.Completed, in.Incomplete, in.KeyFiles, in.Risks} {
		if err := validateStrings(values); err != nil {
			return err
		}
	}
	if len(in.Tests) > 100 || len(in.Dependencies) > 100 || len(in.MergeOrder) > 100 || len(in.UserDecision) > 2048 {
		return errors.New("completion report exceeds limits")
	}
	for _, test := range in.Tests {
		if len(test.Command) > 2048 || len(test.Status) > 2048 || len(test.Summary) > 2048 {
			return errors.New("test evidence exceeds limits")
		}
	}
	encoded, err := json.Marshal(in)
	if err != nil || len(encoded) > 256*1024 {
		return errors.New("completion report exceeds total limit")
	}
	return nil
}

func validateStrings(values []string) error {
	if len(values) > 100 {
		return errors.New("completion report list exceeds limit")
	}
	for _, value := range values {
		if len(value) > 2048 {
			return errors.New("completion report item exceeds limit")
		}
	}
	return nil
}

type ReviewInput struct {
	AgentName      string `json:"agent_name"`
	Role           string `json:"role"`
	Status         string `json:"status"`
	Body           string `json:"body"`
	TakeoverReason string `json:"takeover_reason"`
}

// Validate 校验评审身份、状态与内容限制。
func (in ReviewInput) Validate() error {
	if strings.TrimSpace(in.AgentName) == "" || strings.TrimSpace(in.Role) == "" {
		return errors.New("review identity is required")
	}
	if in.Status != MRApproved && in.Status != MRChangesRequested {
		return errors.New("invalid review status")
	}
	if in.Status == MRChangesRequested && strings.TrimSpace(in.Body) == "" {
		return errors.New("review body is required")
	}
	if len(in.Body) > 8*1024 || len(in.TakeoverReason) > 2*1024 {
		return errors.New("review exceeds limits")
	}
	return nil
}

type CompletionReport struct {
	ID int64 `json:"id"`
	CompletionReportInput
	AgentRole string `json:"agent_role"`
	Version   int64  `json:"version"`
	CreatedAt string `json:"created_at"`
}

type ManagerNotification struct {
	ID          int64  `json:"id"`
	ProjectID   int64  `json:"project_id"`
	TaskID      int64  `json:"task_id"`
	MRID        int64  `json:"mr_id"`
	ReportID    int64  `json:"report_id"`
	ProjectLead string `json:"project_lead"`
	MainCommit  string `json:"main_commit"`
	Status      string `json:"status"`
}

type MRReview struct {
	ID        int64  `json:"id"`
	MRID      int64  `json:"mr_id"`
	Reviewer  string `json:"reviewer"`
	Role      string `json:"role"`
	Status    string `json:"status"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

type MRDetail struct {
	MergeRequest MergeRequest     `json:"merge_request"`
	Report       CompletionReport `json:"report"`
	Reviews      []MRReview       `json:"reviews"`
}

type MergeRequest struct {
	ID            int64  `json:"id"`
	ProjectID     int64  `json:"project_id"`
	TaskID        int64  `json:"task_id"`
	Title         string `json:"title"`
	SourceBranch  string `json:"source_branch"`
	TargetBranch  string `json:"target_branch"`
	Status        string `json:"status"`
	ReportID      int64  `json:"report_id"`
	StepID        int64  `json:"step_id"`
	ReportVersion int64  `json:"report_version"`
	SourceCommit  string `json:"source_commit"`
	ProjectLead   string `json:"project_lead"`
}
