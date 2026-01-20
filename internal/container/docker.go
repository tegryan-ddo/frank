package container

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	containerTypes "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

// DockerRuntime implements Runtime using Docker SDK
type DockerRuntime struct {
	client *client.Client
}

// NewDockerRuntime creates a new Docker runtime
func NewDockerRuntime() (*DockerRuntime, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}
	return &DockerRuntime{client: cli}, nil
}

// Name returns the runtime name
func (d *DockerRuntime) Name() string {
	return "docker"
}

// IsAvailable checks if Docker is available
func (d *DockerRuntime) IsAvailable() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := d.client.Ping(ctx)
	return err == nil
}

// CreateContainer creates a new container
func (d *DockerRuntime) CreateContainer(opts ContainerOptions) (string, error) {
	ctx := context.Background()

	// Build port bindings
	exposedPorts := nat.PortSet{}
	portBindings := nat.PortMap{}

	for _, p := range opts.Ports {
		protocol := p.Protocol
		if protocol == "" {
			protocol = "tcp"
		}
		containerPort := nat.Port(fmt.Sprintf("%d/%s", p.ContainerPort, protocol))
		exposedPorts[containerPort] = struct{}{}
		portBindings[containerPort] = []nat.PortBinding{
			{
				HostIP:   "0.0.0.0",
				HostPort: fmt.Sprintf("%d", p.HostPort),
			},
		}
	}

	// Build mounts
	var mounts []mount.Mount
	for _, v := range opts.Volumes {
		mounts = append(mounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   v.HostPath,
			Target:   v.ContainerPath,
			ReadOnly: v.ReadOnly,
		})
	}

	// Container config
	containerConfig := &containerTypes.Config{
		Image:        opts.Image,
		Env:          opts.Env,
		WorkingDir:   opts.WorkDir,
		Cmd:          opts.Cmd,
		Entrypoint:   opts.Entrypoint,
		Labels:       opts.Labels,
		ExposedPorts: exposedPorts,
		Tty:          opts.TTY,
		OpenStdin:    opts.OpenStdin,
	}

	// Host config
	hostConfig := &containerTypes.HostConfig{
		PortBindings: portBindings,
		Mounts:       mounts,
		AutoRemove:   opts.AutoRemove,
	}

	resp, err := d.client.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, opts.Name)
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w", err)
	}

	return resp.ID, nil
}

// StartContainer starts a container
func (d *DockerRuntime) StartContainer(id string) error {
	ctx := context.Background()
	return d.client.ContainerStart(ctx, id, containerTypes.StartOptions{})
}

// StopContainer stops a container
func (d *DockerRuntime) StopContainer(id string, timeout time.Duration) error {
	ctx := context.Background()
	timeoutSeconds := int(timeout.Seconds())
	return d.client.ContainerStop(ctx, id, containerTypes.StopOptions{Timeout: &timeoutSeconds})
}

// RemoveContainer removes a container
func (d *DockerRuntime) RemoveContainer(id string, force bool) error {
	ctx := context.Background()
	return d.client.ContainerRemove(ctx, id, containerTypes.RemoveOptions{Force: force})
}

// ListContainers lists containers matching the filter
func (d *DockerRuntime) ListContainers(filter ContainerFilter) ([]Container, error) {
	ctx := context.Background()

	args := filters.NewArgs()
	if filter.NamePrefix != "" {
		args.Add("name", filter.NamePrefix)
	}
	for k, v := range filter.Labels {
		args.Add("label", fmt.Sprintf("%s=%s", k, v))
	}

	containers, err := d.client.ContainerList(ctx, containerTypes.ListOptions{
		All:     filter.All,
		Filters: args,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	var result []Container
	for _, c := range containers {
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}

		var ports []PortMapping
		for _, p := range c.Ports {
			ports = append(ports, PortMapping{
				HostPort:      int(p.PublicPort),
				ContainerPort: int(p.PrivatePort),
				Protocol:      p.Type,
			})
		}

		result = append(result, Container{
			ID:      c.ID[:12],
			Name:    name,
			Image:   c.Image,
			Status:  c.Status,
			Created: time.Unix(c.Created, 0),
			Ports:   ports,
			Labels:  c.Labels,
		})
	}

	return result, nil
}

// GetContainer gets a specific container by ID or name
func (d *DockerRuntime) GetContainer(idOrName string) (*Container, error) {
	ctx := context.Background()

	json, err := d.client.ContainerInspect(ctx, idOrName)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container: %w", err)
	}

	name := strings.TrimPrefix(json.Name, "/")

	var ports []PortMapping
	for containerPort, bindings := range json.NetworkSettings.Ports {
		for _, binding := range bindings {
			var hostPort int
			fmt.Sscanf(binding.HostPort, "%d", &hostPort)
			ports = append(ports, PortMapping{
				HostPort:      hostPort,
				ContainerPort: containerPort.Int(),
				Protocol:      containerPort.Proto(),
			})
		}
	}

	created, _ := time.Parse(time.RFC3339, json.Created)

	return &Container{
		ID:      json.ID[:12],
		Name:    name,
		Image:   json.Config.Image,
		Status:  json.State.Status,
		Created: created,
		Ports:   ports,
		Labels:  json.Config.Labels,
	}, nil
}

// ContainerLogs returns container logs
func (d *DockerRuntime) ContainerLogs(id string, opts LogOptions) (io.ReadCloser, error) {
	ctx := context.Background()

	options := containerTypes.LogsOptions{
		ShowStdout: opts.Stdout,
		ShowStderr: opts.Stderr,
		Follow:     opts.Follow,
		Timestamps: opts.Timestamps,
		Tail:       opts.Tail,
	}

	if !opts.Since.IsZero() {
		options.Since = opts.Since.Format(time.RFC3339)
	}

	return d.client.ContainerLogs(ctx, id, options)
}

// ExecInContainer executes a command in a container
func (d *DockerRuntime) ExecInContainer(id string, cmd []string, opts ExecOptions) error {
	ctx := context.Background()

	execConfig := types.ExecConfig{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
		AttachStdin:  opts.Interactive,
		Tty:          opts.TTY,
		User:         opts.User,
		WorkingDir:   opts.WorkDir,
		Env:          opts.Env,
	}

	resp, err := d.client.ContainerExecCreate(ctx, id, execConfig)
	if err != nil {
		return fmt.Errorf("failed to create exec: %w", err)
	}

	attachResp, err := d.client.ContainerExecAttach(ctx, resp.ID, types.ExecStartCheck{
		Tty: opts.TTY,
	})
	if err != nil {
		return fmt.Errorf("failed to attach to exec: %w", err)
	}
	defer attachResp.Close()

	// Handle I/O
	if opts.TTY {
		if opts.Stdin != nil {
			go io.Copy(attachResp.Conn, opts.Stdin)
		}
		if opts.Stdout != nil {
			io.Copy(opts.Stdout, attachResp.Reader)
		}
	} else {
		if opts.Stdin != nil {
			go io.Copy(attachResp.Conn, opts.Stdin)
		}
		// For non-TTY, stdout and stderr are multiplexed
		if opts.Stdout != nil {
			io.Copy(opts.Stdout, attachResp.Reader)
		}
	}

	return nil
}

// CommitContainer commits container state to an image
func (d *DockerRuntime) CommitContainer(id string, imageName string) error {
	ctx := context.Background()

	_, err := d.client.ContainerCommit(ctx, id, containerTypes.CommitOptions{
		Reference: imageName,
	})
	if err != nil {
		return fmt.Errorf("failed to commit container: %w", err)
	}

	return nil
}

// BuildImage builds an image from a Dockerfile
func (d *DockerRuntime) BuildImage(tag string, opts BuildOptions) error {
	ctx := context.Background()

	// Create tar archive of build context
	buildContext, err := createBuildContext(opts.Context, opts.Dockerfile)
	if err != nil {
		return fmt.Errorf("failed to create build context: %w", err)
	}

	buildOptions := types.ImageBuildOptions{
		Tags:       []string{tag},
		Dockerfile: filepath.Base(opts.Dockerfile),
		NoCache:    opts.NoCache,
		Remove:     true,
		BuildArgs:  make(map[string]*string),
	}

	for k, v := range opts.BuildArgs {
		val := v
		buildOptions.BuildArgs[k] = &val
	}

	resp, err := d.client.ImageBuild(ctx, buildContext, buildOptions)
	if err != nil {
		return fmt.Errorf("failed to build image: %w", err)
	}
	defer resp.Body.Close()

	// Read the build output
	_, err = io.Copy(os.Stdout, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read build output: %w", err)
	}

	return nil
}

// PullImage pulls an image from a registry
func (d *DockerRuntime) PullImage(imageName string) error {
	ctx := context.Background()

	resp, err := d.client.ImagePull(ctx, imageName, types.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image: %w", err)
	}
	defer resp.Close()

	// Read the pull output
	_, err = io.Copy(os.Stdout, resp)
	if err != nil {
		return fmt.Errorf("failed to read pull output: %w", err)
	}

	return nil
}

// ImageExists checks if an image exists locally
func (d *DockerRuntime) ImageExists(imageName string) (bool, error) {
	ctx := context.Background()

	_, _, err := d.client.ImageInspectWithRaw(ctx, imageName)
	if err != nil {
		if client.IsErrNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to inspect image: %w", err)
	}

	return true, nil
}

// TagImage tags an image with a new name
func (d *DockerRuntime) TagImage(source, target string) error {
	ctx := context.Background()
	return d.client.ImageTag(ctx, source, target)
}

// ListImages lists images matching a prefix
func (d *DockerRuntime) ListImages(prefix string) ([]string, error) {
	ctx := context.Background()

	images, err := d.client.ImageList(ctx, types.ImageListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list images: %w", err)
	}

	var result []string
	for _, img := range images {
		for _, tag := range img.RepoTags {
			if strings.HasPrefix(tag, prefix) {
				result = append(result, tag)
			}
		}
	}

	return result, nil
}

// createBuildContext creates a tar archive of the build context
func createBuildContext(contextDir, dockerfilePath string) (io.Reader, error) {
	buf := new(bytes.Buffer)
	tw := tar.NewWriter(buf)
	defer tw.Close()

	// Add Dockerfile
	dockerfileContent, err := os.ReadFile(dockerfilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read Dockerfile: %w", err)
	}

	hdr := &tar.Header{
		Name: filepath.Base(dockerfilePath),
		Mode: 0644,
		Size: int64(len(dockerfileContent)),
	}

	if err := tw.WriteHeader(hdr); err != nil {
		return nil, err
	}

	if _, err := tw.Write(dockerfileContent); err != nil {
		return nil, err
	}

	// Add other files from context directory if it exists and is different from Dockerfile location
	if contextDir != "" && contextDir != filepath.Dir(dockerfilePath) {
		err := filepath.Walk(contextDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			if info.IsDir() {
				return nil
			}

			relPath, err := filepath.Rel(contextDir, path)
			if err != nil {
				return err
			}

			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			hdr := &tar.Header{
				Name: relPath,
				Mode: int64(info.Mode()),
				Size: info.Size(),
			}

			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}

			_, err = tw.Write(content)
			return err
		})

		if err != nil {
			return nil, fmt.Errorf("failed to add context files: %w", err)
		}
	}

	return buf, nil
}
