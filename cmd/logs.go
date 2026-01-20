package cmd

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/barff/frank/internal/container"
	"github.com/spf13/cobra"
)

var logsCmd = &cobra.Command{
	Use:   "logs <container>",
	Short: "View container logs",
	Long: `View logs from a frank container.

Examples:
  frank logs frank-dev-1
  frank logs frank-dev-1 -f
  frank logs frank-dev-1 --tail 50
  frank logs frank-dev-1 -f -t`,
	Args: cobra.ExactArgs(1),
	RunE: runLogs,
}

var (
	logsFollow     bool
	logsTail       int
	logsTimestamps bool
	logsSince      string
)

func init() {
	rootCmd.AddCommand(logsCmd)

	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "Follow log output")
	logsCmd.Flags().IntVar(&logsTail, "tail", 100, "Number of lines from end")
	logsCmd.Flags().BoolVarP(&logsTimestamps, "timestamps", "t", false, "Show timestamps")
	logsCmd.Flags().StringVar(&logsSince, "since", "", "Show logs since timestamp (e.g., 2024-01-15T10:00:00)")
}

func runLogs(cmd *cobra.Command, args []string) error {
	containerName := args[0]

	runtime, err := container.DetectRuntime(cfg.Runtime.Preferred)
	if err != nil {
		return fmt.Errorf("failed to detect container runtime: %w", err)
	}

	PrintVerbose("Using runtime: %s", runtime.Name())

	// Verify container exists
	_, err = runtime.GetContainer(containerName)
	if err != nil {
		return fmt.Errorf("container not found: %s", containerName)
	}

	// Parse since time if provided
	var sinceTime time.Time
	if logsSince != "" {
		sinceTime, err = time.Parse(time.RFC3339, logsSince)
		if err != nil {
			// Try other formats
			sinceTime, err = time.Parse("2006-01-02T15:04:05", logsSince)
			if err != nil {
				sinceTime, err = time.Parse("2006-01-02", logsSince)
				if err != nil {
					return fmt.Errorf("invalid timestamp format: %s", logsSince)
				}
			}
		}
	}

	// Get container logs
	logOpts := container.LogOptions{
		Follow:     logsFollow,
		Tail:       fmt.Sprintf("%d", logsTail),
		Timestamps: logsTimestamps,
		Since:      sinceTime,
		Stdout:     true,
		Stderr:     true,
	}

	logs, err := runtime.ContainerLogs(containerName, logOpts)
	if err != nil {
		return fmt.Errorf("failed to get logs: %w", err)
	}
	defer logs.Close()

	// Copy logs to stdout
	_, err = io.Copy(os.Stdout, logs)
	if err != nil && err != io.EOF {
		return fmt.Errorf("error reading logs: %w", err)
	}

	return nil
}
