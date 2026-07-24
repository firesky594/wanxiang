package leases

import (
	"context"
	"sync"
	"time"
)

type Worker struct {
	service   *Service
	interval  time.Duration
	firstDone chan struct{}
	once      sync.Once
	startOnce sync.Once
	closeOnce sync.Once
	cancel    context.CancelFunc
	done      chan struct{}
}

// NewWorker 创建过期租约恢复轮询器。
func NewWorker(service *Service, interval time.Duration) *Worker {
	if interval <= 0 {
		interval = HeartbeatInterval
	}
	return &Worker{service: service, interval: interval, firstDone: make(chan struct{}), done: make(chan struct{})}
}

// FirstScanDone 返回租约轮询器首次扫描完成信号。
func (w *Worker) FirstScanDone() <-chan struct{} { return w.firstDone }

// Start 启动过期租约恢复轮询。
func (w *Worker) Start() {
	w.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		w.cancel = cancel
		go func() {
			defer close(w.done)
			w.Run(ctx)
		}()
	})
}

// Close 停止租约轮询并等待退出。
func (w *Worker) Close() {
	w.closeOnce.Do(func() {
		if w.cancel == nil {
			return
		}
		w.cancel()
		<-w.done
	})
}

// Run 持续扫描并中断过期租约。
func (w *Worker) Run(ctx context.Context) {
	_, _ = w.service.InterruptExpired(ctx)
	w.once.Do(func() { close(w.firstDone) })
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = w.service.InterruptExpired(ctx)
		}
	}
}
