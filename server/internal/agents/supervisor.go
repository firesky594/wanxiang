package agents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"wanxiang-agent/server/internal/events"
	"wanxiang-agent/server/internal/files"
)

const (
	defaultManagerSupervisionInterval = 15 * time.Second
	maxAgentProbeRetryDelay           = 5 * time.Minute
	activeAgentProbeInterval          = 2 * time.Minute
	managerRuntimeStatusPath          = "summaries/runtime-status.md"
	legacyGeneratedEnvExample         = "# 在本地安全配置实际 Provider 凭据，不要提交密钥。\nPROVIDER_API_KEY=\n"
	standardGeneratedEnvExample       = "# 在本地安全配置实际 Provider 凭据，不要提交密钥。\nAGENT_PROVIDER_TYPE=openai\nAGENT_API_KEY=\nAGENT_BASE_URL=https://api.openai.com/v1\nAGENT_MODEL=\n"
)

var errAgentProviderUnavailable = errors.New("agent provider is unavailable; retry scheduled")

// AgentRuntimeStarter 提供 Agent 连通性探测、在线心跳恢复与活动状态查询。
type AgentRuntimeStarter interface {
	StartAgent(context.Context, string) (AgentConfigView, error)
	StartConfiguredAgent(context.Context, string) (AgentConfigView, error)
	IsAgentActive(string) bool
}

// ManagerSupervisor 按固定周期巡检项目、任务和 Agent 的确定性运行状态。
type ManagerSupervisor struct {
	service  *Service
	bus      *events.Bus
	starter  AgentRuntimeStarter
	interval time.Duration

	lifecycleMu     sync.Mutex
	scanMu          sync.Mutex
	mu              sync.Mutex
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	fingerprintInit bool
	lastFingerprint string
	lastFailure     string
	probeRetries    map[string]agentProbeRetry
	lastProbes      map[string]time.Time
	now             func() time.Time
}

type agentProbeRetry struct {
	failures    int
	nextAttempt time.Time
}

type managerSupervisionError struct {
	phase string
	err   error
}

// Error 返回带巡检阶段的错误描述，便于定位失败环节。
func (e *managerSupervisionError) Error() string {
	return e.phase + ": " + e.err.Error()
}

// Unwrap 返回巡检阶段包装的原始错误。
func (e *managerSupervisionError) Unwrap() error {
	return e.err
}

type managerProjectStatus struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	Status        string `json:"status"`
	TaskCount     int64  `json:"task_count"`
	OpenTaskCount int64  `json:"open_task_count"`
	BlockedTasks  int64  `json:"blocked_tasks"`
}

type managerTaskStatus struct {
	ID             int64  `json:"id"`
	ProjectID      int64  `json:"project_id"`
	Status         string `json:"status"`
	StepCount      int64  `json:"step_count"`
	ActiveSteps    int64  `json:"active_steps"`
	BlockedSteps   int64  `json:"blocked_steps"`
	CompletedSteps int64  `json:"completed_steps"`
}

type managerAgentStatus struct {
	Name             string `json:"name"`
	Role             string `json:"role"`
	Status           string `json:"status"`
	SecretConfigured bool   `json:"secret_configured"`
	CurrentTaskID    int64  `json:"current_task_id,omitempty"`
	ActiveTasks      int64  `json:"active_tasks"`
}

type managerRuntimeSnapshot struct {
	Projects []managerProjectStatus `json:"projects"`
	Tasks    []managerTaskStatus    `json:"tasks"`
	Agents   []managerAgentStatus   `json:"agents"`
}

type baselineAgentDefinition struct {
	name         string
	role         string
	capabilities []string
	prompt       string
}

var baselineAgentDefinitions = []baselineAgentDefinition{
	{name: "main-backend-engineer", role: "backend", capabilities: []string{"backend"}, prompt: "负责服务端设计、实现与排障；遵守项目写入范围和验收条件，缺少配置或权限时保持阻塞并报告。\n"},
	{name: "main-frontend-engineer", role: "frontend", capabilities: []string{"frontend"}, prompt: "负责前端页面、交互与状态管理；遵守项目设计规范和验收条件，缺少配置或权限时保持阻塞并报告。\n"},
	{name: "main-test-engineer", role: "testing", capabilities: []string{"qa", "testing"}, prompt: "负责测试设计、回归验证与缺陷证据；不伪造通过结果，缺少环境或权限时保持阻塞并报告。\n"},
	{name: "main-technical-manager", role: "technical-manager", capabilities: []string{"planning", "review", "technical-management"}, prompt: "负责技术拆解、依赖协调与交付审查；不越权替代总管，缺少信息或权限时保持阻塞并报告。\n"},
	{name: "main-ui-analyst", role: "ui-analyst", capabilities: []string{"ui-analysis"}, prompt: "负责界面分析、视觉规范与可用性建议；以现有设计和验收条件为准，缺少资料时保持阻塞并报告。\n"},
	{name: "main-operations-engineer", role: "operations", capabilities: []string{"deployment", "operations"}, prompt: "负责运行状态、部署验证与故障处置；不擅自扩大线上变更范围，缺少授权时保持阻塞并报告。\n"},
}

// NewManagerSupervisor 创建总管确定性巡检器，并以有界退避恢复 Provider 探测失败的 Agent。
func NewManagerSupervisor(service *Service, bus *events.Bus, starter AgentRuntimeStarter, interval time.Duration) *ManagerSupervisor {
	if interval <= 0 {
		interval = defaultManagerSupervisionInterval
	}
	if bus == nil && service != nil {
		bus = service.bus
	}
	return &ManagerSupervisor{
		service: service, bus: bus, starter: starter, interval: interval,
		probeRetries: map[string]agentProbeRetry{}, lastProbes: map[string]time.Time{}, now: time.Now,
	}
}

// Start 启动总管周期巡检；重复调用不会创建额外循环。
func (s *ManagerSupervisor) Start() {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		_ = s.Scan(ctx)
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = s.Scan(ctx)
			}
		}
	}()
}

// Close 停止总管周期巡检并等待当前扫描退出。
func (s *ManagerSupervisor) Close() {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		s.wg.Wait()
	}
}

// Scan 执行一次项目、任务和 Agent 巡检并持久化脱敏状态。
func (s *ManagerSupervisor) Scan(ctx context.Context) error {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	err := s.scan(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		if publishErr := s.publishFailure(ctx, err); publishErr != nil {
			return errors.Join(err, fmt.Errorf("publish supervision failure: %w", publishErr))
		}
		return err
	}
	s.mu.Lock()
	s.lastFailure = ""
	s.mu.Unlock()
	return nil
}

func (s *ManagerSupervisor) scan(ctx context.Context) error {
	if s.service == nil || s.service.db == nil {
		return &managerSupervisionError{phase: "initialize", err: errors.New("manager supervisor service is unavailable")}
	}
	if err := s.ensureBaselineAgents(ctx); err != nil {
		return &managerSupervisionError{phase: "baseline", err: err}
	}
	if err := s.repairLegacyGeneratedAgentTemplates(ctx); err != nil {
		return &managerSupervisionError{phase: "legacy_templates", err: err}
	}
	restoreErr := s.restoreAgentRuntimes(ctx)
	snapshot, err := s.capture(ctx)
	if err != nil {
		return &managerSupervisionError{phase: "capture", err: err}
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return &managerSupervisionError{phase: "encode", err: err}
	}
	sum := sha256.Sum256(encoded)
	fingerprint := hex.EncodeToString(sum[:])
	checkedAt := s.currentTime().UTC()
	s.mu.Lock()
	if !s.fingerprintInit {
		s.lastFingerprint = s.persistedFingerprint()
		s.fingerprintInit = true
	}
	previousFingerprint := s.lastFingerprint
	s.mu.Unlock()
	if err := s.service.WriteMemory(ctx, "manager", managerRuntimeStatusPath, formatManagerRuntimeStatus(snapshot, fingerprint, checkedAt)); err != nil {
		return &managerSupervisionError{phase: "persist", err: err}
	}
	changed := previousFingerprint != fingerprint
	if !changed {
		if restoreErr != nil {
			return &managerSupervisionError{phase: "restore", err: restoreErr}
		}
		return nil
	}
	if s.bus == nil {
		return &managerSupervisionError{phase: "publish", err: errors.New("manager supervisor event bus is unavailable")}
	}
	blockedTasks := int64(0)
	for _, task := range snapshot.Tasks {
		if strings.HasPrefix(task.Status, "blocked") {
			blockedTasks++
		}
	}
	if err := s.bus.PublishJSON(ctx, nil, "manager.supervision.changed", "manager", map[string]any{
		"fingerprint":   fingerprint,
		"project_count": len(snapshot.Projects),
		"task_count":    len(snapshot.Tasks),
		"agent_count":   len(snapshot.Agents),
		"blocked_tasks": blockedTasks,
	}); err != nil {
		return &managerSupervisionError{phase: "publish", err: err}
	}
	s.mu.Lock()
	s.lastFingerprint = fingerprint
	s.mu.Unlock()
	if restoreErr != nil {
		return &managerSupervisionError{phase: "restore", err: restoreErr}
	}
	return nil
}

func (s *ManagerSupervisor) restoreAgentRuntimes(ctx context.Context) error {
	if s.starter == nil {
		return nil
	}
	views, err := s.service.ListAgentConfigs(ctx)
	if err != nil {
		return err
	}
	var restoreErrors []error
	for _, view := range views {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if s.starter.IsAgentActive(view.Name) {
			now := s.currentTime()
			if last := s.lastProbes[view.Name]; !last.IsZero() && now.Sub(last) < activeAgentProbeInterval {
				delete(s.probeRetries, view.Name)
				continue
			}
			if _, probeErr := s.starter.StartAgent(ctx, view.Name); probeErr != nil {
				if errors.Is(probeErr, ErrProviderUnavailable) {
					s.recordAgentProbeFailure(view.Name, now)
					restoreErrors = append(restoreErrors, errors.Join(errAgentProviderUnavailable, probeErr))
				} else {
					restoreErrors = append(restoreErrors, fmt.Errorf("health probe %s: %w", view.Name, probeErr))
				}
				continue
			}
			s.lastProbes[view.Name] = now
			delete(s.probeRetries, view.Name)
			continue
		}
		if !view.SecretConfigured || view.ProviderType == "" || view.Model == "" {
			delete(s.probeRetries, view.Name)
			continue
		}
		if err := s.restoreAgentRuntime(ctx, view); err != nil {
			restoreErrors = append(restoreErrors, err)
		}
	}
	return errors.Join(restoreErrors...)
}

func (s *ManagerSupervisor) restoreAgentRuntime(ctx context.Context, view AgentConfigView) error {
	now := s.currentTime()
	switch view.Status {
	case "online":
		if _, err := s.starter.StartConfiguredAgent(ctx, view.Name); err != nil {
			return fmt.Errorf("restore %s heartbeat: %w", view.Name, err)
		}
		delete(s.probeRetries, view.Name)
		s.lastProbes[view.Name] = now
		return nil
	case "configured":
		delete(s.probeRetries, view.Name)
		if _, err := s.starter.StartAgent(ctx, view.Name); err != nil {
			if errors.Is(err, ErrProviderUnavailable) {
				s.recordAgentProbeFailure(view.Name, now)
				return errors.Join(errAgentProviderUnavailable, err)
			}
			return fmt.Errorf("probe %s: %w", view.Name, err)
		}
		delete(s.probeRetries, view.Name)
		s.lastProbes[view.Name] = now
		return nil
	case "blocked: provider_error":
		retry := s.probeRetries[view.Name]
		if retry.nextAttempt.IsZero() {
			s.recordAgentProbeFailure(view.Name, now)
			return errAgentProviderUnavailable
		}
		if now.Before(retry.nextAttempt) {
			return errAgentProviderUnavailable
		}
		if _, err := s.starter.StartAgent(ctx, view.Name); err != nil {
			if errors.Is(err, ErrProviderUnavailable) {
				s.recordAgentProbeFailure(view.Name, now)
				return errors.Join(errAgentProviderUnavailable, err)
			}
			delete(s.probeRetries, view.Name)
			return fmt.Errorf("probe %s: %w", view.Name, err)
		}
		delete(s.probeRetries, view.Name)
		s.lastProbes[view.Name] = now
		return nil
	default:
		delete(s.probeRetries, view.Name)
		delete(s.lastProbes, view.Name)
		return nil
	}
}

func (s *ManagerSupervisor) recordAgentProbeFailure(name string, now time.Time) {
	if s.probeRetries == nil {
		s.probeRetries = map[string]agentProbeRetry{}
	}
	retry := s.probeRetries[name]
	retry.failures++
	retry.nextAttempt = now.Add(s.agentProbeRetryDelay(retry.failures))
	s.probeRetries[name] = retry
}

func (s *ManagerSupervisor) agentProbeRetryDelay(failures int) time.Duration {
	delay := s.interval
	if delay <= 0 {
		delay = defaultManagerSupervisionInterval
	}
	for attempt := 1; attempt < failures && delay < maxAgentProbeRetryDelay; attempt++ {
		if delay >= maxAgentProbeRetryDelay/2 {
			return maxAgentProbeRetryDelay
		}
		delay *= 2
	}
	if delay > maxAgentProbeRetryDelay {
		return maxAgentProbeRetryDelay
	}
	return delay
}

func (s *ManagerSupervisor) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *ManagerSupervisor) publishFailure(ctx context.Context, scanErr error) error {
	if s.bus == nil {
		return errors.New("manager supervisor event bus is unavailable")
	}
	phase := "scan"
	var supervisionErr *managerSupervisionError
	if errors.As(scanErr, &supervisionErr) {
		phase = supervisionErr.phase
	}
	kind := managerSupervisionErrorKind(scanErr)
	sum := sha256.Sum256([]byte(phase + "\x00" + kind))
	fingerprint := hex.EncodeToString(sum[:])
	s.mu.Lock()
	if s.lastFailure == fingerprint {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	if err := s.bus.PublishJSON(ctx, nil, "manager.supervision.failed", "manager", map[string]any{
		"phase":       phase,
		"error_kind":  kind,
		"fingerprint": fingerprint,
	}); err != nil {
		return err
	}
	s.mu.Lock()
	s.lastFailure = fingerprint
	s.mu.Unlock()
	return nil
}

func managerSupervisionErrorKind(err error) string {
	switch {
	case errors.Is(err, errAgentProviderUnavailable):
		return "agent_provider_unavailable"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			return "filesystem_error"
		}
		return "operation_failed"
	}
}

func (s *ManagerSupervisor) repairLegacyGeneratedAgentTemplates(ctx context.Context) error {
	entries, err := os.ReadDir(s.service.cfg.AgentDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		name := entry.Name()
		if !entry.IsDir() || (!strings.HasPrefix(name, "auto-") && !strings.HasPrefix(name, "sub-")) || ValidateName(name) != nil {
			continue
		}
		dir, err := s.service.agentBase(name)
		if err != nil {
			return err
		}
		candidate := filepath.Join(dir, ".env.example")
		info, err := os.Lstat(candidate)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		path, err := files.UnderRoot(dir, candidate)
		if err != nil {
			return err
		}
		content, matches, err := readExactRegularFile(path, len(legacyGeneratedEnvExample))
		if err != nil {
			return fmt.Errorf("inspect legacy Agent %s template: %w", name, err)
		}
		if !matches || content != legacyGeneratedEnvExample {
			continue
		}
		if err := atomicWriteFile(path, standardGeneratedEnvExample, 0o644); err != nil {
			return fmt.Errorf("upgrade legacy Agent %s template: %w", name, err)
		}
	}
	return nil
}

func readExactRegularFile(path string, expectedSize int) (string, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != int64(expectedSize) {
		return "", false, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return "", false, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return "", false, errors.New("template changed during secure read")
	}
	content, err := io.ReadAll(io.LimitReader(file, int64(expectedSize+1)))
	if err != nil {
		return "", false, err
	}
	if len(content) != expectedSize {
		return "", false, nil
	}
	return string(content), true, nil
}

func (s *ManagerSupervisor) ensureBaselineAgents(ctx context.Context) error {
	for _, definition := range baselineAgentDefinitions {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		dir, err := s.service.agentBase(definition.name)
		if err != nil {
			return err
		}
		for _, relative := range []string{
			".",
			"skills",
			"mcps",
			"memory",
			"memory/summaries",
			"memory/decisions",
			"memory/task-notes",
			"logs",
			"logs/runtime",
			"logs/conversations",
		} {
			path, pathErr := files.UnderRoot(s.service.cfg.AgentDir, filepath.Join(dir, relative))
			if pathErr != nil {
				return pathErr
			}
			if err = ensureBaselineDirectory(path); err != nil {
				return fmt.Errorf("ensure baseline agent %s directory: %w", definition.name, err)
			}
		}
		entries := []struct {
			name    string
			content string
		}{
			{name: ".gitignore", content: "env\nlogs/runtime/*.log\n"},
			{name: ".env.example", content: "AGENT_PROVIDER_TYPE=openai\nAGENT_API_KEY=\nAGENT_BASE_URL=https://api.openai.com/v1\nAGENT_MODEL=\n"},
			{name: "agent.yaml", content: formatBaselineAgentYAML(definition)},
			{name: "system_prompt.md", content: definition.prompt},
		}
		for _, entry := range entries {
			path, pathErr := files.UnderRoot(dir, filepath.Join(dir, entry.name))
			if pathErr != nil {
				return pathErr
			}
			if err = writeBaselineFileOnce(path, entry.content); err != nil {
				return fmt.Errorf("ensure baseline agent %s file: %w", definition.name, err)
			}
		}
		if _, err = s.service.db.ExecContext(ctx, `insert into agent_registry(name,role,dir,status,last_heartbeat)
			values(?,?,?,'blocked: missing_config',datetime('now'))
			on conflict(name) do nothing`, definition.name, definition.role, dir); err != nil {
			return err
		}
	}
	return nil
}

func ensureBaselineDirectory(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("path is not a safe directory")
	}
	return nil
}

func writeBaselineFileOnce(path, content string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return statErr
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("existing path is not a safe regular file")
		}
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = file.WriteString(content); err != nil {
		file.Close()
		_ = os.Remove(path)
		return err
	}
	if err = file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func formatBaselineAgentYAML(definition baselineAgentDefinition) string {
	var content strings.Builder
	content.WriteString("role: " + definition.role + "\n")
	content.WriteString("max_concurrency: 1\n")
	content.WriteString("capabilities:\n")
	for _, capability := range definition.capabilities {
		content.WriteString("  - " + capability + "\n")
	}
	content.WriteString("project_access:\n")
	return content.String()
}

func (s *ManagerSupervisor) capture(ctx context.Context) (managerRuntimeSnapshot, error) {
	snapshot := managerRuntimeSnapshot{
		Projects: []managerProjectStatus{},
		Tasks:    []managerTaskStatus{},
		Agents:   []managerAgentStatus{},
	}
	projectRows, err := s.service.db.QueryContext(ctx, `select p.id,p.slug,p.status,count(t.id),
		coalesce(sum(case when t.id is not null and t.status not in ('completed','cancelled') then 1 else 0 end),0),
		coalesce(sum(case when t.status='blocked' or t.status like 'blocked:%' then 1 else 0 end),0)
		from projects p left join tasks t on t.project_id=p.id
		group by p.id,p.slug,p.status order by p.id`)
	if err != nil {
		return snapshot, err
	}
	for projectRows.Next() {
		var item managerProjectStatus
		if err = projectRows.Scan(&item.ID, &item.Slug, &item.Status, &item.TaskCount, &item.OpenTaskCount, &item.BlockedTasks); err != nil {
			projectRows.Close()
			return snapshot, err
		}
		snapshot.Projects = append(snapshot.Projects, item)
	}
	if err = projectRows.Err(); err != nil {
		projectRows.Close()
		return snapshot, err
	}
	projectRows.Close()

	taskRows, err := s.service.db.QueryContext(ctx, `select t.id,t.project_id,t.status,count(ts.id),
		coalesce(sum(case when ts.status in ('assigned','in_progress','checkpointed','interrupted','review') then 1 else 0 end),0),
		coalesce(sum(case when ts.status='blocked' or ts.status like 'blocked:%' then 1 else 0 end),0),
		coalesce(sum(case when ts.status='completed' then 1 else 0 end),0)
		from tasks t left join task_steps ts on ts.task_id=t.id
		group by t.id,t.project_id,t.status order by t.id`)
	if err != nil {
		return snapshot, err
	}
	for taskRows.Next() {
		var item managerTaskStatus
		if err = taskRows.Scan(&item.ID, &item.ProjectID, &item.Status, &item.StepCount, &item.ActiveSteps, &item.BlockedSteps, &item.CompletedSteps); err != nil {
			taskRows.Close()
			return snapshot, err
		}
		snapshot.Tasks = append(snapshot.Tasks, item)
	}
	if err = taskRows.Err(); err != nil {
		taskRows.Close()
		return snapshot, err
	}
	taskRows.Close()

	configured := map[string]bool{}
	if views, viewErr := s.service.ListAgentConfigs(ctx); viewErr == nil {
		for _, view := range views {
			configured[view.Name] = view.SecretConfigured
		}
	}
	agentRows, err := s.service.db.QueryContext(ctx, `select ar.name,ar.role,ar.status,coalesce(ar.current_task_id,0),
			count(distinct case when ta.status in ('assigned','running','review') then ta.id end)
		from agent_registry ar left join task_assignments ta on ta.agent_name=ar.name
		group by ar.id,ar.name,ar.role,ar.status,ar.current_task_id order by ar.name`)
	if err != nil {
		return snapshot, err
	}
	for agentRows.Next() {
		var item managerAgentStatus
		if err = agentRows.Scan(&item.Name, &item.Role, &item.Status, &item.CurrentTaskID, &item.ActiveTasks); err != nil {
			agentRows.Close()
			return snapshot, err
		}
		item.SecretConfigured = configured[item.Name]
		snapshot.Agents = append(snapshot.Agents, item)
	}
	if err = agentRows.Err(); err != nil {
		agentRows.Close()
		return snapshot, err
	}
	agentRows.Close()
	sort.Slice(snapshot.Agents, func(i, j int) bool { return snapshot.Agents[i].Name < snapshot.Agents[j].Name })
	return snapshot, nil
}

func (s *ManagerSupervisor) persistedFingerprint() string {
	base, err := s.service.agentBase("manager")
	if err != nil {
		return ""
	}
	memoryRoot := filepath.Join(base, "memory")
	path, err := files.UnderRoot(memoryRoot, filepath.Join(memoryRoot, filepath.FromSlash(managerRuntimeStatusPath)))
	if err != nil {
		return ""
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 16*1024 {
		return ""
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(content), "\n") {
		if !strings.HasPrefix(line, "fingerprint: ") {
			continue
		}
		value := strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "fingerprint: ")), "`")
		if len(value) == sha256.Size*2 {
			if _, err := hex.DecodeString(value); err == nil {
				return value
			}
		}
	}
	return ""
}

func formatManagerRuntimeStatus(snapshot managerRuntimeSnapshot, fingerprint string, checkedAt time.Time) string {
	var content strings.Builder
	content.WriteString("# 总管运行巡检\n\n")
	content.WriteString("此文件由服务端确定性巡检生成，不包含任务描述、日志、Provider 错误或密钥。\n\n")
	content.WriteString("checked_at: `" + checkedAt.Format(time.RFC3339Nano) + "`\n")
	content.WriteString("fingerprint: `" + fingerprint + "`\n\n")
	content.WriteString("## 项目\n\n| ID | 标识 | 状态 | 任务 | 未完成 | 阻塞 |\n| --- | --- | --- | ---: | ---: | ---: |\n")
	for _, item := range snapshot.Projects {
		fmt.Fprintf(&content, "| %d | %s | %s | %d | %d | %d |\n", item.ID, safeRuntimeField(item.Slug), safeRuntimeField(item.Status), item.TaskCount, item.OpenTaskCount, item.BlockedTasks)
	}
	content.WriteString("\n## 任务\n\n| ID | Project | 状态 | 步骤 | 活动 | 阻塞 | 完成 |\n| --- | ---: | --- | ---: | ---: | ---: | ---: |\n")
	for _, item := range snapshot.Tasks {
		fmt.Fprintf(&content, "| %d | %d | %s | %d | %d | %d | %d |\n", item.ID, item.ProjectID, safeRuntimeField(item.Status), item.StepCount, item.ActiveSteps, item.BlockedSteps, item.CompletedSteps)
	}
	content.WriteString("\n## Agent\n\n| 名称 | 角色 | 状态 | 已配置 | 当前任务 | 活动任务 |\n| --- | --- | --- | --- | ---: | ---: |\n")
	for _, item := range snapshot.Agents {
		configured := "否"
		if item.SecretConfigured {
			configured = "是"
		}
		fmt.Fprintf(&content, "| %s | %s | %s | %s | %d | %d |\n", safeRuntimeField(item.Name), safeRuntimeField(item.Role), safeRuntimeField(item.Status), configured, item.CurrentTaskID, item.ActiveTasks)
	}
	return content.String()
}

func safeRuntimeField(value string) string {
	value = strings.TrimSpace(strings.NewReplacer("|", "/", "\r", " ", "\n", " ").Replace(value))
	runes := []rune(value)
	if len(runes) > 120 {
		value = string(runes[:120])
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"api_key", "api-key", "authorization", "bearer ", "token=", "password", "secret", "cookie"} {
		if strings.Contains(lower, marker) {
			return "[已脱敏]"
		}
	}
	return value
}
