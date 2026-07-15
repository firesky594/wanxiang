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
}

func NewWorker(service *Service, interval time.Duration) *Worker {
	if interval <= 0 {
		interval = HeartbeatInterval
	}
	return &Worker{service: service, interval: interval, firstDone: make(chan struct{})}
}

func (w *Worker) FirstScanDone() <-chan struct{} { return w.firstDone }

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
