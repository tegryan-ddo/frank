package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/barff/frank/internal/container"
	"github.com/barff/frank/internal/git"
	"github.com/barff/frank/internal/snapshot"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

var stopCmd = &cobra.Command{
	Use:   "stop [containers...]",
	Short: "Stop frank containers",
	Long: `Stop one or more frank containers.

When stopping a container:
1. Git worktrees are cleaned up (can be disabled with --no-cleanup)
2. Container state is persisted to a timestamped image (can be disabled with --no-snapshot)

Examples:
  frank stop frank-dev-1
  frank stop frank-dev-1 frank-prod-2
  frank stop --profile dev
  frank stop --all
  frank stop --all --force --no-snapshot`,
	RunE: runStop,
}

var (
	stopProfile    string
	stopAll        bool
	stopForce      bool
	stopTimeout    time.Duration
	stopNoSnapshot bool
	stopNoCleanup  bool
)

func init() {
	rootCmd.AddCommand(stopCmd)

	stopCmd.Flags().StringVar(&stopProfile, "profile", "", "Stop all containers for this AWS profile")
	stopCmd.Flags().BoolVar(&stopAll, "all", false, "Stop all frank containers")
	stopCmd.Flags().BoolVar(&stopForce, "force", false, "Force stop (SIGKILL instead of SIGTERM)")
	stopCmd.Flags().DurationVar(&stopTimeout, "timeout", 10*time.Second, "Timeout before force stop")
	stopCmd.Flags().BoolVar(&stopNoSnapshot, "no-snapshot", false, "Skip persisting container state to image")
	stopCmd.Flags().BoolVar(&stopNoCleanup, "no-cleanup", false, "Skip git worktree cleanup")
}

func runStop(cmd *cobra.Command, args []string) error {
	runtime, err := container.DetectRuntime(cfg.Runtime.Preferred)
	if err != nil {
		return fmt.Errorf("failed to detect container runtime: %w", err)
	}

	PrintVerbose("Using runtime: %s", runtime.Name())

	// Get list of containers to stop
	var containersToStop []container.Container

	if stopAll {
		containers, err := runtime.ListContainers(container.ContainerFilter{
			All:        false, // Only running containers
			NamePrefix: "frank-",
		})
		if err != nil {
			return fmt.Errorf("failed to list containers: %w", err)
		}
		for _, c := range containers {
			if strings.HasPrefix(c.Name, "frank-") {
				containersToStop = append(containersToStop, c)
			}
		}
	} else if stopProfile != "" {
		containers, err := runtime.ListContainers(container.ContainerFilter{
			All:        false,
			NamePrefix: fmt.Sprintf("frank-%s-", stopProfile),
		})
		if err != nil {
			return fmt.Errorf("failed to list containers: %w", err)
		}
		for _, c := range containers {
			if strings.HasPrefix(c.Name, fmt.Sprintf("frank-%s-", stopProfile)) {
				containersToStop = append(containersToStop, c)
			}
		}
	} else if len(args) > 0 {
		for _, name := range args {
			c, err := runtime.GetContainer(name)
			if err != nil {
				PrintError("Container not found: %s", name)
				continue
			}
			containersToStop = append(containersToStop, *c)
		}
	} else {
		return fmt.Errorf("specify containers to stop, use --profile, or use --all")
	}

	if len(containersToStop) == 0 {
		fmt.Println("No containers to stop")
		return nil
	}

	fmt.Printf("Stopping %d container(s)...\n", len(containersToStop))

	worktreeManager := git.NewWorktreeManager(cfg.Git.WorktreeBase)

	for _, c := range containersToStop {
		if err := stopContainer(runtime, worktreeManager, c); err != nil {
			PrintError("Failed to stop %s: %v", c.Name, err)
		}
	}

	return nil
}

func stopContainer(runtime container.Runtime, worktreeManager *git.WorktreeManager, c container.Container) error {
	fmt.Printf("  Stopping %s...\n", c.Name)

	// Step 1: Clean up git worktree
	if !stopNoCleanup && cfg.Git.CleanupOnStop {
		PrintVerbose("  Cleaning up git worktree for %s", c.Name)
		if err := worktreeManager.Remove(c.Name); err != nil {
			PrintVerbose("  Warning: failed to clean up worktree: %v", err)
		}
	}

	// Step 2: Persist container state to image
	if !stopNoSnapshot {
		// Create timestamped snapshot
		timestampedName := fmt.Sprintf("%s-snapshot:%s", c.Name, time.Now().Format("20060102-150405"))
		PrintVerbose("  Creating snapshot: %s", timestampedName)
		if err := runtime.CommitContainer(c.ID, timestampedName); err != nil {
			PrintVerbose("  Warning: failed to create snapshot: %v", err)
		} else {
			fmt.Printf("    Snapshot saved: %s\n", color.CyanString(timestampedName))
		}

		// Also create repo-based snapshot with :latest tag for auto-resume
		if repoURL, ok := c.Labels["frank.repo"]; ok && repoURL != "" {
			repoSnapshotName := snapshot.GenerateSnapshotName(repoURL)
			PrintVerbose("  Creating repo snapshot: %s", repoSnapshotName)
			if err := runtime.CommitContainer(c.ID, repoSnapshotName); err != nil {
				PrintVerbose("  Warning: failed to create repo snapshot: %v", err)
			} else {
				fmt.Printf("    Repo snapshot saved: %s\n", color.CyanString(repoSnapshotName))
				fmt.Println("    (Next 'frank start' with this repo will resume from this snapshot)")
			}
		}
	}

	// Step 3: Stop the container
	timeout := stopTimeout
	if stopForce {
		timeout = 0
	}

	if err := runtime.StopContainer(c.ID, timeout); err != nil {
		return fmt.Errorf("failed to stop container: %w", err)
	}

	fmt.Printf("    %s stopped\n", color.GreenString(c.Name))
	return nil
}
