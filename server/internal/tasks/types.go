package tasks

type Task struct {
	ID          int64  `json:"id"`
	ProjectID   int64  `json:"project_id"`
	ProjectSlug string `json:"project_slug"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"`
}
