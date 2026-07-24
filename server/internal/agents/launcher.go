package agents

import (
	"context"
	"errors"
	"sync"
	"time"

	"wanxiang-agent/server/internal/events"
)

const managerHeartbeatInterval = 15 * time.Second

// ErrLauncherClosed 表示 Agent 启动器已关闭，需由 StartAll 显式重新初始化。
var ErrLauncherClosed = errors.New("agent launcher is closed")

type Launcher struct {
	service    *Service
	bus        *events.Bus
	supervisor *ManagerSupervisor

	lifecycleMu sync.Mutex
	startMu     sync.Mutex
	mu          sync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}
	active      map[string]string
	closed      bool
	generation  uint64
}

// NewLauncher 创建并初始化 Agent 启动器。
func NewLauncher(service *Service, bus *events.Bus) *Launcher {
	launcher := &Launcher{service: service, bus: bus, active: map[string]string{}}
	launcher.supervisor = NewManagerSupervisor(service, bus, launcher, defaultManagerSupervisionInterval)
	return launcher
}

// Start 探测 Manager 并启动持续心跳。
func (l *Launcher) Start(ctx context.Context) (ManagerStatus, error) {
	generation, err := l.openGeneration()
	if err != nil {
		return ManagerStatus{}, err
	}
	status, err := l.service.EnsureManager(ctx)
	if err != nil || status.Status == "blocked: missing_secret" {
		return status, err
	}
	view, err := l.service.ProbeAgent(ctx, "manager")
	if err != nil {
		l.deactivate("manager")
		return ManagerStatus{Status: view.Status}, err
	}
	status.Status = view.Status
	return l.startHeartbeatForGeneration(ctx, "manager", "manager", status, generation)
}

// StartAll 启动 Manager 及全部已配置 Agent。
func (l *Launcher) StartAll(ctx context.Context) (ManagerStatus, error) {
	l.lifecycleMu.Lock()
	defer l.lifecycleMu.Unlock()

	l.mu.Lock()
	l.closed = false
	l.generation++
	l.mu.Unlock()

	status, startErr := l.Start(ctx)
	if startErr != nil {
		if !errors.Is(startErr, ErrProviderUnavailable) {
			l.stopRuntime()
			return status, startErr
		}
		status.Status = "blocked: provider_error"
	}
	views, listErr := l.service.ListAgentConfigs(ctx)
	if listErr != nil {
		l.stopRuntime()
		return status, listErr
	}
	for _, view := range views {
		if view.Name == "manager" {
			continue
		}
		if !view.SecretConfigured || view.ProviderType == "" || view.Model == "" {
			continue
		}
		var agentErr error
		if view.Status == "online" {
			_, agentErr = l.StartConfiguredAgent(ctx, view.Name)
		} else {
			_, agentErr = l.StartAgent(ctx, view.Name)
		}
		if agentErr == nil || errors.Is(agentErr, ErrProviderUnavailable) {
			continue
		}
		l.stopRuntime()
		return status, agentErr
	}
	l.supervisor.Start()
	return status, nil
}

// StartAgent 探测并启动指定 Agent 的心跳。
func (l *Launcher) StartAgent(ctx context.Context, name string) (AgentConfigView, error) {
	generation, err := l.openGeneration()
	if err != nil {
		return AgentConfigView{}, err
	}
	view, err := l.service.ProbeAgent(ctx, name)
	if err != nil {
		l.deactivate(name)
		return view, err
	}
	dir, _ := l.service.agentBase(name)
	role := l.service.agentRole(ctx, name, dir)
	if _, err := l.startHeartbeatForGeneration(ctx, name, role, ManagerStatus{Status: "online"}, generation); err != nil {
		return AgentConfigView{}, err
	}
	return view, nil
}

// StartConfiguredAgent 在不调用模型的前提下，将已探测在线的 Agent 恢复到心跳集合。
func (l *Launcher) StartConfiguredAgent(ctx context.Context, name string) (AgentConfigView, error) {
	generation, err := l.openGeneration()
	if err != nil {
		return AgentConfigView{}, err
	}
	view, err := l.service.GetAgentConfig(ctx, name)
	if err != nil {
		l.deactivate(name)
		return view, err
	}
	if view.Status != "online" {
		return view, errors.New("agent must be probed online before heartbeat recovery")
	}
	dir, _ := l.service.agentBase(name)
	role := l.service.agentRole(ctx, name, dir)
	if _, err := l.startHeartbeatForGeneration(ctx, name, role, ManagerStatus{Status: "online"}, generation); err != nil {
		return AgentConfigView{}, err
	}
	view.Status = "online"
	return view, nil
}

// IsAgentActive 判断指定 Agent 是否已加入当前 Launcher 心跳集合。
func (l *Launcher) IsAgentActive(name string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.active[name]
	return ok
}

func (l *Launcher) startHeartbeat(ctx context.Context, name, role string, status ManagerStatus) (ManagerStatus, error) {
	generation, err := l.openGeneration()
	if err != nil {
		return ManagerStatus{}, err
	}
	return l.startHeartbeatForGeneration(ctx, name, role, status, generation)
}

func (l *Launcher) startHeartbeatForGeneration(ctx context.Context, name, role string, status ManagerStatus, generation uint64) (ManagerStatus, error) {
	l.startMu.Lock()
	defer l.startMu.Unlock()

	l.mu.Lock()
	if l.closed || l.generation != generation {
		l.mu.Unlock()
		return ManagerStatus{}, ErrLauncherClosed
	}
	if _, existed := l.active[name]; existed {
		l.mu.Unlock()
		return status, nil
	}
	l.mu.Unlock()

	if err := l.service.keepAlive(ctx, name, role, "online"); err != nil {
		return ManagerStatus{}, err
	}
	if err := l.publishStarted(ctx, name); err != nil {
		return ManagerStatus{}, err
	}

	l.mu.Lock()
	if l.cancel == nil {
		runCtx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		l.cancel = cancel
		l.done = done
		go l.runHeartbeats(runCtx, done)
	}
	l.active[name] = role
	l.mu.Unlock()
	return status, nil
}

func (l *Launcher) runHeartbeats(runCtx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(managerHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-runCtx.Done():
			return
		case <-ticker.C:
			l.keepActiveAgentsAlive(runCtx)
		}
	}
}

func (l *Launcher) keepActiveAgentsAlive(ctx context.Context) {
	l.mu.Lock()
	active := make(map[string]string, len(l.active))
	for activeName, activeRole := range l.active {
		active[activeName] = activeRole
	}
	l.mu.Unlock()
	for activeName, activeRole := range active {
		if err := l.service.keepAlive(ctx, activeName, activeRole, "online"); err != nil {
			l.deactivate(activeName)
		}
	}
}

// Close 停止全部 Agent 心跳并等待退出。
func (l *Launcher) Close() {
	l.lifecycleMu.Lock()
	defer l.lifecycleMu.Unlock()
	l.stopRuntime()
}

func (l *Launcher) stopRuntime() {
	if l.supervisor != nil {
		l.supervisor.Close()
	}
	l.startMu.Lock()
	defer l.startMu.Unlock()

	l.mu.Lock()
	cancel := l.cancel
	done := l.done
	l.cancel = nil
	l.done = nil
	l.active = map[string]string{}
	l.closed = true
	l.generation++
	l.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

func (l *Launcher) openGeneration() (uint64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return 0, ErrLauncherClosed
	}
	return l.generation, nil
}

func (l *Launcher) publishStarted(ctx context.Context, name string) error {
	eventType := "agent.started"
	if name == "manager" {
		eventType = "manager.started"
	}
	return l.bus.PublishJSON(ctx, nil, eventType, name, map[string]any{"status": "online"})
}

func (l *Launcher) deactivate(name string) {
	l.mu.Lock()
	delete(l.active, name)
	l.mu.Unlock()
}
