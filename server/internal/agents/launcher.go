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
	wg     sync.WaitGroup
}

func NewLauncher(service *Service, bus *events.Bus) *Launcher {
	return &Launcher{service: service, bus: bus}
}

func (l *Launcher) Start(ctx context.Context) (ManagerStatus, error) {
	status, err := l.service.EnsureManager(ctx)
	if err != nil || status.Status != "online" {
		return status, err
	}

	l.mu.Lock()
	if l.cancel != nil {
		l.mu.Unlock()
		return status, nil
	}
	runCtx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel
	l.mu.Unlock()

	if err := l.service.Heartbeat(ctx, HeartbeatInput{Name: "manager", Role: "manager", Status: "online"}); err != nil {
		l.stopStart(cancel)
		return ManagerStatus{}, err
	}
	if err := l.bus.PublishJSON(ctx, nil, "manager.started", "manager", map[string]any{"status": "online"}); err != nil {
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
				_ = l.service.Heartbeat(runCtx, HeartbeatInput{Name: "manager", Role: "manager", Status: "online"})
			}
		}
	}()
	return status, nil
}

func (l *Launcher) Close() {
	l.mu.Lock()
	cancel := l.cancel
	l.cancel = nil
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
