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

func NewWorker(service *Service, interval time.Duration) *Worker {
	if interval <= 0 {
		interval = HeartbeatInterval
	}
	return &Worker{service: service, interval: interval, firstDone: make(chan struct{}), done: make(chan struct{})}
}

func (w *Worker) FirstScanDone() <-chan struct{} { return w.firstDone }

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

func (w *Worker) Close() {
	w.closeOnce.Do(func() {
		if w.cancel == nil {
			return
		}
		w.cancel()
		<-w.done
	})
}

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
