package agents

type ManagerStatus struct {
	Status     string   `json:"status"`
	MissingEnv []string `json:"missing_env"`
}

type HeartbeatInput struct {
	Name          string `json:"name"`
	Role          string `json:"role"`
	Status        string `json:"status"`
	CurrentTaskID *int64 `json:"current_task_id,omitempty"`
	CurrentModel  string `json:"current_model"`
}

type TokenUsageInput struct {
	TaskID       *int64 `json:"task_id,omitempty"`
	StepID       *int64 `json:"step_id,omitempty"`
	AgentName    string `json:"agent_name"`
	Model        string `json:"model"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
}
