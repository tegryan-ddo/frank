package container

import (
	"io"
	"os/exec"
	"strings"
	"time"
)

// OrbStackRuntime implements Runtime using OrbStack
// OrbStack provides Docker-compatible CLI, so we wrap DockerRuntime with OrbStack detection
type OrbStackRuntime struct {
	docker *DockerRuntime
}

// NewOrbStackRuntime creates a new OrbStack runtime
func NewOrbStackRuntime() (*OrbStackRuntime, error) {
	docker, err := NewDockerRuntime()
	if err != nil {
		return nil, err
	}
	return &OrbStackRuntime{docker: docker}, nil
}

// Name returns the runtime name
func (o *OrbStackRuntime) Name() string {
	return "orbstack"
}

// IsAvailable checks if OrbStack is available
func (o *OrbStackRuntime) IsAvailable() bool {
	// Check if orbctl exists (OrbStack CLI)
	if _, err := exec.LookPath("orbctl"); err == nil {
		cmd := exec.Command("orbctl", "status")
		if cmd.Run() == nil {
			return true
		}
	}

	// Alternative: check if docker is OrbStack's docker
	cmd := exec.Command("docker", "version", "--format", "{{.Server.Platform.Name}}")
	output, err := cmd.Output()
	if err == nil && strings.Contains(strings.ToLower(string(output)), "orbstack") {
		return true
	}

	return false
}

// CreateContainer creates a new container
func (o *OrbStackRuntime) CreateContainer(opts ContainerOptions) (string, error) {
	return o.docker.CreateContainer(opts)
}

// StartContainer starts a container
func (o *OrbStackRuntime) StartContainer(id string) error {
	return o.docker.StartContainer(id)
}

// StopContainer stops a container
func (o *OrbStackRuntime) StopContainer(id string, timeout time.Duration) error {
	return o.docker.StopContainer(id, timeout)
}

// RemoveContainer removes a container
func (o *OrbStackRuntime) RemoveContainer(id string, force bool) error {
	return o.docker.RemoveContainer(id, force)
}

// ListContainers lists containers matching the filter
func (o *OrbStackRuntime) ListContainers(filter ContainerFilter) ([]Container, error) {
	return o.docker.ListContainers(filter)
}

// GetContainer gets a specific container by ID or name
func (o *OrbStackRuntime) GetContainer(idOrName string) (*Container, error) {
	return o.docker.GetContainer(idOrName)
}

// ContainerLogs returns container logs
func (o *OrbStackRuntime) ContainerLogs(id string, opts LogOptions) (io.ReadCloser, error) {
	return o.docker.ContainerLogs(id, opts)
}

// ExecInContainer executes a command in a container
func (o *OrbStackRuntime) ExecInContainer(id string, cmd []string, opts ExecOptions) error {
	return o.docker.ExecInContainer(id, cmd, opts)
}

// CommitContainer commits container state to an image
func (o *OrbStackRuntime) CommitContainer(id string, imageName string) error {
	return o.docker.CommitContainer(id, imageName)
}

// BuildImage builds an image from a Dockerfile
func (o *OrbStackRuntime) BuildImage(tag string, opts BuildOptions) error {
	return o.docker.BuildImage(tag, opts)
}

// PullImage pulls an image from a registry
func (o *OrbStackRuntime) PullImage(imageName string) error {
	return o.docker.PullImage(imageName)
}

// ImageExists checks if an image exists locally
func (o *OrbStackRuntime) ImageExists(imageName string) (bool, error) {
	return o.docker.ImageExists(imageName)
}

// TagImage tags an image with a new name
func (o *OrbStackRuntime) TagImage(source, target string) error {
	return o.docker.TagImage(source, target)
}

// ListImages lists images matching a prefix
func (o *OrbStackRuntime) ListImages(prefix string) ([]string, error) {
	return o.docker.ListImages(prefix)
}
