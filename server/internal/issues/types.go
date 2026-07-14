package issues

type Issue struct {
	ID        int64  `json:"id"`
	TaskID    *int64 `json:"task_id,omitempty"`
	MRID      int64  `json:"mr_id,omitempty"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Status    string `json:"status"`
	Blocking  bool   `json:"blocking"`
	CreatedBy string `json:"created_by"`
}

type CreateIssueInput struct {
	TaskID    *int64 `json:"task_id,omitempty"`
	MRID      int64  `json:"mr_id,omitempty"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Blocking  bool   `json:"blocking"`
	CreatedBy string `json:"created_by"`
}
