package agents

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/events"
	"wanxiang-agent/server/internal/files"
)

type Service struct {
	cfg config.Config
	db  *sql.DB
	bus *events.Bus
}

var agentNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func NewService(cfg config.Config, db *sql.DB, buses ...*events.Bus) *Service {
	bus := events.NewBus(db)
	if len(buses) > 0 && buses[0] != nil {
		bus = buses[0]
	}
	return &Service{cfg: cfg, db: db, bus: bus}
}

func (s *Service) EnsureManager(ctx context.Context) (ManagerStatus, error) {
	dir := filepath.Join(s.cfg.AgentDir, "manager")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ManagerStatus{}, err
	}
	files := map[string]string{
		".gitignore":       "env\nlogs/runtime/*.log\n",
		"env.example":      "MANAGER_API_KEY=\n",
		"system_prompt.md": "# Manager Agent\n\nYou plan tasks, manage agents, and enforce human blocking issues.\n",
		"agent.yaml":       managerYAML(),
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return ManagerStatus{}, err
			}
		}
	}
	for _, sub := range []string{"skills", "mcps", "memory/summaries", "memory/decisions", "memory/task-notes", "logs/runtime", "logs/conversations"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return ManagerStatus{}, err
		}
	}
	missing := missingEnv(filepath.Join(dir, "env"), []string{"MANAGER_API_KEY"})
	status := "online"
	if len(missing) > 0 {
		status = "blocked: missing_secret"
	}
	_, err := s.db.ExecContext(ctx, `insert into agent_registry(name, role, dir, status, last_heartbeat) values('manager','manager',?,?,datetime('now'))
		on conflict(name) do update set status=excluded.status, dir=excluded.dir, last_heartbeat=datetime('now')`, dir, status)
	if err != nil {
		return ManagerStatus{}, err
	}
	return ManagerStatus{Status: status, MissingEnv: missing}, nil
}

func (s *Service) ManagerReady(ctx context.Context) (bool, error) {
	status, err := s.EnsureManager(ctx)
	if err != nil {
		return false, err
	}
	return status.Status == "online", nil
}

func (s *Service) SaveManagerSecret(ctx context.Context, key, value string) error {
	dir := filepath.Join(s.cfg.AgentDir, "manager")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	envPath := filepath.Join(dir, "env")
	line := key + "=" + value + "\n"
	return os.WriteFile(envPath, []byte(line), 0o600)
}

func (s *Service) Heartbeat(ctx context.Context, input HeartbeatInput) error {
	if input.Name == "" || input.Role == "" || input.Status == "" {
		return errors.New("name, role, and status are required")
	}
	if input.Name == "manager" {
		ready, err := s.ManagerReady(ctx)
		if err != nil {
			return err
		}
		if !ready {
			return errors.New("manager is blocked: MANAGER_API_KEY is missing")
		}
		input.Role = "manager"
	}
	dir, err := s.agentBase(input.Name)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `insert into agent_registry(name,role,dir,status,last_heartbeat,current_task_id,current_model) values(?,?,?,?,datetime('now'),?,?)
		on conflict(name) do update set status=excluded.status,last_heartbeat=datetime('now'),current_task_id=excluded.current_task_id,current_model=excluded.current_model`,
		input.Name, input.Role, dir, input.Status, input.CurrentTaskID, input.CurrentModel)
	if err != nil {
		return err
	}
	return s.bus.PublishJSON(ctx, input.CurrentTaskID, "agent.heartbeat", input.Name, map[string]any{
		"name": input.Name, "role": input.Role, "status": input.Status, "current_task_id": input.CurrentTaskID, "current_model": input.CurrentModel,
	})
}

func (s *Service) RecordTokenUsage(ctx context.Context, input TokenUsageInput) error {
	if input.AgentName == "" || input.Model == "" {
		return errors.New("agent_name and model are required")
	}
	if err := ValidateName(input.AgentName); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `insert into token_usage(task_id,step_id,agent_name,model,input_tokens,output_tokens,created_at) values(?,?,?,?,?,?,?)`,
		input.TaskID, input.StepID, input.AgentName, input.Model, input.InputTokens, input.OutputTokens, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	return s.bus.PublishJSON(ctx, input.TaskID, "token.usage", input.AgentName, map[string]any{
		"step_id": input.StepID, "agent_name": input.AgentName, "model": input.Model, "input_tokens": input.InputTokens, "output_tokens": input.OutputTokens,
	})
}

func (s *Service) WriteMemory(ctx context.Context, agentName, relPath, content string) error {
	agentBase, err := s.agentBase(agentName)
	if err != nil {
		return err
	}
	base := filepath.Join(agentBase, "memory")
	return writeAgentFile(base, relPath, content, 0o644)
}

func (s *Service) WriteLog(ctx context.Context, agentName, relPath, content string) error {
	agentBase, err := s.agentBase(agentName)
	if err != nil {
		return err
	}
	base := filepath.Join(agentBase, "logs")
	return writeAgentFile(base, relPath, content, 0o644)
}

func ValidateName(name string) error {
	if !agentNamePattern.MatchString(name) {
		return errors.New("invalid agent name")
	}
	return nil
}

func (s *Service) agentBase(agentName string) (string, error) {
	if err := ValidateName(agentName); err != nil {
		return "", err
	}
	return files.UnderRoot(s.cfg.AgentDir, filepath.Join(s.cfg.AgentDir, agentName))
}

func writeAgentFile(base, relPath, content string, perm os.FileMode) error {
	target := filepath.Join(base, relPath)
	safe, err := files.UnderRoot(base, target)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(safe), 0o755); err != nil {
		return err
	}
	return os.WriteFile(safe, []byte(content), perm)
}

func missingEnv(path string, keys []string) []string {
	content, err := os.ReadFile(path)
	values := map[string]bool{}
	if err == nil {
		for _, line := range strings.Split(string(content), "\n") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
				values[strings.TrimSpace(parts[0])] = true
			}
		}
	}
	var missing []string
	for _, key := range keys {
		if !values[key] {
			missing = append(missing, key)
		}
	}
	return missing
}

func managerYAML() string {
	return `role: manager
capabilities:
  - planning
  - review
  - agent-governance
required_env:
  - MANAGER_API_KEY
model_profiles:
  planning:
    provider_env: MANAGER_PROVIDER
    api_key_env: MANAGER_API_KEY
autonomy:
  update_non_secret_agent_config: true
git_policy:
  commit_non_secret_changes: true
`
}
