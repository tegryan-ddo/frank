package container

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// PodmanRuntime implements Runtime using Podman CLI
type PodmanRuntime struct{}

// NewPodmanRuntime creates a new Podman runtime
func NewPodmanRuntime() (*PodmanRuntime, error) {
	return &PodmanRuntime{}, nil
}

// Name returns the runtime name
func (p *PodmanRuntime) Name() string {
	return "podman"
}

// IsAvailable checks if Podman is available
func (p *PodmanRuntime) IsAvailable() bool {
	cmd := exec.Command("podman", "version")
	return cmd.Run() == nil
}

// CreateContainer creates a new container
func (p *PodmanRuntime) CreateContainer(opts ContainerOptions) (string, error) {
	args := []string{"create", "--name", opts.Name}

	// Add port mappings
	for _, port := range opts.Ports {
		protocol := port.Protocol
		if protocol == "" {
			protocol = "tcp"
		}
		args = append(args, "-p", fmt.Sprintf("%d:%d/%s", port.HostPort, port.ContainerPort, protocol))
	}

	// Add environment variables
	for _, env := range opts.Env {
		args = append(args, "-e", env)
	}

	// Add volume mounts
	for _, vol := range opts.Volumes {
		mountOpt := fmt.Sprintf("%s:%s", vol.HostPath, vol.ContainerPath)
		if vol.ReadOnly {
			mountOpt += ":ro"
		}
		args = append(args, "-v", mountOpt)
	}

	// Add working directory
	if opts.WorkDir != "" {
		args = append(args, "-w", opts.WorkDir)
	}

	// Add labels
	for k, v := range opts.Labels {
		args = append(args, "--label", fmt.Sprintf("%s=%s", k, v))
	}

	// Add TTY and stdin options
	if opts.TTY {
		args = append(args, "-t")
	}
	if opts.OpenStdin {
		args = append(args, "-i")
	}

	// Add entrypoint if specified
	if len(opts.Entrypoint) > 0 {
		args = append(args, "--entrypoint", strings.Join(opts.Entrypoint, " "))
	}

	// Add image
	args = append(args, opts.Image)

	// Add command
	args = append(args, opts.Cmd...)

	cmd := exec.Command("podman", args...)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}

// StartContainer starts a container
func (p *PodmanRuntime) StartContainer(id string) error {
	cmd := exec.Command("podman", "start", id)
	return cmd.Run()
}

// StopContainer stops a container
func (p *PodmanRuntime) StopContainer(id string, timeout time.Duration) error {
	cmd := exec.Command("podman", "stop", "-t", fmt.Sprintf("%d", int(timeout.Seconds())), id)
	return cmd.Run()
}

// RemoveContainer removes a container
func (p *PodmanRuntime) RemoveContainer(id string, force bool) error {
	args := []string{"rm"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, id)
	cmd := exec.Command("podman", args...)
	return cmd.Run()
}

// ListContainers lists containers matching the filter
func (p *PodmanRuntime) ListContainers(filter ContainerFilter) ([]Container, error) {
	args := []string{"ps", "--format", "json"}
	if filter.All {
		args = append(args, "-a")
	}
	if filter.NamePrefix != "" {
		args = append(args, "--filter", fmt.Sprintf("name=%s", filter.NamePrefix))
	}
	for k, v := range filter.Labels {
		args = append(args, "--filter", fmt.Sprintf("label=%s=%s", k, v))
	}

	cmd := exec.Command("podman", args...)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	var podmanContainers []struct {
		ID      string            `json:"Id"`
		Names   []string          `json:"Names"`
		Image   string            `json:"Image"`
		Status  string            `json:"Status"`
		Created interface{}       `json:"Created"` // Can be string (Docker) or number (Podman)
		Ports   []interface{}     `json:"Ports"`
		Labels  map[string]string `json:"Labels"`
	}

	if err := json.Unmarshal(output, &podmanContainers); err != nil {
		return nil, fmt.Errorf("failed to parse container list: %w", err)
	}

	var result []Container
	for _, c := range podmanContainers {
		name := ""
		if len(c.Names) > 0 {
			name = c.Names[0]
		}

		// Handle Created field - can be string (RFC3339) or number (Unix timestamp)
		var created time.Time
		switch v := c.Created.(type) {
		case string:
			created, _ = time.Parse(time.RFC3339, v)
		case float64:
			created = time.Unix(int64(v), 0)
		}

		// Parse ports - Podman uses host_port, container_port, and range
		var ports []PortMapping
		for _, portData := range c.Ports {
			if portMap, ok := portData.(map[string]interface{}); ok {
				hostPort := 0
				containerPort := 0
				portRange := 1

				if hp, ok := portMap["host_port"].(float64); ok {
					hostPort = int(hp)
				}
				if cp, ok := portMap["container_port"].(float64); ok {
					containerPort = int(cp)
				}
				if r, ok := portMap["range"].(float64); ok {
					portRange = int(r)
				}

				// Add all ports in the range
				for i := 0; i < portRange; i++ {
					if hostPort > 0 {
						ports = append(ports, PortMapping{
							HostPort:      hostPort + i,
							ContainerPort: containerPort + i,
						})
					}
				}
			}
		}

		result = append(result, Container{
			ID:      c.ID[:12],
			Name:    name,
			Image:   c.Image,
			Status:  c.Status,
			Created: created,
			Ports:   ports,
			Labels:  c.Labels,
		})
	}

	return result, nil
}

// GetContainer gets a specific container by ID or name
func (p *PodmanRuntime) GetContainer(idOrName string) (*Container, error) {
	cmd := exec.Command("podman", "inspect", "--format", "json", idOrName)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container: %w", err)
	}

	var containers []struct {
		ID     string `json:"Id"`
		Name   string `json:"Name"`
		Config struct {
			Image  string            `json:"Image"`
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		State struct {
			Status string `json:"Status"`
		} `json:"State"`
		Created interface{} `json:"Created"` // Can be string or number
	}

	if err := json.Unmarshal(output, &containers); err != nil {
		return nil, fmt.Errorf("failed to parse container info: %w", err)
	}

	if len(containers) == 0 {
		return nil, fmt.Errorf("container not found: %s", idOrName)
	}

	c := containers[0]

	// Handle Created field - can be string (RFC3339) or number (Unix timestamp)
	var created time.Time
	switch v := c.Created.(type) {
	case string:
		created, _ = time.Parse(time.RFC3339, v)
	case float64:
		created = time.Unix(int64(v), 0)
	}

	return &Container{
		ID:      c.ID[:12],
		Name:    strings.TrimPrefix(c.Name, "/"),
		Image:   c.Config.Image,
		Status:  c.State.Status,
		Created: created,
		Labels:  c.Config.Labels,
	}, nil
}

// ContainerLogs returns container logs
func (p *PodmanRuntime) ContainerLogs(id string, opts LogOptions) (io.ReadCloser, error) {
	args := []string{"logs"}
	if opts.Follow {
		args = append(args, "-f")
	}
	if opts.Tail != "" {
		args = append(args, "--tail", opts.Tail)
	}
	if opts.Timestamps {
		args = append(args, "-t")
	}
	if !opts.Since.IsZero() {
		args = append(args, "--since", opts.Since.Format(time.RFC3339))
	}
	args = append(args, id)

	cmd := exec.Command("podman", args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// Combine stdout and stderr
	reader := io.MultiReader(stdout, stderr)

	return &cmdReadCloser{
		Reader: reader,
		cmd:    cmd,
	}, nil
}

// ExecInContainer executes a command in a container
func (p *PodmanRuntime) ExecInContainer(id string, cmdArgs []string, opts ExecOptions) error {
	args := []string{"exec"}
	if opts.Interactive {
		args = append(args, "-i")
	}
	if opts.TTY {
		args = append(args, "-t")
	}
	if opts.User != "" {
		args = append(args, "-u", opts.User)
	}
	if opts.WorkDir != "" {
		args = append(args, "-w", opts.WorkDir)
	}
	for _, env := range opts.Env {
		args = append(args, "-e", env)
	}
	args = append(args, id)
	args = append(args, cmdArgs...)

	cmd := exec.Command("podman", args...)
	cmd.Stdin = opts.Stdin
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr

	return cmd.Run()
}

// CommitContainer commits container state to an image
func (p *PodmanRuntime) CommitContainer(id string, imageName string) error {
	cmd := exec.Command("podman", "commit", id, imageName)
	return cmd.Run()
}

// BuildImage builds an image from a Dockerfile
func (p *PodmanRuntime) BuildImage(tag string, opts BuildOptions) error {
	args := []string{"build", "-t", tag}
	if opts.NoCache {
		args = append(args, "--no-cache")
	}
	if opts.Dockerfile != "" {
		args = append(args, "-f", opts.Dockerfile)
	}
	for k, v := range opts.BuildArgs {
		args = append(args, "--build-arg", fmt.Sprintf("%s=%s", k, v))
	}
	if opts.Context != "" {
		args = append(args, opts.Context)
	} else {
		args = append(args, ".")
	}

	cmd := exec.Command("podman", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// PullImage pulls an image from a registry
func (p *PodmanRuntime) PullImage(imageName string) error {
	cmd := exec.Command("podman", "pull", imageName)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ImageExists checks if an image exists locally
func (p *PodmanRuntime) ImageExists(imageName string) (bool, error) {
	cmd := exec.Command("podman", "image", "exists", imageName)
	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() == 1 {
				return false, nil
			}
		}
		return false, err
	}
	return true, nil
}

// TagImage tags an image with a new name
func (p *PodmanRuntime) TagImage(source, target string) error {
	cmd := exec.Command("podman", "tag", source, target)
	return cmd.Run()
}

// ListImages lists images matching a prefix
func (p *PodmanRuntime) ListImages(prefix string) ([]string, error) {
	cmd := exec.Command("podman", "images", "--format", "{{.Repository}}:{{.Tag}}")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list images: %w", err)
	}

	var result []string
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			result = append(result, line)
		}
	}

	return result, nil
}

// cmdReadCloser wraps a command's output as an io.ReadCloser
type cmdReadCloser struct {
	io.Reader
	cmd *exec.Cmd
}

func (c *cmdReadCloser) Close() error {
	return c.cmd.Wait()
}

// logReader combines stdout and stderr into a single reader
type logReader struct {
	scanner *bufio.Scanner
}

func (l *logReader) Read(p []byte) (n int, err error) {
	if l.scanner.Scan() {
		line := l.scanner.Bytes()
		copy(p, line)
		p[len(line)] = '\n'
		return len(line) + 1, nil
	}
	return 0, io.EOF
}
