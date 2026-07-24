package agents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
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
var agentRolePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// ErrProviderUnavailable 标识 Agent Provider 探测失败，可由巡检器稍后重试。
var ErrProviderUnavailable = errors.New("agent provider is unavailable")

type providerUnavailableError struct {
	err error
}

func (e *providerUnavailableError) Error() string {
	return e.err.Error()
}

func (e *providerUnavailableError) Unwrap() []error {
	return []error{ErrProviderUnavailable, e.err}
}

// NewService 创建 Agent 配置与运行服务。
func NewService(cfg config.Config, db *sql.DB, buses ...*events.Bus) *Service {
	bus := events.NewBus(db)
	if len(buses) > 0 && buses[0] != nil {
		bus = buses[0]
	}
	return &Service{cfg: cfg, db: db, bus: bus, providerRegistry: providers.NewRegistry(&http.Client{Timeout: 20 * time.Second})}
}

// EnsureManager 初始化 Manager 目录并同步注册状态。
func (s *Service) EnsureManager(ctx context.Context) (ManagerStatus, error) {
	dir, err := s.ensureAgentDirectory("manager")
	if err != nil {
		return ManagerStatus{}, err
	}
	templates := map[string]string{
		".gitignore":       "env\nlogs/runtime/*.log\n",
		"env.example":      "AGENT_PROVIDER_TYPE=openai\nAGENT_API_KEY=\nAGENT_BASE_URL=https://api.openai.com/v1\nAGENT_MODEL=\n",
		"system_prompt.md": "# Manager Agent\n\nYou plan tasks, manage agents, and enforce human blocking issues.\n",
		"agent.yaml":       managerYAML(),
	}
	for name, content := range templates {
		if err := writeAgentFileOnce(dir, name, content, 0o644); err != nil {
			return ManagerStatus{}, err
		}
	}
	for _, sub := range []string{"skills", "mcps", "memory/summaries", "memory/decisions", "memory/task-notes", "logs/runtime", "logs/conversations"} {
		if err := ensureAgentSubdirectory(dir, sub); err != nil {
			return ManagerStatus{}, err
		}
	}
	values, err := readAgentEnv(dir)
	if err != nil {
		return ManagerStatus{}, err
	}
	missing := missingAgentConfig(values)
	status := "online"
	if len(missing) > 0 {
		status = "blocked: missing_secret"
	} else {
		status = "configured"
		_ = s.db.QueryRowContext(ctx, `select status from agent_registry where name='manager'`).Scan(&status)
	}
	_, err = s.db.ExecContext(ctx, `insert into agent_registry(name, role, dir, status, last_heartbeat) values('manager','manager',?,?,datetime('now'))
		on conflict(name) do update set status=excluded.status, dir=excluded.dir, last_heartbeat=datetime('now')`, dir, status)
	if err != nil {
		return ManagerStatus{}, err
	}
	return ManagerStatus{Status: status, MissingEnv: missing}, nil
}

// ManagerReady 确认 Manager 已初始化且在线。
func (s *Service) ManagerReady(ctx context.Context) (bool, error) {
	status, err := s.EnsureManager(ctx)
	if err != nil {
		return false, err
	}
	return status.Status == "online", nil
}

// SaveManagerSecret 安全写入 Manager 环境密钥。
func (s *Service) SaveManagerSecret(ctx context.Context, key, value string) error {
	dir, err := s.ensureAgentDirectory("manager")
	if err != nil {
		return err
	}
	values, err := readAgentEnv(dir)
	if err != nil {
		return err
	}
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
	return writeAgentFile(dir, "env", content.String(), 0o600)
}

// SaveAgentConfig 校验并持久化 Agent 运行配置。
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
	dir, err := s.ensureAgentDirectory(input.Name)
	if err != nil {
		return AgentConfigView{}, err
	}
	existing, err := readAgentEnv(dir)
	if err != nil {
		return AgentConfigView{}, err
	}
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
	if err := writeAgentFile(dir, "env", content, 0o600); err != nil {
		return AgentConfigView{}, err
	}
	role := s.agentRole(ctx, input.Name, dir)
	_, err = s.db.ExecContext(ctx, `insert into agent_registry(name,role,dir,status,last_heartbeat,current_model) values(?,?,?,'configured',datetime('now'),?)
		on conflict(name) do update set role=excluded.role,dir=excluded.dir,status=excluded.status,last_heartbeat=excluded.last_heartbeat,current_model=excluded.current_model`, input.Name, role, dir, input.Model)
	if err != nil {
		return AgentConfigView{}, err
	}
	return AgentConfigView{Name: input.Name, ProviderType: input.ProviderType, BaseURL: input.BaseURL, Model: input.Model, SecretConfigured: true, Status: "configured"}, nil
}

// GetAgentConfig 读取指定 Agent 的脱敏运行配置。
func (s *Service) GetAgentConfig(ctx context.Context, name string) (AgentConfigView, error) {
	runtimeCfg, err := s.loadRuntimeConfig(ctx, name)
	return runtimeCfg.AgentConfigView, err
}

// ListAgentConfigs 列出全部 Agent 的脱敏配置与状态。
func (s *Service) ListAgentConfigs(ctx context.Context) ([]AgentConfigView, error) {
	info, err := os.Lstat(s.cfg.AgentDir)
	if os.IsNotExist(err) {
		return []AgentConfigView{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("agent root is not a safe directory")
	}
	entries, err := os.ReadDir(s.cfg.AgentDir)
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
			dir, pathErr := s.agentBase(entry.Name())
			if pathErr != nil {
				return nil, pathErr
			}
			values, pathErr := readAgentEnv(dir)
			if pathErr != nil {
				return nil, pathErr
			}
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

// ProbeAgent 调用模型探测 Agent 并更新在线状态。
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
	dir, _ := s.agentBase(name)
	role := s.agentRole(ctx, name, dir)
	_, dbErr := s.db.ExecContext(ctx, `insert into agent_registry(name,role,dir,status,last_heartbeat,current_model) values(?,?,?,?,datetime('now'),?)
		on conflict(name) do update set role=excluded.role,status=excluded.status,last_heartbeat=excluded.last_heartbeat,current_model=excluded.current_model`, name, role, dir, status, runtimeCfg.Model)
	if dbErr != nil {
		return AgentConfigView{}, dbErr
	}
	view := runtimeCfg.AgentConfigView
	view.Status = status
	view.LastError = lastError
	if err != nil {
		return view, &providerUnavailableError{err: err}
	}
	return view, err
}

// ChatAgent 按 Agent 配置调用模型对话。
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
	values, err := readAgentEnv(dir)
	if err != nil {
		return runtimeConfig{}, err
	}
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
	if status == "blocked: missing_config" {
		if _, err := s.db.ExecContext(ctx, `update agent_registry set status='configured',current_model=? where name=? and status='blocked: missing_config'`, model, name); err != nil {
			return runtimeConfig{}, err
		}
		status = "configured"
	}
	return runtimeConfig{AgentConfigView: AgentConfigView{Name: name, ProviderType: providerType, BaseURL: baseURL, Model: model, SecretConfigured: true, Status: status}, APIKey: apiKey}, nil
}

// Heartbeat 更新 Agent 心跳并发布心跳事件。
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

func (s *Service) keepAlive(ctx context.Context, name, role, status string) error {
	if name == "" || role == "" || status == "" {
		return errors.New("name, role, and status are required")
	}
	if name == "manager" {
		ready, err := s.ManagerReady(ctx)
		if err != nil {
			return err
		}
		if !ready {
			return errors.New("manager is blocked: MANAGER_API_KEY is missing")
		}
		role = "manager"
	}
	dir, err := s.agentBase(name)
	if err != nil {
		return err
	}
	runtimeCfg, err := s.loadRuntimeConfig(ctx, name)
	if err != nil {
		if markErr := s.markMissingConfig(ctx, name, role, dir); markErr != nil {
			return errors.Join(err, fmt.Errorf("mark agent missing config: %w", markErr))
		}
		return err
	}
	if runtimeCfg.Status != "online" {
		return fmt.Errorf("agent %s must be probed online before keepalive", name)
	}
	_, err = s.db.ExecContext(ctx, `insert into agent_registry(name,role,dir,status,last_heartbeat) values(?,?,?,?,datetime('now'))
		on conflict(name) do update set role=excluded.role,status=excluded.status,last_heartbeat=datetime('now')`,
		name, role, dir, status)
	if err != nil {
		return err
	}
	return nil
}

func (s *Service) markMissingConfig(ctx context.Context, name, role, dir string) error {
	_, err := s.db.ExecContext(ctx, `insert into agent_registry(name,role,dir,status,last_heartbeat)
		values(?,?,?,'blocked: missing_config',datetime('now'))
		on conflict(name) do update set role=excluded.role,dir=excluded.dir,status=excluded.status,last_heartbeat=excluded.last_heartbeat`,
		name, role, dir)
	return err
}

// RecordTokenUsage 记录 Agent 模型用量并发布事件。
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

// WriteMemory 安全写入 Agent 记忆目录文件。
func (s *Service) WriteMemory(ctx context.Context, agentName, relPath, content string) error {
	agentBase, err := s.agentBase(agentName)
	if err != nil {
		return err
	}
	base := filepath.Join(agentBase, "memory")
	return writeAgentFile(base, relPath, content, 0o644)
}

// WriteLog 安全写入 Agent 日志目录文件。
func (s *Service) WriteLog(ctx context.Context, agentName, relPath, content string) error {
	agentBase, err := s.agentBase(agentName)
	if err != nil {
		return err
	}
	base := filepath.Join(agentBase, "logs")
	return writeAgentFile(base, relPath, content, 0o644)
}

// ValidateName 校验 Agent 名称格式。
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

func (s *Service) ensureAgentDirectory(agentName string) (string, error) {
	dir, err := s.agentBase(agentName)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	safe, err := files.UnderRoot(s.cfg.AgentDir, dir)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(safe)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("agent path is not a safe directory")
	}
	return safe, nil
}

func (s *Service) agentRole(ctx context.Context, name, dir string) string {
	if name == "manager" {
		return "manager"
	}
	path := filepath.Join(dir, "agent.yaml")
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 && info.Size() <= 64*1024 {
		if content, readErr := os.ReadFile(path); readErr == nil {
			for _, line := range strings.Split(string(content), "\n") {
				if len(line) != len(strings.TrimLeft(line, " \t")) {
					continue
				}
				parts := strings.SplitN(strings.TrimSpace(line), ":", 2)
				if len(parts) != 2 || strings.TrimSpace(parts[0]) != "role" {
					continue
				}
				role := strings.TrimSpace(parts[1])
				if agentRolePattern.MatchString(role) {
					return role
				}
				break
			}
		}
	}
	var role string
	if s.db != nil {
		_ = s.db.QueryRowContext(ctx, `select role from agent_registry where name=?`, name).Scan(&role)
	}
	if agentRolePattern.MatchString(role) {
		return role
	}
	return "agent"
}

func writeAgentFile(base, relPath, content string, perm os.FileMode) error {
	target := filepath.Join(base, relPath)
	safe, err := files.UnderRoot(base, target)
	if err != nil {
		return err
	}
	parent := filepath.Dir(safe)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	safe, err = files.UnderRoot(base, safe)
	if err != nil {
		return err
	}
	if err := requireSafeDirectory(filepath.Dir(safe)); err != nil {
		return err
	}
	if info, statErr := os.Lstat(safe); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("target is not a safe regular file")
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	return atomicWriteFile(safe, content, perm)
}

func writeAgentFileOnce(base, relPath, content string, perm os.FileMode) error {
	target, err := files.UnderRoot(base, filepath.Join(base, relPath))
	if err != nil {
		return err
	}
	info, err := os.Lstat(target)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("existing path is not a safe regular file")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	return writeAgentFile(base, relPath, content, perm)
}

func ensureAgentSubdirectory(base, relPath string) error {
	target, err := files.UnderRoot(base, filepath.Join(base, relPath))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	target, err = files.UnderRoot(base, target)
	if err != nil {
		return err
	}
	return requireSafeDirectory(target)
}

func requireSafeDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("path is not a safe directory")
	}
	return nil
}

func atomicWriteFile(path, content string, perm os.FileMode) error {
	parent := filepath.Dir(path)
	file, err := os.CreateTemp(parent, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err = file.Chmod(perm); err != nil {
		file.Close()
		return err
	}
	if _, err = file.WriteString(content); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(tempPath, path); err != nil {
		return err
	}
	return nil
}

func readAgentEnv(dir string) (map[string]string, error) {
	values := map[string]string{}
	path, err := files.UnderRoot(dir, filepath.Join(dir, "env"))
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return values, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("agent env is not a safe regular file")
	}
	if info.Size() > 1024*1024 {
		return nil, errors.New("agent env is too large")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, errors.New("agent env changed during secure read")
	}
	content, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if err != nil {
		return nil, err
	}
	if len(content) > 1024*1024 {
		return nil, errors.New("agent env is too large")
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
	return values, nil
}

func missingAgentConfig(values map[string]string) []string {
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
