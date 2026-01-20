package notification

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/barff/frank/internal/config"
	"github.com/barff/frank/internal/container"
)

// Monitor monitors container output for notification triggers
type Monitor struct {
	containerID   string
	containerName string
	runtime       container.Runtime
	detector      *PatternDetector
	notifier      *BeeepNotifier
	cooldown      *CooldownManager
	cfg           config.NotificationConfig

	lastActivity time.Time
	stopChan     chan struct{}
	running      bool
	mu           sync.Mutex
}

// NewMonitor creates a new notification monitor
func NewMonitor(
	containerID string,
	containerName string,
	runtime container.Runtime,
	cfg config.NotificationConfig,
) *Monitor {
	return &Monitor{
		containerID:   containerID,
		containerName: containerName,
		runtime:       runtime,
		detector:      NewPatternDetector(cfg),
		notifier:      NewBeeepNotifier(),
		cooldown:      NewCooldownManager(cfg.Cooldown),
		cfg:           cfg,
		lastActivity:  time.Now(),
		stopChan:      make(chan struct{}),
	}
}

// Start starts the notification monitor
func (m *Monitor) Start() error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return fmt.Errorf("monitor already running")
	}
	m.running = true
	m.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start inactivity checker
	go m.checkInactivity(ctx)

	// Get container logs
	logs, err := m.runtime.ContainerLogs(m.containerID, container.LogOptions{
		Follow: true,
		Tail:   "0", // Only new logs
		Stdout: true,
		Stderr: true,
		Since:  time.Now(),
	})
	if err != nil {
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
		return fmt.Errorf("failed to attach to container logs: %w", err)
	}
	defer logs.Close()

	// Create scanner for logs
	reader := newDockerLogReader(logs)
	scanner := bufio.NewScanner(reader)

	for scanner.Scan() {
		select {
		case <-m.stopChan:
			return nil
		default:
			line := scanner.Text()
			m.processLine(line)
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		return fmt.Errorf("error reading logs: %w", err)
	}

	return nil
}

// Stop stops the notification monitor
func (m *Monitor) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		close(m.stopChan)
		m.running = false
	}
}

// IsRunning returns whether the monitor is running
func (m *Monitor) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// ToggleNotifications toggles notifications on/off
func (m *Monitor) ToggleNotifications() bool {
	return m.notifier.Toggle()
}

// processLine processes a single log line
func (m *Monitor) processLine(line string) {
	m.lastActivity = time.Now()

	if !m.cfg.Enabled {
		return
	}

	if m.detector.ShouldNotify(line) && m.cooldown.CanNotify() {
		m.sendNotification(line)
		m.cooldown.RecordNotification()
	}
}

// sendNotification sends a desktop notification
func (m *Monitor) sendNotification(line string) {
	title := fmt.Sprintf("Frank - %s", m.containerName)
	message := m.detector.ExtractMessage(line)

	if m.cfg.Sound {
		m.notifier.SendWithSound(title, message)
	} else {
		m.notifier.Send(title, message)
	}
}

// checkInactivity monitors for inactivity
func (m *Monitor) checkInactivity(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopChan:
			return
		case <-ticker.C:
			if !m.cfg.Enabled {
				continue
			}

			inactiveDuration := time.Since(m.lastActivity)
			if inactiveDuration > m.cfg.InactivityTimeout && m.cooldown.CanNotify() {
				title := fmt.Sprintf("Frank - %s", m.containerName)
				message := "Claude may be waiting for input (inactive)"

				if m.cfg.Sound {
					m.notifier.SendWithSound(title, message)
				} else {
					m.notifier.Send(title, message)
				}
				m.cooldown.RecordNotification()
			}
		}
	}
}

// dockerLogReader strips Docker log headers from the stream
type dockerLogReader struct {
	reader io.Reader
	buffer []byte
}

func newDockerLogReader(r io.Reader) *dockerLogReader {
	return &dockerLogReader{
		reader: r,
		buffer: make([]byte, 8192),
	}
}

func (d *dockerLogReader) Read(p []byte) (int, error) {
	// Read from underlying reader
	n, err := d.reader.Read(d.buffer)
	if err != nil {
		return 0, err
	}

	// Docker log format: 8-byte header followed by payload
	// Header format:
	//   - byte 0: stream type (1=stdout, 2=stderr)
	//   - bytes 1-3: padding
	//   - bytes 4-7: payload size (big endian)

	data := d.buffer[:n]
	offset := 0
	written := 0

	for offset < len(data) {
		// Check if we have a complete header
		if len(data)-offset < 8 {
			// Not enough data for header, copy remaining
			copy(p[written:], data[offset:])
			written += len(data) - offset
			break
		}

		// Parse header
		payloadSize := int(data[offset+4])<<24 | int(data[offset+5])<<16 | int(data[offset+6])<<8 | int(data[offset+7])

		// Skip header
		offset += 8

		// Copy payload
		if offset+payloadSize <= len(data) {
			copy(p[written:], data[offset:offset+payloadSize])
			written += payloadSize
			offset += payloadSize
		} else {
			// Partial payload, copy what we have
			copy(p[written:], data[offset:])
			written += len(data) - offset
			break
		}
	}

	return written, nil
}
