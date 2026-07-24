package executor

import "time"

type RunStatus string

const (
	RunStarting     RunStatus = "starting"
	RunRunning      RunStatus = "running"
	RunCheckpointed RunStatus = "checkpointed"
	RunCompleted    RunStatus = "completed"
	RunInterrupted  RunStatus = "interrupted"
	RunFailed       RunStatus = "failed"
	RunStopped      RunStatus = "stopped"
)

// Valid 校验执行状态是否合法。
func (s RunStatus) Valid() bool {
	switch s {
	case RunStarting, RunRunning, RunCheckpointed, RunCompleted, RunInterrupted, RunFailed, RunStopped:
		return true
	default:
		return false
	}
}

type ActionType string

const (
	ActionReadFile   ActionType = "read_file"
	ActionWriteFile  ActionType = "write_file"
	ActionRunCheck   ActionType = "run_check"
	ActionGitStatus  ActionType = "git_status"
	ActionCheckpoint ActionType = "checkpoint"
)

// Valid 校验动作类型是否合法。
func (a ActionType) Valid() bool {
	switch a {
	case ActionReadFile, ActionWriteFile, ActionRunCheck, ActionGitStatus, ActionCheckpoint:
		return true
	default:
		return false
	}
}

type ActionRequest struct {
	Type    ActionType `json:"type"`
	Path    string     `json:"path,omitempty"`
	Content string     `json:"content,omitempty"`
	Command string     `json:"command,omitempty"`
	Args    []string   `json:"args,omitempty"`
}

type WorkerInput struct {
	TaskID       int64  `json:"task_id"`
	StepID       int64  `json:"step_id"`
	AgentName    string `json:"agent_name"`
	LeaseID      string `json:"lease_id"`
	LeaseVersion int64  `json:"lease_version"`
	ClaimToken   string `json:"claim_token,omitempty"`
	ServerURL    string `json:"server_url"`
	AgentToken   string `json:"agent_token"`
}

type WorkerResult struct {
	Status       RunStatus `json:"status"`
	Summary      string    `json:"summary"`
	NextAction   string    `json:"next_action"`
	RequestCount int       `json:"request_count"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	FinishedAt   time.Time `json:"finished_at"`
}
