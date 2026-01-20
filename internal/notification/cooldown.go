package notification

import (
	"sync"
	"time"
)

// CooldownManager manages notification cooldown to prevent spam
type CooldownManager struct {
	duration         time.Duration
	lastNotification time.Time
	mu               sync.Mutex
}

// NewCooldownManager creates a new cooldown manager
func NewCooldownManager(duration time.Duration) *CooldownManager {
	return &CooldownManager{
		duration: duration,
	}
}

// CanNotify checks if a notification can be sent
func (c *CooldownManager) CanNotify() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.lastNotification.IsZero() {
		return true
	}

	return time.Since(c.lastNotification) >= c.duration
}

// RecordNotification records that a notification was sent
func (c *CooldownManager) RecordNotification() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastNotification = time.Now()
}

// Reset resets the cooldown
func (c *CooldownManager) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastNotification = time.Time{}
}

// TimeSinceLastNotification returns the time since the last notification
func (c *CooldownManager) TimeSinceLastNotification() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.lastNotification.IsZero() {
		return time.Duration(0)
	}

	return time.Since(c.lastNotification)
}
