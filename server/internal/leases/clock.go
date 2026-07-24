package leases

import (
	"sync"
	"time"
)

type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

// Now 返回当前 UTC 时间。
func (SystemClock) Now() time.Time { return time.Now().UTC() }

type FakeClock struct {
	mu  sync.RWMutex
	now time.Time
}

// NewFakeClock 创建用于回归测试的可控时钟。
func NewFakeClock(now time.Time) *FakeClock {
	return &FakeClock{now: now.UTC()}
}

// Now 返回可控测试时钟的当前时间。
func (c *FakeClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

// Advance 推进可控测试时钟的时间。
func (c *FakeClock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(duration)
}
