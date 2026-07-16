package httpapi

import (
	"database/sql"
	"encoding/json"
	"github.com/go-chi/chi/v5"
	"net/http"
	"strconv"
	"strings"
	"wanxiang-agent/server/internal/gitx"
	"wanxiang-agent/server/internal/pipelines"
)

func pipelineError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
func handleListPipelines(s *pipelines.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, e := s.List(r.Context())
		if e != nil {
			pipelineError(w, 500, e.Error())
			return
		}
		writeJSON(w, 200, items)
	}
}
func handleGetPipeline(s *pipelines.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, e := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if e != nil {
			pipelineError(w, 400, "invalid id")
			return
		}
		x, e := s.Get(r.Context(), id)
		if e != nil {
			pipelineError(w, 404, e.Error())
			return
		}
		writeJSON(w, 200, x)
	}
}
func handleStartPipeline(db *sql.DB, s *pipelines.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		project, e := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if e != nil {
			pipelineError(w, 400, "invalid id")
			return
		}
		var in struct {
			TaskID         *int64 `json:"task_id"`
			IdempotencyKey string `json:"idempotency_key"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			pipelineError(w, 400, "invalid body")
			return
		}
		var dir, recorded string
		if e = db.QueryRowContext(r.Context(), `select dir,coalesce(main_commit,'') from projects where id=?`, project).Scan(&dir, &recorded); e != nil {
			pipelineError(w, 404, "project not found")
			return
		}
		branch, e := gitx.Run(r.Context(), dir, "branch", "--show-current")
		if e != nil || strings.TrimSpace(branch) != "main" {
			pipelineError(w, 409, "project must be on main")
			return
		}
		status, e := gitx.Run(r.Context(), dir, "status", "--porcelain")
		if e != nil || strings.TrimSpace(status) != "" {
			pipelineError(w, 409, "project must be clean")
			return
		}
		head, e := gitx.Run(r.Context(), dir, "rev-parse", "HEAD")
		if e != nil {
			pipelineError(w, 409, "project commit unavailable")
			return
		}
		commit := strings.TrimSpace(head)
		if recorded != "" && recorded != commit {
			pipelineError(w, 409, "project commit drifted")
			return
		}
		d, e := pipelines.LoadDefinition(dir)
		if e != nil {
			pipelineError(w, 409, e.Error())
			return
		}
		run, e := s.Start(r.Context(), pipelines.StartInput{ProjectID: project, TaskID: in.TaskID, Definition: d, SafeCommit: commit, IdempotencyKey: in.IdempotencyKey, RequestedBy: "admin"})
		if e != nil {
			pipelineError(w, 409, e.Error())
			return
		}
		writeJSON(w, 201, run)
	}
}
func handleConfirmPipeline(s *pipelines.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, e := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if e != nil {
			pipelineError(w, 400, "invalid id")
			return
		}
		x, e := s.Confirm(r.Context(), id, chi.URLParam(r, "step"), "admin")
		if e != nil {
			pipelineError(w, 409, e.Error())
			return
		}
		writeJSON(w, 200, x)
	}
}
func handleConfirmRollback(s *pipelines.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, e := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if e != nil {
			pipelineError(w, 400, "invalid id")
			return
		}
		if e = s.ConfirmRollback(r.Context(), id, "admin"); e != nil {
			pipelineError(w, 409, e.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	}
}
