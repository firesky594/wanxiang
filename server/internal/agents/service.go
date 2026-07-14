package agents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/events"
	"wanxiang-agent/server/internal/files"
	"wanxiang-agent/server/internal/providers"
)

type Service struct {
	cfg              config.Config
	db               *sql.DB
	bus              *events.Bus
	providerRegistry *providers.Registry
}

var agentNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func NewService(cfg config.Config, db *sql.DB, buses ...*events.Bus) *Service {
	bus := events.NewBus(db)
	if len(buses) > 0 && buses[0] != nil {
		bus = buses[0]
	}
	return &Service{cfg: cfg, db: db, bus: bus, providerRegistry: providers.NewRegistry(&http.Client{Timeout: 20 * time.Second})}
}

func (s *Service) EnsureManager(ctx context.Context) (ManagerStatus, error) {
	dir := filepath.Join(s.cfg.AgentDir, "manager")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ManagerStatus{}, err
	}
	files := map[string]string{
		".gitignore":       "env\nlogs/runtime/*.log\n",
		"env.example":      "AGENT_PROVIDER_TYPE=openai\nAGENT_API_KEY=\nAGENT_BASE_URL=https://api.openai.com/v1\nAGENT_MODEL=\n",
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
	missing := missingAgentConfig(filepath.Join(dir, "env"))
	status := "online"
	if len(missing) > 0 {
		status = "blocked: missing_secret"
	} else {
		status = "configured"
		_ = s.db.QueryRowContext(ctx, `select status from agent_registry where name='manager'`).Scan(&status)
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
	values := readEnv(envPath)
	values[key] = value
	keys := make([]string, 0, len(values))
	for envKey := range values {
		keys = append(keys, envKey)
	}
	sort.Strings(keys)
	var content strings.Builder
	for _, envKey := range keys {
		content.WriteString(envKey + "=" + values[envKey] + "\n")
	}
	return os.WriteFile(envPath, []byte(content.String()), 0o600)
}

func (s *Service) SaveAgentConfig(ctx context.Context, input AgentConfigInput) (AgentConfigView, error) {
	if err := ValidateName(input.Name); err != nil {
		return AgentConfigView{}, err
	}
	input.ProviderType = strings.ToLower(strings.TrimSpace(input.ProviderType))
	if _, err := s.providerRegistry.Get(input.ProviderType); err != nil {
		return AgentConfigView{}, err
	}
	input.Model = strings.TrimSpace(input.Model)
	if input.Model == "" {
		return AgentConfigView{}, errors.New("model is required")
	}
	input.BaseURL = strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	if input.BaseURL == "" {
		input.BaseURL = providers.DefaultBaseURL(input.ProviderType)
	}
	parsed, err := url.Parse(input.BaseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return AgentConfigView{}, errors.New("base_url must be an absolute HTTP or HTTPS URL")
	}
	for _, value := range []string{input.ProviderType, input.BaseURL, input.Model, input.APIKey} {
		if strings.ContainsAny(value, "\r\n") {
			return AgentConfigView{}, errors.New("configuration values cannot contain newlines")
		}
	}
	dir, err := s.agentBase(input.Name)
	if err != nil {
		return AgentConfigView{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return AgentConfigView{}, err
	}
	existing := readEnv(filepath.Join(dir, "env"))
	apiKey := strings.TrimSpace(input.APIKey)
	if apiKey == "" {
		apiKey = existing["AGENT_API_KEY"]
		if apiKey == "" && input.Name == "manager" {
			apiKey = existing["MANAGER_API_KEY"]
		}
	}
	if apiKey == "" {
		return AgentConfigView{}, errors.New("api_key is required for a new agent configuration")
	}
	content := fmt.Sprintf("AGENT_PROVIDER_TYPE=%s\nAGENT_API_KEY=%s\nAGENT_BASE_URL=%s\nAGENT_MODEL=%s\n", input.ProviderType, apiKey, input.BaseURL, input.Model)
	if err := os.WriteFile(filepath.Join(dir, "env"), []byte(content), 0o600); err != nil {
		return AgentConfigView{}, err
	}
	if err := os.Chmod(filepath.Join(dir, "env"), 0o600); err != nil {
		return AgentConfigView{}, err
	}
	role := "agent"
	if input.Name == "manager" {
		role = "manager"
	}
	_, err = s.db.ExecContext(ctx, `insert into agent_registry(name,role,dir,status,last_heartbeat,current_model) values(?,?,?,'configured',datetime('now'),?)
		on conflict(name) do update set role=excluded.role,dir=excluded.dir,status=excluded.status,last_heartbeat=excluded.last_heartbeat,current_model=excluded.current_model`, input.Name, role, dir, input.Model)
	if err != nil {
		return AgentConfigView{}, err
	}
	return AgentConfigView{Name: input.Name, ProviderType: input.ProviderType, BaseURL: input.BaseURL, Model: input.Model, SecretConfigured: true, Status: "configured"}, nil
}

func (s *Service) GetAgentConfig(ctx context.Context, name string) (AgentConfigView, error) {
	runtimeCfg, err := s.loadRuntimeConfig(ctx, name)
	return runtimeCfg.AgentConfigView, err
}

func (s *Service) ListAgentConfigs(ctx context.Context) ([]AgentConfigView, error) {
	entries, err := os.ReadDir(s.cfg.AgentDir)
	if os.IsNotExist(err) {
		return []AgentConfigView{}, nil
	}
	if err != nil {
		return nil, err
	}
	views := make([]AgentConfigView, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || ValidateName(entry.Name()) != nil {
			continue
		}
		view, err := s.GetAgentConfig(ctx, entry.Name())
		if err != nil {
			values := readEnv(filepath.Join(s.cfg.AgentDir, entry.Name(), "env"))
			providerType := strings.ToLower(values["AGENT_PROVIDER_TYPE"])
			baseURL := values["AGENT_BASE_URL"]
			if baseURL == "" {
				baseURL = providers.DefaultBaseURL(providerType)
			}
			view = AgentConfigView{Name: entry.Name(), ProviderType: providerType, BaseURL: baseURL, Model: values["AGENT_MODEL"], SecretConfigured: values["AGENT_API_KEY"] != "" || (entry.Name() == "manager" && values["MANAGER_API_KEY"] != ""), Status: "blocked: missing_config"}
		}
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	return views, nil
}

func (s *Service) ProbeAgent(ctx context.Context, name string) (AgentConfigView, error) {
	runtimeCfg, err := s.loadRuntimeConfig(ctx, name)
	if err != nil {
		return AgentConfigView{}, err
	}
	provider, err := s.providerRegistry.Get(runtimeCfg.ProviderType)
	if err == nil {
		_, err = provider.Chat(ctx, providers.Config{APIKey: runtimeCfg.APIKey, BaseURL: runtimeCfg.BaseURL, Model: runtimeCfg.Model}, []providers.Message{{Role: "user", Content: "Reply OK."}}, 1)
	}
	status := "online"
	lastError := ""
	if err != nil {
		status = "blocked: provider_error"
		lastError = err.Error()
	}
	role := "agent"
	if name == "manager" {
		role = "manager"
	}
	dir, _ := s.agentBase(name)
	_, dbErr := s.db.ExecContext(ctx, `insert into agent_registry(name,role,dir,status,last_heartbeat,current_model) values(?,?,?,?,datetime('now'),?)
		on conflict(name) do update set status=excluded.status,last_heartbeat=excluded.last_heartbeat,current_model=excluded.current_model`, name, role, dir, status, runtimeCfg.Model)
	if dbErr != nil {
		return AgentConfigView{}, dbErr
	}
	view := runtimeCfg.AgentConfigView
	view.Status = status
	view.LastError = lastError
	return view, err
}

func (s *Service) ChatAgent(ctx context.Context, name string, messages []providers.Message, maxTokens int) (providers.Result, error) {
	runtimeCfg, err := s.loadRuntimeConfig(ctx, name)
	if err != nil {
		return providers.Result{}, err
	}
	provider, err := s.providerRegistry.Get(runtimeCfg.ProviderType)
	if err != nil {
		return providers.Result{}, err
	}
	return provider.Chat(ctx, providers.Config{APIKey: runtimeCfg.APIKey, BaseURL: runtimeCfg.BaseURL, Model: runtimeCfg.Model}, messages, maxTokens)
}

func (s *Service) loadRuntimeConfig(ctx context.Context, name string) (runtimeConfig, error) {
	dir, err := s.agentBase(name)
	if err != nil {
		return runtimeConfig{}, err
	}
	values := readEnv(filepath.Join(dir, "env"))
	providerType := strings.ToLower(strings.TrimSpace(values["AGENT_PROVIDER_TYPE"]))
	apiKey := strings.TrimSpace(values["AGENT_API_KEY"])
	if apiKey == "" && name == "manager" {
		apiKey = strings.TrimSpace(values["MANAGER_API_KEY"])
	}
	baseURL := strings.TrimRight(strings.TrimSpace(values["AGENT_BASE_URL"]), "/")
	if baseURL == "" {
		baseURL = providers.DefaultBaseURL(providerType)
	}
	model := strings.TrimSpace(values["AGENT_MODEL"])
	if providerType == "" || apiKey == "" || model == "" {
		return runtimeConfig{}, errors.New("agent provider_type, api_key, and model are required")
	}
	if _, err := s.providerRegistry.Get(providerType); err != nil {
		return runtimeConfig{}, err
	}
	status := "configured"
	_ = s.db.QueryRowContext(ctx, `select status from agent_registry where name=?`, name).Scan(&status)
	return runtimeConfig{AgentConfigView: AgentConfigView{Name: name, ProviderType: providerType, BaseURL: baseURL, Model: model, SecretConfigured: true, Status: status}, APIKey: apiKey}, nil
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

func readEnv(path string) map[string]string {
	values := map[string]string{}
	content, err := os.ReadFile(path)
	if err != nil {
		return values
	}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			values[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return values
}

func missingAgentConfig(path string) []string {
	values := readEnv(path)
	missing := []string{}
	if values["AGENT_PROVIDER_TYPE"] == "" {
		missing = append(missing, "AGENT_PROVIDER_TYPE")
	}
	if values["AGENT_API_KEY"] == "" && values["MANAGER_API_KEY"] == "" {
		missing = append(missing, "AGENT_API_KEY")
	}
	if values["AGENT_MODEL"] == "" {
		missing = append(missing, "AGENT_MODEL")
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
  - AGENT_PROVIDER_TYPE
  - AGENT_API_KEY
  - AGENT_MODEL
model_profiles:
  planning:
    provider_env: AGENT_PROVIDER_TYPE
    api_key_env: AGENT_API_KEY
    base_url_env: AGENT_BASE_URL
    model_env: AGENT_MODEL
autonomy:
  update_non_secret_agent_config: true
git_policy:
  commit_non_secret_changes: true
`
}
