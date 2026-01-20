package notification

import (
	"sync"

	"github.com/gen2brain/beeep"
)

// Notifier sends desktop notifications
type Notifier interface {
	Send(title, message string) error
	SendWithSound(title, message string) error
	SetEnabled(enabled bool)
	IsEnabled() bool
}

// BeeepNotifier implements Notifier using the beeep library
type BeeepNotifier struct {
	enabled bool
	appIcon string
	mu      sync.RWMutex
}

// NewBeeepNotifier creates a new beeep-based notifier
func NewBeeepNotifier() *BeeepNotifier {
	return &BeeepNotifier{
		enabled: true,
		appIcon: "", // Can be set to path of icon file
	}
}

// Send sends a notification without sound
func (n *BeeepNotifier) Send(title, message string) error {
	n.mu.RLock()
	enabled := n.enabled
	n.mu.RUnlock()

	if !enabled {
		return nil
	}

	return beeep.Notify(title, message, n.appIcon)
}

// SendWithSound sends a notification with sound
func (n *BeeepNotifier) SendWithSound(title, message string) error {
	n.mu.RLock()
	enabled := n.enabled
	n.mu.RUnlock()

	if !enabled {
		return nil
	}

	return beeep.Alert(title, message, n.appIcon)
}

// SetEnabled enables or disables notifications
func (n *BeeepNotifier) SetEnabled(enabled bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.enabled = enabled
}

// IsEnabled returns whether notifications are enabled
func (n *BeeepNotifier) IsEnabled() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.enabled
}

// SetIcon sets the notification icon
func (n *BeeepNotifier) SetIcon(iconPath string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.appIcon = iconPath
}

// Toggle toggles notifications on/off
func (n *BeeepNotifier) Toggle() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.enabled = !n.enabled
	return n.enabled
}
