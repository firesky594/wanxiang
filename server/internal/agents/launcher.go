package agents

import (
	"context"
	"sync"
	"time"

	"wanxiang-agent/server/internal/events"
)

const managerHeartbeatInterval = 15 * time.Second

type Launcher struct {
	service *Service
	bus     *events.Bus

	mu     sync.Mutex
	cancel context.CancelFunc
	active map[string]string
	wg     sync.WaitGroup
}

// NewLauncher 创建并初始化 Agent 启动器。
func NewLauncher(service *Service, bus *events.Bus) *Launcher {
	return &Launcher{service: service, bus: bus, active: map[string]string{}}
}

// Start 探测 Manager 并启动持续心跳。
func (l *Launcher) Start(ctx context.Context) (ManagerStatus, error) {
	status, err := l.service.EnsureManager(ctx)
	if err != nil || status.Status == "blocked: missing_secret" {
		return status, err
	}
	view, err := l.service.ProbeAgent(ctx, "manager")
	if err != nil {
		return ManagerStatus{Status: view.Status}, err
	}
	return l.startHeartbeat(ctx, "manager", "manager", status)
}

// StartAll 启动 Manager 及全部已配置 Agent。
func (l *Launcher) StartAll(ctx context.Context) (ManagerStatus, error) {
	status, err := l.Start(ctx)
	if err != nil {
		status.Status = "blocked: provider_error"
	}
	views, listErr := l.service.ListAgentConfigs(ctx)
	if listErr != nil {
		return status, listErr
	}
	for _, view := range views {
		if view.Name == "manager" {
			continue
		}
		if probed, probeErr := l.service.ProbeAgent(ctx, view.Name); probeErr == nil && probed.Status == "online" {
			_, _ = l.startHeartbeat(ctx, view.Name, "agent", ManagerStatus{Status: "online"})
		}
	}
	return status, nil
}

// StartAgent 探测并启动指定 Agent 的心跳。
func (l *Launcher) StartAgent(ctx context.Context, name string) (AgentConfigView, error) {
	view, err := l.service.ProbeAgent(ctx, name)
	if err != nil {
		return view, err
	}
	role := "agent"
	if name == "manager" {
		role = "manager"
	}
	if _, err := l.startHeartbeat(ctx, name, role, ManagerStatus{Status: "online"}); err != nil {
		return AgentConfigView{}, err
	}
	return view, nil
}

func (l *Launcher) startHeartbeat(ctx context.Context, name, role string, status ManagerStatus) (ManagerStatus, error) {

	l.mu.Lock()
	if l.cancel != nil {
		l.active[name] = role
		l.mu.Unlock()
		return status, nil
	}
	runCtx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel
	l.active[name] = role
	l.mu.Unlock()

	if err := l.service.Heartbeat(ctx, HeartbeatInput{Name: name, Role: role, Status: "online"}); err != nil {
		l.stopStart(cancel)
		return ManagerStatus{}, err
	}
	eventType := "agent.started"
	if name == "manager" {
		eventType = "manager.started"
	}
	if err := l.bus.PublishJSON(ctx, nil, eventType, name, map[string]any{"status": "online"}); err != nil {
		l.stopStart(cancel)
		return ManagerStatus{}, err
	}

	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		ticker := time.NewTicker(managerHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				l.mu.Lock()
				active := make(map[string]string, len(l.active))
				for activeName, activeRole := range l.active {
					active[activeName] = activeRole
				}
				l.mu.Unlock()
				for activeName, activeRole := range active {
					_ = l.service.Heartbeat(runCtx, HeartbeatInput{Name: activeName, Role: activeRole, Status: "online"})
				}
			}
		}
	}()
	return status, nil
}

// Close 停止全部 Agent 心跳并等待退出。
func (l *Launcher) Close() {
	l.mu.Lock()
	cancel := l.cancel
	l.cancel = nil
	l.active = map[string]string{}
	l.mu.Unlock()
	if cancel != nil {
		cancel()
		l.wg.Wait()
	}
}

func (l *Launcher) stopStart(cancel context.CancelFunc) {
	cancel()
	l.mu.Lock()
	if l.cancel != nil {
		l.cancel = nil
	}
	l.mu.Unlock()
}
