package executor

import (
	"context"
	"database/sql"
	"errors"
)

var ErrExecutorUnavailable = errors.New("executor supervisor unavailable")
var ErrRunNotActive = errors.New("executor run is not active")

type RunView struct {
	ID           int64  `json:"id"`
	TaskID       int64  `json:"task_id"`
	StepID       int64  `json:"step_id"`
	AgentName    string `json:"agent_name"`
	PID          *int64 `json:"pid,omitempty"`
	Status       string `json:"status"`
	RequestCount int    `json:"request_count"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	ExitCode     *int   `json:"exit_code,omitempty"`
	ErrorSummary string `json:"error_summary,omitempty"`
	CreatedAt    string `json:"created_at"`
	StartedAt    string `json:"started_at,omitempty"`
	ExitedAt     string `json:"exited_at,omitempty"`
	UpdatedAt    string `json:"updated_at"`
}
type ActionView struct {
	Sequence      int    `json:"sequence"`
	ActionType    string `json:"action_type"`
	RelativePath  string `json:"relative_path,omitempty"`
	Status        string `json:"status"`
	ResultSummary string `json:"result_summary,omitempty"`
	ResultHash    string `json:"result_hash"`
	CreatedAt     string `json:"created_at"`
}
type RunDetail struct {
	Run     RunView      `json:"run"`
	Actions []ActionView `json:"actions"`
}
type AdminService struct {
	db         *sql.DB
	supervisor *Supervisor
}

// NewAdminService 创建执行器后台管理服务。
func NewAdminService(db *sql.DB, supervisor *Supervisor) *AdminService {
	return &AdminService{db: db, supervisor: supervisor}
}

// ListRuns 查询任务的执行记录列表。
func (s *AdminService) ListRuns(ctx context.Context, taskID int64) ([]RunView, error) {
	query := `select id,task_id,step_id,agent_name,pid,status,request_count,input_tokens,output_tokens,exit_code,error_summary,created_at,coalesce(started_at,''),coalesce(exited_at,''),updated_at from executor_runs`
	args := []any{}
	if taskID > 0 {
		query += ` where task_id=?`
		args = append(args, taskID)
	}
	query += ` order by id desc limit 100`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []RunView{}
	for rows.Next() {
		item, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// GetRun 查询执行记录及动作明细。
func (s *AdminService) GetRun(ctx context.Context, id int64) (RunDetail, error) {
	item, err := scanRun(s.db.QueryRowContext(ctx, `select id,task_id,step_id,agent_name,pid,status,request_count,input_tokens,output_tokens,exit_code,error_summary,created_at,coalesce(started_at,''),coalesce(exited_at,''),updated_at from executor_runs where id=?`, id))
	if err != nil {
		return RunDetail{}, err
	}
	rows, err := s.db.QueryContext(ctx, `select sequence,action_type,relative_path,status,result_summary,result_hash,created_at from executor_actions where run_id=? order by sequence`, id)
	if err != nil {
		return RunDetail{}, err
	}
	defer rows.Close()
	actions := []ActionView{}
	for rows.Next() {
		var action ActionView
		if err := rows.Scan(&action.Sequence, &action.ActionType, &action.RelativePath, &action.Status, &action.ResultSummary, &action.ResultHash, &action.CreatedAt); err != nil {
			return RunDetail{}, err
		}
		action.ResultSummary = publicError(action.ResultSummary)
		actions = append(actions, action)
	}
	return RunDetail{Run: item, Actions: actions}, rows.Err()
}

// Scan 触发一次执行器任务扫描。
func (s *AdminService) Scan(ctx context.Context) (int, error) {
	if s.supervisor == nil {
		return 0, ErrExecutorUnavailable
	}
	return s.supervisor.Scan(ctx)
}

// StopRun 停止指定执行记录对应进程。
func (s *AdminService) StopRun(ctx context.Context, id int64) error {
	if s.supervisor == nil {
		return ErrExecutorUnavailable
	}
	return s.supervisor.StopRun(ctx, id)
}

type rowScanner interface{ Scan(...any) error }

func scanRun(row rowScanner) (RunView, error) {
	var item RunView
	var pid sql.NullInt64
	var exit sql.NullInt64
	var summary string
	err := row.Scan(&item.ID, &item.TaskID, &item.StepID, &item.AgentName, &pid, &item.Status, &item.RequestCount, &item.InputTokens, &item.OutputTokens, &exit, &summary, &item.CreatedAt, &item.StartedAt, &item.ExitedAt, &item.UpdatedAt)
	if pid.Valid {
		value := pid.Int64
		item.PID = &value
	}
	if exit.Valid {
		value := int(exit.Int64)
		item.ExitCode = &value
	}
	item.ErrorSummary = publicError(summary)
	return item, err
}
func publicError(value string) string {
	if value == "" {
		return ""
	}
	redacted := Redact(value)
	if redacted != value {
		return "redacted error"
	}
	return redacted
}
