package mr

type MergeRequest struct {
	ID           int64  `json:"id"`
	ProjectID    int64  `json:"project_id"`
	TaskID       int64  `json:"task_id"`
	Title        string `json:"title"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	Status       string `json:"status"`
}
