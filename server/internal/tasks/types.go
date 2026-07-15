package tasks

import "errors"

var (
	ErrNotFound          = errors.New("task not found")
	ErrInvalidTransition = errors.New("invalid task status transition")
	ErrProjectNotFound   = errors.New("project not found")
	ErrProjectConflict   = errors.New("project is not reusable")
)

type CreateTaskInput struct {
	Title       string
	Description string
	ProjectID   *int64
}

type Task struct {
	ID          int64  `json:"id"`
	ProjectID   int64  `json:"project_id"`
	ProjectSlug string `json:"project_slug"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"`
}

type Project struct {
	ID         int64   `json:"id"`
	Slug       string  `json:"slug"`
	Dir        string  `json:"dir"`
	Status     string  `json:"status"`
	MainCommit *string `json:"main_commit,omitempty"`
	RemoteURL  string  `json:"remote_url"`
	CreatedAt  string  `json:"created_at"`
}

type TaskStep struct {
	ID          int64   `json:"id"`
	TaskID      int64   `json:"task_id"`
	AgentName   string  `json:"agent_name"`
	Kind        string  `json:"kind"`
	Status      string  `json:"status"`
	Input       string  `json:"input"`
	Output      string  `json:"output"`
	CreatedAt   string  `json:"created_at"`
	CompletedAt *string `json:"completed_at,omitempty"`
}

type WorkflowEdge struct {
	ID         int64  `json:"id"`
	TaskID     int64  `json:"task_id"`
	FromStepID *int64 `json:"from_step_id,omitempty"`
	ToStepID   *int64 `json:"to_step_id,omitempty"`
	Label      string `json:"label"`
	CreatedAt  string `json:"created_at"`
}

type TaskDetail struct {
	Task    Task           `json:"task"`
	Project Project        `json:"project"`
	Steps   []TaskStep     `json:"steps"`
	Edges   []WorkflowEdge `json:"edges"`
}
