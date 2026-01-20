package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/barff/frank/internal/container"
	"github.com/fatih/color"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List running frank containers",
	Long:  `List all frank containers. By default shows only running containers.`,
	RunE:  runList,
}

var (
	listAll    bool
	listQuiet  bool
	listFormat string
)

func init() {
	rootCmd.AddCommand(listCmd)

	listCmd.Flags().BoolVarP(&listAll, "all", "a", false, "Show all containers including stopped")
	listCmd.Flags().BoolVarP(&listQuiet, "quiet", "q", false, "Only display container IDs")
	listCmd.Flags().StringVar(&listFormat, "format", "table", "Output format: table, json, yaml")
}

func runList(cmd *cobra.Command, args []string) error {
	runtime, err := container.DetectRuntime(cfg.Runtime.Preferred)
	if err != nil {
		return fmt.Errorf("failed to detect container runtime: %w", err)
	}

	PrintVerbose("Using runtime: %s", runtime.Name())

	containers, err := runtime.ListContainers(container.ContainerFilter{
		All:        listAll,
		NamePrefix: "frank-",
	})
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}

	// Filter to only frank containers
	var frankContainers []container.Container
	for _, c := range containers {
		if strings.HasPrefix(c.Name, "frank-") {
			frankContainers = append(frankContainers, c)
		}
	}

	if listQuiet {
		for _, c := range frankContainers {
			fmt.Println(c.ID)
		}
		return nil
	}

	switch listFormat {
	case "json":
		return outputJSON(frankContainers)
	case "yaml":
		return outputYAML(frankContainers)
	default:
		return outputTable(frankContainers)
	}
}

func outputTable(containers []container.Container) error {
	if len(containers) == 0 {
		fmt.Println("No frank containers found")
		return nil
	}

	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"NAME", "STATUS", "PORT", "PROFILE", "CREATED", "IMAGE"})
	table.SetBorder(false)
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetCenterSeparator("")
	table.SetColumnSeparator("")
	table.SetRowSeparator("")
	table.SetHeaderLine(false)
	table.SetTablePadding("  ")
	table.SetNoWhiteSpace(true)

	for _, c := range containers {
		// Extract profile from container name (frank-<profile>-<index>)
		profile := extractProfile(c.Name)

		// Get the port
		port := "-"
		for _, p := range c.Ports {
			if p.ContainerPort == 7681 { // ttyd port
				port = fmt.Sprintf("%d", p.HostPort)
				break
			}
		}

		// Format status with color
		status := formatStatus(c.Status)

		// Format created time
		created := c.Created.Format("2006-01-02 15:04")

		table.Append([]string{
			c.Name,
			status,
			port,
			profile,
			created,
			c.Image,
		})
	}

	table.Render()
	return nil
}

func outputJSON(containers []container.Container) error {
	output := make([]map[string]interface{}, len(containers))
	for i, c := range containers {
		output[i] = map[string]interface{}{
			"id":      c.ID,
			"name":    c.Name,
			"status":  c.Status,
			"image":   c.Image,
			"created": c.Created.Format("2006-01-02T15:04:05Z"),
			"ports":   c.Ports,
			"profile": extractProfile(c.Name),
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(output)
}

func outputYAML(containers []container.Container) error {
	output := make([]map[string]interface{}, len(containers))
	for i, c := range containers {
		output[i] = map[string]interface{}{
			"id":      c.ID,
			"name":    c.Name,
			"status":  c.Status,
			"image":   c.Image,
			"created": c.Created.Format("2006-01-02T15:04:05Z"),
			"ports":   c.Ports,
			"profile": extractProfile(c.Name),
		}
	}

	enc := yaml.NewEncoder(os.Stdout)
	return enc.Encode(output)
}

func extractProfile(name string) string {
	// Format: frank-<profile>-<index>
	parts := strings.Split(name, "-")
	if len(parts) >= 3 {
		// Join all parts except first (frank) and last (index)
		return strings.Join(parts[1:len(parts)-1], "-")
	}
	return "-"
}

func formatStatus(status string) string {
	statusLower := strings.ToLower(status)
	if strings.Contains(statusLower, "up") || strings.Contains(statusLower, "running") {
		return color.GreenString(status)
	} else if strings.Contains(statusLower, "exited") || strings.Contains(statusLower, "stopped") {
		return color.RedString(status)
	} else if strings.Contains(statusLower, "created") {
		return color.YellowString(status)
	}
	return status
}
