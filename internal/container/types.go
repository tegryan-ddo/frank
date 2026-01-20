package container

import (
	"io"
	"time"
)

// Container represents a running or stopped container
type Container struct {
	ID      string
	Name    string
	Image   string
	Status  string
	Created time.Time
	Ports   []PortMapping
	Labels  map[string]string
}

// ContainerOptions holds options for creating a container
type ContainerOptions struct {
	Name       string
	Image      string
	Ports      []PortMapping
	Env        []string
	Volumes    []VolumeMount
	WorkDir    string
	Cmd        []string
	Entrypoint []string
	Labels     map[string]string
	AutoRemove bool
	TTY        bool
	OpenStdin  bool
}

// PortMapping represents a port mapping between host and container
type PortMapping struct {
	HostPort      int
	ContainerPort int
	Protocol      string // tcp, udp
}

// VolumeMount represents a volume mount
type VolumeMount struct {
	HostPath      string
	ContainerPath string
	ReadOnly      bool
}

// ContainerFilter holds filters for listing containers
type ContainerFilter struct {
	All        bool              // include stopped containers
	NamePrefix string            // filter by name prefix
	Labels     map[string]string // filter by labels
}

// LogOptions holds options for container logs
type LogOptions struct {
	Follow     bool
	Tail       string // "all" or number
	Timestamps bool
	Since      time.Time
	Stdout     bool
	Stderr     bool
}

// ExecOptions holds options for executing a command in a container
type ExecOptions struct {
	Interactive bool
	TTY         bool
	User        string
	WorkDir     string
	Env         []string
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
}

// BuildOptions holds options for building an image
type BuildOptions struct {
	NoCache    bool
	BuildArgs  map[string]string
	Dockerfile string
	Context    string
}

// Runtime defines the interface for container runtime operations
type Runtime interface {
	// Name returns the runtime name
	Name() string

	// IsAvailable checks if the runtime is available
	IsAvailable() bool

	// CreateContainer creates a new container
	CreateContainer(opts ContainerOptions) (string, error)

	// StartContainer starts a container
	StartContainer(id string) error

	// StopContainer stops a container
	StopContainer(id string, timeout time.Duration) error

	// RemoveContainer removes a container
	RemoveContainer(id string, force bool) error

	// ListContainers lists containers matching the filter
	ListContainers(filter ContainerFilter) ([]Container, error)

	// GetContainer gets a specific container by ID or name
	GetContainer(idOrName string) (*Container, error)

	// ContainerLogs returns container logs
	ContainerLogs(id string, opts LogOptions) (io.ReadCloser, error)

	// ExecInContainer executes a command in a container
	ExecInContainer(id string, cmd []string, opts ExecOptions) error

	// CommitContainer commits container state to an image
	CommitContainer(id string, imageName string) error

	// BuildImage builds an image from a Dockerfile
	BuildImage(tag string, opts BuildOptions) error

	// PullImage pulls an image from a registry
	PullImage(image string) error

	// ImageExists checks if an image exists locally
	ImageExists(image string) (bool, error)

	// TagImage tags an image with a new name
	TagImage(source, target string) error

	// ListImages lists images matching a prefix
	ListImages(prefix string) ([]string, error)
}
