package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/barff/frank/internal/aws"
	"github.com/barff/frank/internal/claude"
	"github.com/barff/frank/internal/container"
	"github.com/barff/frank/internal/git"
	"github.com/barff/frank/internal/notification"
	"github.com/barff/frank/internal/snapshot"
	"github.com/barff/frank/internal/terminal"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

var startCmd = &cobra.Command{
	Use:   "start [local-path]",
	Short: "Start a new frank container",
	Long: `Start a new isolated Claude Code development environment.

You can provide a local directory path as an argument to mount it into the container,
or use --repo to clone a git repository.

Examples:
  frank start /path/to/project -p dev          # Mount local directory
  frank start --repo https://github.com/user/project -p dev  # Clone git repo
  frank start --profile all                    # Just start with AWS credentials
  frank start --name custom-session --port 9000`,
	Args: cobra.MaximumNArgs(1),
	RunE: runStart,
}

var (
	startProfile         string
	startRepo            string
	startBranch          string
	startName            string
	startPort            int
	startNoNotifications bool
	startDetach          bool
	startFresh           bool
	startMountSSH        bool
	startMountGH         bool
)

func init() {
	rootCmd.AddCommand(startCmd)

	startCmd.Flags().StringVarP(&startProfile, "profile", "p", "", "AWS profile name or 'all' for full ~/.aws mount")
	startCmd.Flags().StringVarP(&startRepo, "repo", "r", "", "Git repository URL to clone")
	startCmd.Flags().StringVarP(&startBranch, "branch", "b", "", "Branch to checkout (default: main)")
	startCmd.Flags().StringVarP(&startName, "name", "n", "", "Custom container name suffix")
	startCmd.Flags().IntVar(&startPort, "port", 0, "Override starting port (default: from config)")
	startCmd.Flags().BoolVar(&startNoNotifications, "no-notifications", false, "Disable notifications for this container")
	startCmd.Flags().BoolVarP(&startDetach, "detach", "d", false, "Run in background")
	startCmd.Flags().BoolVar(&startFresh, "fresh", false, "Force fresh clone, ignore existing snapshot")
	startCmd.Flags().BoolVar(&startMountSSH, "ssh", false, "Mount ~/.ssh for git SSH authentication")
	startCmd.Flags().BoolVar(&startMountGH, "gh", false, "Mount ~/.config/gh for GitHub CLI authentication")
}

func runStart(cmd *cobra.Command, args []string) error {
	// Handle local path argument
	var localPath string
	if len(args) > 0 {
		absPath, err := filepath.Abs(args[0])
		if err != nil {
			return fmt.Errorf("failed to resolve path: %w", err)
		}
		info, err := os.Stat(absPath)
		if err != nil {
			return fmt.Errorf("path does not exist: %s", absPath)
		}
		if !info.IsDir() {
			return fmt.Errorf("path is not a directory: %s", absPath)
		}
		localPath = absPath
		PrintVerbose("Using local path: %s", localPath)
	}

	// Detect container runtime
	runtime, err := container.DetectRuntime(cfg.Runtime.Preferred)
	if err != nil {
		return fmt.Errorf("failed to detect container runtime: %w", err)
	}
	PrintVerbose("Using runtime: %s", runtime.Name())

	// Determine which image to use
	imageName := cfg.Container.Image
	usingSnapshot := false

	// Check for existing snapshot if repo is provided and --fresh is not set
	if startRepo != "" && !startFresh {
		snapshotName := snapshot.GenerateSnapshotName(startRepo)
		snapshotExists, err := runtime.ImageExists(snapshotName)
		if err != nil {
			PrintVerbose("Warning: failed to check snapshot: %v", err)
		}
		if snapshotExists {
			imageName = snapshotName
			usingSnapshot = true
			fmt.Printf("Found existing snapshot for this repo: %s\n", color.CyanString(snapshotName))
			fmt.Println("Resuming from snapshot. Use --fresh to start from scratch.")
		}
	}

	// Check if image exists
	imageExists, err := runtime.ImageExists(imageName)
	if err != nil {
		PrintVerbose("Warning: failed to check image: %v", err)
	}
	if !imageExists {
		if usingSnapshot {
			// Snapshot doesn't exist, fall back to base image
			imageName = cfg.Container.Image
			usingSnapshot = false
			imageExists, err = runtime.ImageExists(imageName)
			if err != nil {
				PrintVerbose("Warning: failed to check image: %v", err)
			}
		}
		if !imageExists {
			fmt.Printf("Image %s not found. Run 'frank rebuild' first.\n", cfg.Container.Image)
			return fmt.Errorf("image not found: %s", cfg.Container.Image)
		}
	}

	// Determine profile
	profile := startProfile
	if profile == "" {
		profile = cfg.AWS.DefaultProfile
	}
	if profile == "" {
		profile = "default"
	}

	// Generate container name
	containerName, err := generateContainerName(runtime, profile)
	if err != nil {
		return fmt.Errorf("failed to generate container name: %w", err)
	}
	PrintVerbose("Container name: %s", containerName)

	// Allocate port
	portAllocator := terminal.NewPortAllocator(cfg.Container.BasePort, cfg.Container.MaxPort)

	// Mark ports from existing frank containers as used
	existingContainers, _ := runtime.ListContainers(container.ContainerFilter{
		All:        false, // Only running containers
		NamePrefix: "frank-",
	})
	for _, c := range existingContainers {
		for _, p := range c.Ports {
			portAllocator.MarkUsed(p.HostPort, c.Name)
		}
	}

	port := startPort
	if port == 0 {
		port, err = portAllocator.Allocate(containerName)
		if err != nil {
			return fmt.Errorf("failed to allocate port: %w", err)
		}
	}
	PrintVerbose("Allocated port: %d", port)

	// Setup AWS credentials
	var awsEnv []string
	var awsVolumes []container.VolumeMount

	if profile == "all" {
		// Mount entire ~/.aws directory
		awsDir := aws.GetAWSDir()
		awsVolumes = append(awsVolumes, container.VolumeMount{
			HostPath:      awsDir,
			ContainerPath: "/root/.aws",
			ReadOnly:      true,
		})
		// Set default profile and region for MCP servers
		awsEnv = append(awsEnv, "AWS_PROFILE=default")
		if region := os.Getenv("AWS_REGION"); region != "" {
			awsEnv = append(awsEnv, fmt.Sprintf("AWS_REGION=%s", region))
		} else if region := os.Getenv("AWS_DEFAULT_REGION"); region != "" {
			awsEnv = append(awsEnv, fmt.Sprintf("AWS_REGION=%s", region))
		} else {
			awsEnv = append(awsEnv, "AWS_REGION=us-east-1")
		}
		PrintVerbose("Mounting AWS directory: %s", awsDir)
	} else if profile != "" && profile != "default" {
		// Get temporary credentials for specific profile
		ssoManager := aws.NewSSOManager()

		// Ensure we're logged in
		if err := ssoManager.EnsureLoggedIn(profile, cfg.AWS.AutoLogin); err != nil {
			return fmt.Errorf("failed to ensure AWS login: %w", err)
		}

		// Get credentials
		creds, err := ssoManager.GetCredentials(profile)
		if err != nil {
			return fmt.Errorf("failed to get AWS credentials: %w", err)
		}

		awsEnv = aws.CredentialsToEnv(creds)
		// Also set AWS_PROFILE for MCP servers that need it
		awsEnv = append(awsEnv, fmt.Sprintf("AWS_PROFILE=%s", profile))
		PrintVerbose("Injecting AWS credentials for profile: %s", profile)
	} else {
		// Default profile - mount ~/.aws if it exists and set env vars for MCP
		awsDir := aws.GetAWSDir()
		if _, err := os.Stat(awsDir); err == nil {
			awsVolumes = append(awsVolumes, container.VolumeMount{
				HostPath:      awsDir,
				ContainerPath: "/root/.aws",
				ReadOnly:      true,
			})
			PrintVerbose("Mounting AWS directory: %s", awsDir)
		}
		awsEnv = append(awsEnv, "AWS_PROFILE=default")
		if region := os.Getenv("AWS_REGION"); region != "" {
			awsEnv = append(awsEnv, fmt.Sprintf("AWS_REGION=%s", region))
		} else if region := os.Getenv("AWS_DEFAULT_REGION"); region != "" {
			awsEnv = append(awsEnv, fmt.Sprintf("AWS_REGION=%s", region))
		} else {
			awsEnv = append(awsEnv, "AWS_REGION=us-east-1")
		}
	}

	// Setup MCP configuration
	mcpManager := claude.NewMCPManager(cfg.MCP.ConfigDir)
	var mcpServers []claude.MCPServer
	for _, s := range cfg.MCP.Servers {
		mcpServers = append(mcpServers, claude.MCPServer{
			Name:    s.Name,
			Enabled: s.Enabled,
		})
	}

	mcpConfigPath, err := mcpManager.CreateContainerMCPConfig(mcpServers)
	if err != nil {
		PrintVerbose("Warning: failed to create MCP config: %v", err)
	}

	// Setup volumes
	var volumes []container.VolumeMount
	volumes = append(volumes, awsVolumes...)

	// Mount MCP config if created
	if mcpConfigPath != "" {
		volumes = append(volumes, container.VolumeMount{
			HostPath:      mcpConfigPath,
			ContainerPath: "/root/.claude/mcp.json",
			ReadOnly:      true,
		})
	}

	// Setup workspace: local path > git repo > snapshot
	if localPath != "" {
		// Mount local directory directly
		volumes = append(volumes, container.VolumeMount{
			HostPath:      localPath,
			ContainerPath: cfg.Container.WorkspaceMount,
			ReadOnly:      false,
		})
		PrintVerbose("Mounting local directory: %s", localPath)
	} else if startRepo != "" && !usingSnapshot {
		// Clone git repo into worktree
		worktreeManager := git.NewWorktreeManager(cfg.Git.WorktreeBase)
		worktreePath, err := worktreeManager.Create(containerName, startRepo, startBranch)
		if err != nil {
			return fmt.Errorf("failed to create worktree: %w", err)
		}

		volumes = append(volumes, container.VolumeMount{
			HostPath:      worktreePath,
			ContainerPath: cfg.Container.WorkspaceMount,
			ReadOnly:      false,
		})
		PrintVerbose("Created worktree at: %s", worktreePath)
	} else if usingSnapshot {
		PrintVerbose("Using snapshot - workspace is already in the container image")
	}

	// Port mapping:
	// - Main port (8080): Combined web view
	// - Main port + 1 (8081): Claude terminal
	// - Main port + 2 (8082): Bash terminal
	// - Main port + 3 (8083): Status API
	webPort := port
	claudePort := port + 1
	bashPort := port + 2
	statusPort := port + 3

	// Combine environment variables
	env := awsEnv
	env = append(env, fmt.Sprintf("WEB_PORT=%d", 7680))
	env = append(env, fmt.Sprintf("TTYD_PORT=%d", 7681))
	env = append(env, fmt.Sprintf("BASH_PORT=%d", 7682))
	env = append(env, fmt.Sprintf("STATUS_PORT=%d", 7683))
	// Pass host ports so the HTML can reference them correctly
	env = append(env, fmt.Sprintf("HOST_CLAUDE_PORT=%d", claudePort))
	env = append(env, fmt.Sprintf("HOST_BASH_PORT=%d", bashPort))
	env = append(env, fmt.Sprintf("HOST_STATUS_PORT=%d", statusPort))
	// Pass container name for worktree naming
	env = append(env, fmt.Sprintf("CONTAINER_NAME=%s", containerName))
	// Pass git repo info if provided (for container-side cloning with worktrees)
	if startRepo != "" {
		env = append(env, fmt.Sprintf("GIT_REPO=%s", startRepo))
		if startBranch != "" {
			env = append(env, fmt.Sprintf("GIT_BRANCH=%s", startBranch))
		}
	}

	// Setup GitHub authentication
	if ghToken := GetGitHubToken(); ghToken != "" {
		env = append(env, fmt.Sprintf("GH_TOKEN=%s", ghToken))
		PrintVerbose("GitHub token configured")
	}

	// Setup Claude authentication
	// Mount ~/.claude directory for OAuth credentials
	claudeDir := filepath.Join(getHomeDir(), ".claude")
	if _, err := os.Stat(claudeDir); err == nil {
		volumes = append(volumes, container.VolumeMount{
			HostPath:      claudeDir,
			ContainerPath: "/root/.claude",
			ReadOnly:      false, // Claude may need to refresh tokens
		})
		PrintVerbose("Mounting Claude credentials directory: %s", claudeDir)
	}
	// Also support ANTHROPIC_API_KEY for direct API key auth
	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		env = append(env, fmt.Sprintf("ANTHROPIC_API_KEY=%s", apiKey))
		PrintVerbose("Anthropic API key configured")
	}

	// Mount SSH directory if requested (via flag or config)
	if startMountSSH || cfg.GitHub.MountSSH {
		if sshDir := GetSSHDir(); sshDir != "" {
			volumes = append(volumes, container.VolumeMount{
				HostPath:      sshDir,
				ContainerPath: "/root/.ssh",
				ReadOnly:      true,
			})
			PrintVerbose("Mounting SSH directory: %s", sshDir)
		} else if startMountSSH {
			fmt.Println("Warning: --ssh specified but no ~/.ssh directory found")
		}
	}

	// Mount GitHub CLI config if requested (via flag or config)
	if startMountGH || cfg.GitHub.MountGHConfig {
		if ghDir := GetGHConfigDir(); ghDir != "" {
			volumes = append(volumes, container.VolumeMount{
				HostPath:      ghDir,
				ContainerPath: "/root/.config/gh",
				ReadOnly:      true,
			})
			PrintVerbose("Mounting GitHub CLI config: %s", ghDir)
		} else if startMountGH {
			fmt.Println("Warning: --gh specified but no ~/.config/gh directory found")
			fmt.Println("Run 'gh auth login' first to authenticate with GitHub CLI")
		}
	}

	// Create container labels
	labels := map[string]string{
		"frank.profile": profile,
		"frank.port":    fmt.Sprintf("%d", port),
	}
	if startRepo != "" {
		labels["frank.repo"] = startRepo
	}

	// Create container
	containerOpts := container.ContainerOptions{
		Name:  containerName,
		Image: imageName,
		Ports: []container.PortMapping{
			{HostPort: webPort, ContainerPort: 7680, Protocol: "tcp"},
			{HostPort: claudePort, ContainerPort: 7681, Protocol: "tcp"},
			{HostPort: bashPort, ContainerPort: 7682, Protocol: "tcp"},
			{HostPort: statusPort, ContainerPort: 7683, Protocol: "tcp"},
		},
		Env:       env,
		Volumes:   volumes,
		WorkDir:   cfg.Container.WorkspaceMount,
		TTY:       true,
		OpenStdin: true,
		Labels:    labels,
	}

	fmt.Printf("Creating container %s...\n", color.CyanString(containerName))

	containerID, err := runtime.CreateContainer(containerOpts)
	if err != nil {
		return fmt.Errorf("failed to create container: %w", err)
	}
	PrintVerbose("Container ID: %s", containerID)

	// Start container
	if err := runtime.StartContainer(containerID); err != nil {
		// Cleanup on failure
		runtime.RemoveContainer(containerID, true)
		return fmt.Errorf("failed to start container: %w", err)
	}

	fmt.Printf("\n%s Container started successfully!\n\n", color.GreenString("✓"))
	fmt.Printf("  Name:     %s\n", color.CyanString(containerName))
	fmt.Printf("  Terminal: %s (split view)\n", color.CyanString(fmt.Sprintf("http://localhost:%d", webPort)))
	fmt.Printf("  Claude:   %s\n", color.YellowString(fmt.Sprintf("http://localhost:%d", claudePort)))
	fmt.Printf("  Bash:     %s\n", color.YellowString(fmt.Sprintf("http://localhost:%d", bashPort)))
	fmt.Printf("  Profile:  %s\n", profile)

	if localPath != "" {
		fmt.Printf("  Path:     %s\n", localPath)
	} else if startRepo != "" {
		fmt.Printf("  Repo:     %s\n", startRepo)
		if startBranch != "" {
			fmt.Printf("  Branch:   %s\n", startBranch)
		}
		if usingSnapshot {
			fmt.Printf("  Image:    %s (snapshot)\n", color.GreenString(imageName))
		}
	}

	fmt.Println()

	// Start notification monitor if enabled
	if !startNoNotifications && cfg.Notifications.Enabled {
		fmt.Println("Starting notification monitor...")
		monitor := notification.NewMonitor(
			containerID,
			containerName,
			runtime,
			cfg.Notifications,
		)
		go monitor.Start()
	}

	// If not detached, show instructions
	if !startDetach {
		fmt.Printf("Open %s in your browser to access Claude Code.\n", color.CyanString(fmt.Sprintf("http://localhost:%d", port)))
		fmt.Printf("Use 'frank stop %s' to stop the container.\n", containerName)
	}

	return nil
}

// generateContainerName generates a unique container name
func generateContainerName(rt container.Runtime, profile string) (string, error) {
	if startName != "" {
		return fmt.Sprintf("frank-%s-%s", profile, startName), nil
	}

	// Find next available index
	containers, err := rt.ListContainers(container.ContainerFilter{
		All:        true,
		NamePrefix: fmt.Sprintf("frank-%s-", profile),
	})
	if err != nil {
		return "", err
	}

	maxIndex := 0
	for _, c := range containers {
		if !strings.HasPrefix(c.Name, fmt.Sprintf("frank-%s-", profile)) {
			continue
		}

		parts := strings.Split(c.Name, "-")
		if len(parts) >= 3 {
			var index int
			fmt.Sscanf(parts[len(parts)-1], "%d", &index)
			if index > maxIndex {
				maxIndex = index
			}
		}
	}

	return fmt.Sprintf("frank-%s-%d", profile, maxIndex+1), nil
}

// getHomeDir returns the user's home directory
func getHomeDir() string {
	if home := os.Getenv("HOME"); home != "" {
		return home
	}
	if home := os.Getenv("USERPROFILE"); home != "" {
		return home
	}
	home, _ := os.UserHomeDir()
	return home
}

// ensureDir ensures a directory exists
func ensureDir(path string) error {
	return os.MkdirAll(path, 0755)
}

// getConfigFilePath returns the path for a config file in the frank config directory
func getConfigFilePath(filename string) string {
	return filepath.Join(getHomeDir(), ".config", "frank", filename)
}
