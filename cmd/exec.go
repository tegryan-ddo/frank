package cmd

import (
	"fmt"
	"os"

	"github.com/barff/frank/internal/container"
	"github.com/spf13/cobra"
)

var execCmd = &cobra.Command{
	Use:   "exec <container> <command> [args...]",
	Short: "Execute a command in a container",
	Long: `Execute a command inside a running frank container.

Examples:
  frank exec frank-dev-1 bash
  frank exec frank-dev-1 git status
  frank exec -it frank-dev-1 /bin/bash
  frank exec -u root frank-dev-1 apt update`,
	Args: cobra.MinimumNArgs(2),
	RunE: runExec,
}

var (
	execInteractive bool
	execTTY         bool
	execUser        string
	execWorkDir     string
)

func init() {
	rootCmd.AddCommand(execCmd)

	execCmd.Flags().BoolVarP(&execInteractive, "interactive", "i", false, "Keep STDIN open")
	execCmd.Flags().BoolVarP(&execTTY, "tty", "t", false, "Allocate pseudo-TTY")
	execCmd.Flags().StringVarP(&execUser, "user", "u", "developer", "User to run as")
	execCmd.Flags().StringVarP(&execWorkDir, "workdir", "w", "", "Working directory inside container")
}

func runExec(cmd *cobra.Command, args []string) error {
	containerName := args[0]
	command := args[1:]

	runtime, err := container.DetectRuntime(cfg.Runtime.Preferred)
	if err != nil {
		return fmt.Errorf("failed to detect container runtime: %w", err)
	}

	PrintVerbose("Using runtime: %s", runtime.Name())

	// Verify container exists and is running
	c, err := runtime.GetContainer(containerName)
	if err != nil {
		return fmt.Errorf("container not found: %s", containerName)
	}

	if c.Status != "running" {
		return fmt.Errorf("container is not running: %s (status: %s)", containerName, c.Status)
	}

	// Execute command
	execOpts := container.ExecOptions{
		Interactive: execInteractive,
		TTY:         execTTY,
		User:        execUser,
		WorkDir:     execWorkDir,
		Stdin:       os.Stdin,
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
	}

	return runtime.ExecInContainer(containerName, command, execOpts)
}
