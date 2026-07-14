package agents

type ManagerStatus struct {
	Status     string   `json:"status"`
	MissingEnv []string `json:"missing_env"`
}

type AgentConfigInput struct {
	Name         string `json:"name"`
	ProviderType string `json:"provider_type"`
	BaseURL      string `json:"base_url"`
	Model        string `json:"model"`
	APIKey       string `json:"api_key"`
}

type AgentConfigView struct {
	Name             string `json:"name"`
	ProviderType     string `json:"provider_type"`
	BaseURL          string `json:"base_url"`
	Model            string `json:"model"`
	SecretConfigured bool   `json:"secret_configured"`
	Status           string `json:"status"`
	LastError        string `json:"last_error,omitempty"`
}

type runtimeConfig struct {
	AgentConfigView
	APIKey string
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
