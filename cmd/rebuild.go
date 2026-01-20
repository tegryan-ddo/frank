package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/barff/frank/internal/container"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

var rebuildCmd = &cobra.Command{
	Use:   "rebuild",
	Short: "Rebuild the frank container image",
	Long: `Rebuild the frank development container image from the Dockerfile.

This builds a new image with all the required tools:
- Claude Code CLI
- ttyd (web terminal)
- git, gh, curl, jq
- AWS CLI v2
- uv (Python package manager)

You can also rebuild from an existing snapshot to bake installed
tools/configs into the base image.

Examples:
  frank rebuild                                    # Build from Dockerfile
  frank rebuild --no-cache                         # Build without cache
  frank rebuild --tag my-frank:v1                  # Custom tag
  frank rebuild --from-snapshot frank-snapshot-abc123:latest  # Use snapshot as base`,
	RunE: runRebuild,
}

var (
	rebuildNoCache     bool
	rebuildTag         string
	rebuildFromSnapshot string
)

func init() {
	rootCmd.AddCommand(rebuildCmd)

	rebuildCmd.Flags().BoolVar(&rebuildNoCache, "no-cache", false, "Build without using cache")
	rebuildCmd.Flags().StringVar(&rebuildTag, "tag", "frank-dev:latest", "Image tag")
	rebuildCmd.Flags().StringVar(&rebuildFromSnapshot, "from-snapshot", "", "Build from existing snapshot image instead of Dockerfile")
}

func runRebuild(cmd *cobra.Command, args []string) error {
	runtime, err := container.DetectRuntime(cfg.Runtime.Preferred)
	if err != nil {
		return fmt.Errorf("failed to detect container runtime: %w", err)
	}

	PrintVerbose("Using runtime: %s", runtime.Name())

	// If building from snapshot, just tag the existing image
	if rebuildFromSnapshot != "" {
		return rebuildFromExistingSnapshot(runtime)
	}

	// Find Dockerfile
	dockerfilePath, err := findDockerfile()
	if err != nil {
		return err
	}

	fmt.Printf("Building image %s...\n", color.CyanString(rebuildTag))
	PrintVerbose("Using Dockerfile: %s", dockerfilePath)

	buildOpts := container.BuildOptions{
		NoCache:    rebuildNoCache,
		Dockerfile: dockerfilePath,
		Context:    filepath.Dir(dockerfilePath),
	}

	if err := runtime.BuildImage(rebuildTag, buildOpts); err != nil {
		return fmt.Errorf("failed to build image: %w", err)
	}

	fmt.Printf("\n%s Image built successfully: %s\n", color.GreenString("✓"), rebuildTag)
	return nil
}

func rebuildFromExistingSnapshot(runtime container.Runtime) error {
	// Check if snapshot exists
	exists, err := runtime.ImageExists(rebuildFromSnapshot)
	if err != nil {
		return fmt.Errorf("failed to check snapshot: %w", err)
	}
	if !exists {
		// Try to list available snapshots
		fmt.Printf("Snapshot %s not found.\n\n", color.RedString(rebuildFromSnapshot))
		fmt.Println("Available snapshots:")
		listSnapshots(runtime)
		return fmt.Errorf("snapshot not found: %s", rebuildFromSnapshot)
	}

	fmt.Printf("Creating new base image from snapshot...\n")
	fmt.Printf("  Source: %s\n", color.CyanString(rebuildFromSnapshot))
	fmt.Printf("  Target: %s\n", color.CyanString(rebuildTag))

	// Tag the snapshot as the new base image
	if err := runtime.TagImage(rebuildFromSnapshot, rebuildTag); err != nil {
		return fmt.Errorf("failed to tag image: %w", err)
	}

	fmt.Printf("\n%s Base image updated from snapshot: %s\n", color.GreenString("✓"), rebuildTag)
	fmt.Println("\nAll new containers will now use this snapshot as the base.")
	fmt.Println("The snapshot includes any tools/configs installed in that session.")
	return nil
}

func listSnapshots(runtime container.Runtime) {
	images, err := runtime.ListImages("frank-snapshot-")
	if err != nil {
		fmt.Println("  (unable to list snapshots)")
		return
	}

	if len(images) == 0 {
		fmt.Println("  (no snapshots found)")
		return
	}

	for _, img := range images {
		fmt.Printf("  - %s\n", img)
	}
}

func findDockerfile() (string, error) {
	// Look for Dockerfile in standard locations
	searchPaths := []string{
		"build/Dockerfile",
		"Dockerfile",
		filepath.Join(os.Getenv("HOME"), ".config", "frank", "Dockerfile"),
	}

	// If running from installed location, check there too
	execPath, err := os.Executable()
	if err == nil {
		searchPaths = append(searchPaths,
			filepath.Join(filepath.Dir(execPath), "build", "Dockerfile"),
			filepath.Join(filepath.Dir(execPath), "Dockerfile"),
		)
	}

	for _, path := range searchPaths {
		if _, err := os.Stat(path); err == nil {
			absPath, err := filepath.Abs(path)
			if err != nil {
				return path, nil
			}
			return absPath, nil
		}
	}

	return "", fmt.Errorf("Dockerfile not found. Searched in:\n  %s\n\nPlease ensure the Dockerfile exists or create one at build/Dockerfile",
		filepath.Join("build", "Dockerfile"))
}
