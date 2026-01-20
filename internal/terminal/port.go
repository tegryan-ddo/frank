package terminal

import (
	"fmt"
	"net"
	"sync"
)

// PortAllocator manages port allocation for containers
type PortAllocator struct {
	basePort int
	maxPort  int
	mu       sync.Mutex
	used     map[int]string // port -> container name
}

// NewPortAllocator creates a new port allocator
func NewPortAllocator(basePort, maxPort int) *PortAllocator {
	return &PortAllocator{
		basePort: basePort,
		maxPort:  maxPort,
		used:     make(map[int]string),
	}
}

// Allocate allocates a port for a container
func (p *PortAllocator) Allocate(containerName string) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Check if container already has a port
	for port, name := range p.used {
		if name == containerName {
			return port, nil
		}
	}

	// Find next available port range (we need 4 consecutive ports: web, claude, bash, status)
	for port := p.basePort; port <= p.maxPort-3; port += 4 {
		// Check if any of the 4 ports are already allocated
		if _, exists := p.used[port]; exists {
			continue
		}
		if _, exists := p.used[port+1]; exists {
			continue
		}
		if _, exists := p.used[port+2]; exists {
			continue
		}
		if _, exists := p.used[port+3]; exists {
			continue
		}

		// Check if all 4 ports are available on the system
		if !isPortAvailable(port) || !isPortAvailable(port+1) || !isPortAvailable(port+2) || !isPortAvailable(port+3) {
			continue
		}

		// Mark all 4 ports as used
		p.used[port] = containerName
		p.used[port+1] = containerName
		p.used[port+2] = containerName
		p.used[port+3] = containerName
		return port, nil
	}

	return 0, fmt.Errorf("no available ports in range %d-%d", p.basePort, p.maxPort)
}

// Release releases a port allocated for a container
func (p *PortAllocator) Release(containerName string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for port, name := range p.used {
		if name == containerName {
			delete(p.used, port)
			return
		}
	}
}

// ReleasePort releases a specific port
func (p *PortAllocator) ReleasePort(port int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.used, port)
}

// GetPort returns the port allocated for a container
func (p *PortAllocator) GetPort(containerName string) (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for port, name := range p.used {
		if name == containerName {
			return port, true
		}
	}
	return 0, false
}

// MarkUsed marks a port as used by a container
func (p *PortAllocator) MarkUsed(port int, containerName string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.used[port] = containerName
}

// GetUsedPorts returns all used ports
func (p *PortAllocator) GetUsedPorts() map[int]string {
	p.mu.Lock()
	defer p.mu.Unlock()

	result := make(map[int]string)
	for port, name := range p.used {
		result[port] = name
	}
	return result
}

// isPortAvailable checks if a port is available
func isPortAvailable(port int) bool {
	addr := fmt.Sprintf(":%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	listener.Close()
	return true
}

// FindAvailablePort finds an available port starting from basePort
func FindAvailablePort(basePort, maxPort int) (int, error) {
	for port := basePort; port <= maxPort; port++ {
		if isPortAvailable(port) {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available ports in range %d-%d", basePort, maxPort)
}
