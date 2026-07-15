package workspaces

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
)

func (s *Service) AuthorizeAgent(ctx context.Context, agent string, taskID, stepID int64, relativePath string) error {
	if agent == "" || relativePath == "" || filepath.IsAbs(relativePath) || strings.Contains(relativePath, "\\") {
		return errors.New("invalid assignment scope")
	}
	clean := filepath.Clean(relativePath)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return errors.New("path escapes assignment scope")
	}
	var scopeJSON string
	err := s.db.QueryRowContext(ctx, `select write_scope_json from project_workspaces where task_id=? and step_id=? and agent_name=? and status='ready'`, taskID, stepID, agent).Scan(&scopeJSON)
	if err != nil {
		return errors.New("agent does not own active assignment")
	}
	var scopes []string
	if json.Unmarshal([]byte(scopeJSON), &scopes) != nil {
		return errors.New("invalid stored assignment scope")
	}
	for _, scope := range scopes {
		if scope == "." || clean == scope || strings.HasPrefix(clean, filepath.Clean(scope)+string(filepath.Separator)) {
			return nil
		}
	}
	return errors.New("path outside assignment scope")
}
