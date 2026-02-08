package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/barff/frank/internal/alb"
	"github.com/barff/frank/internal/profile"
	"github.com/fatih/color"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

// ECS configuration defaults
const (
	defaultCluster   = "frank"
	defaultService   = "frank"
	defaultLogGroup  = "/ecs/frank"
	defaultTaskFamily = "FrankStack-FrankTask"
)

var ecsCmd = &cobra.Command{
	Use:   "ecs",
	Short: "Manage Frank instances on AWS ECS",
	Long: `Manage Frank instances running on AWS ECS.

This command allows you to start profile-based tasks, list running instances,
stop tasks, and stream logs.

Examples:
  frank ecs start enkai             # Start a profile (creates subdomain)
  frank ecs list                    # List all running Frank tasks
  frank ecs stop enkai              # Stop a profile by name
  frank ecs stop <task-id>          # Stop a specific task by ID
  frank ecs logs <task-id>          # Stream logs from a task`,
}

// Subcommand flags
var (
	ecsCluster      string
	ecsRegion       string
	ecsLogsFollow   bool
	ecsLogsTail     int
	prewarmWorkers  int
)

func init() {
	rootCmd.AddCommand(ecsCmd)

	// Global ECS flags
	ecsCmd.PersistentFlags().StringVar(&ecsCluster, "cluster", defaultCluster, "ECS cluster name")
	ecsCmd.PersistentFlags().StringVar(&ecsRegion, "region", "", "AWS region (default: from AWS config)")

	// Add subcommands
	ecsCmd.AddCommand(ecsStartCmd)
	ecsCmd.AddCommand(ecsListCmd)
	ecsCmd.AddCommand(ecsRunCmd)
	ecsCmd.AddCommand(ecsStopCmd)
	ecsCmd.AddCommand(ecsScaleCmd)
	ecsCmd.AddCommand(ecsLogsCmd)
	ecsCmd.AddCommand(ecsStatusCmd)
	ecsCmd.AddCommand(ecsExecCmd)
	ecsCmd.AddCommand(ecsPrewarmCmd)
	ecsCmd.AddCommand(ecsCleanupCmd)

	// Prewarm command flags
	ecsPrewarmCmd.Flags().IntVar(&prewarmWorkers, "workers", 4, "Number of worktrees to create")

	// Logs command flags
	ecsLogsCmd.Flags().BoolVarP(&ecsLogsFollow, "follow", "f", false, "Follow log output")
	ecsLogsCmd.Flags().IntVarP(&ecsLogsTail, "tail", "t", 50, "Number of lines to show from the end")
}

// getECSClient creates an ECS client with the configured region
func getECSClient(ctx context.Context) (*ecs.Client, error) {
	opts := []func(*config.LoadOptions) error{}
	if ecsRegion != "" {
		opts = append(opts, config.WithRegion(ecsRegion))
	}

	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return ecs.NewFromConfig(cfg), nil
}

// getLogsClient creates a CloudWatch Logs client
func getLogsClient(ctx context.Context) (*cloudwatchlogs.Client, error) {
	opts := []func(*config.LoadOptions) error{}
	if ecsRegion != "" {
		opts = append(opts, config.WithRegion(ecsRegion))
	}

	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return cloudwatchlogs.NewFromConfig(cfg), nil
}

// ============================================================================
// ecs start - Start a profile-based task
// ============================================================================

var ecsStartCmd = &cobra.Command{
	Use:   "start <profile>",
	Short: "Start a Frank task for a profile",
	Long: `Start a Frank ECS task for a configured profile.

The profile must be configured first using 'frank profile add'.
This command will:
  1. Create ALB infrastructure (target group, listener rule) if needed
  2. Start an ECS task with the profile's repository configuration
  3. Register the task in the target group for routing

The task will be accessible at https://<profile>.frank.digitaldevops.io/claude/`,
	Args: cobra.ExactArgs(1),
	RunE: runECSStart,
}

func runECSStart(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	profileName := args[0]

	// Load profile configuration
	p, err := profile.GetProfile(profileName)
	if err != nil {
		return fmt.Errorf("profile %q not found. Create it with: frank profile add %s --repo <url>", profileName, profileName)
	}

	// Check if task is already running for this profile
	existingTask, existingIP := findTaskByProfile(ctx, profileName)
	if existingTask != "" {
		fmt.Printf("Profile %q is already running\n\n", profileName)
		fmt.Printf("  Task ID: %s\n", color.CyanString(existingTask))
		fmt.Printf("  URL:     %s\n", color.CyanString(fmt.Sprintf("https://frank.digitaldevops.io/%s/", profileName)))
		fmt.Println()
		fmt.Printf("Use 'frank ecs stop %s' to stop it first\n", profileName)
		return nil
	}
	_ = existingIP // Will be used later

	fmt.Printf("Starting profile %q...\n", profileName)

	// Create ALB manager
	albMgr, err := alb.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create ALB manager: %w", err)
	}

	// Ensure ALB infrastructure exists
	fmt.Printf("  Ensuring ALB target group...\n")
	tgArn, err := albMgr.EnsureTargetGroup(ctx, profileName)
	if err != nil {
		return fmt.Errorf("failed to ensure target group: %w", err)
	}

	fmt.Printf("  Ensuring ALB listener rule...\n")
	if err := albMgr.EnsureListenerRule(ctx, profileName, tgArn); err != nil {
		return fmt.Errorf("failed to ensure listener rule: %w", err)
	}

	// Get ECS client
	client, err := getECSClient(ctx)
	if err != nil {
		return err
	}

	// Get the service to find the task definition and network config
	descService, err := client.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  aws.String(ecsCluster),
		Services: []string{defaultService},
	})
	if err != nil {
		return fmt.Errorf("failed to describe service: %w", err)
	}

	if len(descService.Services) == 0 {
		return fmt.Errorf("service %s not found in cluster %s", defaultService, ecsCluster)
	}

	service := descService.Services[0]
	taskDef := aws.ToString(service.TaskDefinition)
	networkConfig := service.NetworkConfiguration

	// Build container overrides for profile
	branch := p.Branch
	if branch == "" {
		branch = "main"
	}

	overrides := &types.TaskOverride{
		ContainerOverrides: []types.ContainerOverride{
			{
				Name: aws.String("frank"),
				Environment: []types.KeyValuePair{
					{Name: aws.String("CONTAINER_NAME"), Value: aws.String(profileName)},
					{Name: aws.String("GIT_REPO"), Value: aws.String(p.Repo)},
					{Name: aws.String("GIT_BRANCH"), Value: aws.String(branch)},
					{Name: aws.String("URL_PREFIX"), Value: aws.String("/" + profileName)},
				},
			},
		},
	}

	// Start the task
	fmt.Printf("  Starting ECS task...\n")
	runResult, err := client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:              aws.String(ecsCluster),
		TaskDefinition:       aws.String(taskDef),
		LaunchType:           types.LaunchTypeFargate,
		NetworkConfiguration: networkConfig,
		Overrides:            overrides,
		EnableExecuteCommand: true,
		Tags: []types.Tag{
			{Key: aws.String("frank-profile"), Value: aws.String(profileName)},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to run task: %w", err)
	}

	if len(runResult.Tasks) == 0 {
		if len(runResult.Failures) > 0 {
			return fmt.Errorf("failed to start task: %s - %s",
				aws.ToString(runResult.Failures[0].Reason),
				aws.ToString(runResult.Failures[0].Detail))
		}
		return fmt.Errorf("failed to start task: no task created")
	}

	task := runResult.Tasks[0]
	taskID := extractTaskID(*task.TaskArn)

	// Wait for task to get an IP address
	fmt.Printf("  Waiting for task IP...\n")
	taskIP, err := waitForTaskIP(ctx, client, taskID)
	if err != nil {
		fmt.Printf("  Warning: Could not get task IP: %v\n", err)
		fmt.Printf("  You may need to manually register the task in the target group\n")
	} else {
		// Register task in target group
		fmt.Printf("  Registering task in target group...\n")
		if err := albMgr.RegisterTarget(ctx, tgArn, taskIP, alb.TargetPort); err != nil {
			fmt.Printf("  Warning: Failed to register target: %v\n", err)
		}
	}

	fmt.Printf("\n%s Profile %q started!\n\n", color.GreenString("✓"), profileName)
	fmt.Printf("  Task ID:    %s\n", color.CyanString(taskID))
	fmt.Printf("  Repository: %s\n", p.Repo)
	fmt.Printf("  Branch:     %s\n", branch)
	fmt.Printf("  URL:        %s\n", color.CyanString(fmt.Sprintf("https://frank.digitaldevops.io/%s/", profileName)))
	fmt.Println()
	fmt.Printf("Note: It may take 1-2 minutes for the task to become healthy\n")
	fmt.Printf("Use 'frank ecs logs %s' to view logs\n", taskID)

	return nil
}

// findTaskByProfile finds a running task for a profile by checking tags
func findTaskByProfile(ctx context.Context, profileName string) (taskID string, taskIP string) {
	client, err := getECSClient(ctx)
	if err != nil {
		return "", ""
	}

	// List all tasks
	listResult, err := client.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster: aws.String(ecsCluster),
	})
	if err != nil || len(listResult.TaskArns) == 0 {
		return "", ""
	}

	// Describe tasks with tags
	descResult, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(ecsCluster),
		Tasks:   listResult.TaskArns,
		Include: []types.TaskField{types.TaskFieldTags},
	})
	if err != nil {
		return "", ""
	}

	for _, task := range descResult.Tasks {
		// Check tags for profile match
		for _, tag := range task.Tags {
			if aws.ToString(tag.Key) == "frank-profile" && aws.ToString(tag.Value) == profileName {
				// Found matching task
				tid := extractTaskID(*task.TaskArn)
				ip := ""
				// Get IP from attachments
				for _, att := range task.Attachments {
					if aws.ToString(att.Type) == "ElasticNetworkInterface" {
						for _, detail := range att.Details {
							if aws.ToString(detail.Name) == "privateIPv4Address" {
								ip = aws.ToString(detail.Value)
							}
						}
					}
				}
				return tid, ip
			}
		}
	}

	return "", ""
}

// waitForTaskIP waits for a task to get an IP address
func waitForTaskIP(ctx context.Context, client *ecs.Client, taskID string) (string, error) {
	for i := 0; i < 30; i++ { // Wait up to 60 seconds
		descResult, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(ecsCluster),
			Tasks:   []string{taskID},
		})
		if err != nil {
			return "", err
		}

		if len(descResult.Tasks) == 0 {
			return "", fmt.Errorf("task not found")
		}

		task := descResult.Tasks[0]

		// Check for IP in attachments
		for _, att := range task.Attachments {
			if aws.ToString(att.Type) == "ElasticNetworkInterface" {
				for _, detail := range att.Details {
					if aws.ToString(detail.Name) == "privateIPv4Address" {
						ip := aws.ToString(detail.Value)
						if ip != "" {
							return ip, nil
						}
					}
				}
			}
		}

		time.Sleep(2 * time.Second)
	}

	return "", fmt.Errorf("timeout waiting for task IP")
}

// ============================================================================
// ecs list - List running Frank tasks
// ============================================================================

var ecsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List running Frank tasks on ECS",
	Long:  `List all Frank tasks running on the ECS cluster.`,
	RunE:  runECSList,
}

func runECSList(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	client, err := getECSClient(ctx)
	if err != nil {
		return err
	}

	// List tasks in the cluster
	listResult, err := client.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster: aws.String(ecsCluster),
	})
	if err != nil {
		return fmt.Errorf("failed to list tasks: %w", err)
	}

	if len(listResult.TaskArns) == 0 {
		fmt.Println("No Frank tasks running")
		return nil
	}

	// Describe tasks to get details including tags
	descResult, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(ecsCluster),
		Tasks:   listResult.TaskArns,
		Include: []types.TaskField{types.TaskFieldTags},
	})
	if err != nil {
		return fmt.Errorf("failed to describe tasks: %w", err)
	}

	// Display as table
	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"PROFILE", "TYPE", "TASK ID", "STATUS", "HEALTH", "STARTED"})
	table.SetBorder(false)
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetCenterSeparator("")
	table.SetColumnSeparator("")
	table.SetRowSeparator("")
	table.SetHeaderLine(false)
	table.SetTablePadding("  ")
	table.SetNoWhiteSpace(true)

	for _, task := range descResult.Tasks {
		taskID := extractTaskID(*task.TaskArn)
		status := formatECSStatus(aws.ToString(task.LastStatus))
		health := formatHealthStatus(task.HealthStatus)

		// Extract profile and task type from tags
		profileName := "-"
		taskType := "interactive"
		for _, tag := range task.Tags {
			if aws.ToString(tag.Key) == "frank-profile" {
				profileName = aws.ToString(tag.Value)
			}
			if aws.ToString(tag.Key) == "frank-task-type" {
				taskType = aws.ToString(tag.Value)
			}
		}

		started := "-"
		if task.StartedAt != nil {
			started = task.StartedAt.Format("2006-01-02 15:04")
		}

		table.Append([]string{profileName, taskType, taskID, status, health, started})
	}

	table.Render()
	return nil
}

// ============================================================================
// ecs run - Run a new standalone task
// ============================================================================

var ecsRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Run a new Frank task on ECS",
	Long: `Run a new standalone Frank task on ECS.

This creates a new task separate from the main service, useful for
running parallel workers or isolated experiments.

The task will use the same task definition as the service.`,
	RunE: runECSRun,
}

func runECSRun(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	client, err := getECSClient(ctx)
	if err != nil {
		return err
	}

	// Get the service to find the task definition
	descService, err := client.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  aws.String(ecsCluster),
		Services: []string{defaultService},
	})
	if err != nil {
		return fmt.Errorf("failed to describe service: %w", err)
	}

	if len(descService.Services) == 0 {
		return fmt.Errorf("service %s not found in cluster %s", defaultService, ecsCluster)
	}

	service := descService.Services[0]
	taskDef := aws.ToString(service.TaskDefinition)

	// Get network configuration from the service
	var networkConfig *types.NetworkConfiguration
	if service.NetworkConfiguration != nil {
		networkConfig = service.NetworkConfiguration
	}

	// Run the task
	fmt.Printf("Starting new Frank task...\n")

	runResult, err := client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:              aws.String(ecsCluster),
		TaskDefinition:       aws.String(taskDef),
		LaunchType:           types.LaunchTypeFargate,
		NetworkConfiguration: networkConfig,
		EnableExecuteCommand: true,
	})
	if err != nil {
		return fmt.Errorf("failed to run task: %w", err)
	}

	if len(runResult.Tasks) == 0 {
		if len(runResult.Failures) > 0 {
			return fmt.Errorf("failed to start task: %s - %s",
				aws.ToString(runResult.Failures[0].Reason),
				aws.ToString(runResult.Failures[0].Detail))
		}
		return fmt.Errorf("failed to start task: no task created")
	}

	task := runResult.Tasks[0]
	taskID := extractTaskID(*task.TaskArn)

	fmt.Printf("\n%s Task started successfully!\n\n", color.GreenString("✓"))
	fmt.Printf("  Task ID:    %s\n", color.CyanString(taskID))
	fmt.Printf("  Status:     %s\n", aws.ToString(task.LastStatus))
	fmt.Printf("  Task Def:   %s\n", extractTaskDefName(taskDef))
	fmt.Println()
	fmt.Printf("Use 'frank ecs logs %s' to view logs\n", taskID)
	fmt.Printf("Use 'frank ecs stop %s' to stop the task\n", taskID)

	return nil
}

// ============================================================================
// ecs stop - Stop a running task
// ============================================================================

var ecsStopCmd = &cobra.Command{
	Use:   "stop <profile-or-task-id>",
	Short: "Stop a running Frank task",
	Long: `Stop a Frank task by profile name or task ID.

If the argument matches a profile name with a running task, stops that task.
Otherwise, treats the argument as a task ID.`,
	Args: cobra.ExactArgs(1),
	RunE: runECSStop,
}

func runECSStop(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	arg := args[0]

	// Check if arg is a profile name with a running task
	taskID, taskIP := findTaskByProfile(ctx, arg)
	isProfile := taskID != ""

	if !isProfile {
		// Treat as task ID
		taskID = arg
	}

	client, err := getECSClient(ctx)
	if err != nil {
		return err
	}

	if isProfile {
		fmt.Printf("Stopping profile %q (task %s)...\n", arg, taskID)

		// Deregister from target group
		albMgr, err := alb.NewManager(ctx)
		if err == nil && taskIP != "" {
			tgArn, err := albMgr.GetTargetGroupArn(ctx, arg)
			if err == nil {
				_ = albMgr.DeregisterTarget(ctx, tgArn, taskIP, alb.TargetPort)
			}
		}
	} else {
		fmt.Printf("Stopping task %s...\n", taskID)
	}

	_, err = client.StopTask(ctx, &ecs.StopTaskInput{
		Cluster: aws.String(ecsCluster),
		Task:    aws.String(taskID),
		Reason:  aws.String("Stopped by frank ecs stop"),
	})
	if err != nil {
		return fmt.Errorf("failed to stop task: %w", err)
	}

	if isProfile {
		// Clean up ALB resources (listener rules + target groups)
		albMgr, albErr := alb.NewManager(ctx)
		if albErr == nil {
			fmt.Printf("  Cleaning up ALB resources...\n")
			if err := albMgr.DeleteAllListenerRules(ctx, arg); err != nil {
				fmt.Printf("  Warning: Failed to delete listener rules: %v\n", err)
			}
			if err := albMgr.DeleteAllTargetGroups(ctx, arg); err != nil {
				fmt.Printf("  Warning: Failed to delete target groups: %v\n", err)
			}
		}
		fmt.Printf("%s Profile %q stopped\n", color.GreenString("✓"), arg)
	} else {
		fmt.Printf("%s Task %s stopped\n", color.GreenString("✓"), taskID)
	}
	return nil
}

// ============================================================================
// ecs scale - Scale the service
// ============================================================================

var ecsScaleCmd = &cobra.Command{
	Use:   "scale <count>",
	Short: "Scale the Frank service to a specific number of tasks",
	Long: `Scale the Frank ECS service to run a specific number of tasks.

This updates the desired count of the main service, not standalone tasks.`,
	Args: cobra.ExactArgs(1),
	RunE: runECSScale,
}

func runECSScale(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	client, err := getECSClient(ctx)
	if err != nil {
		return err
	}

	var count int
	_, err = fmt.Sscanf(args[0], "%d", &count)
	if err != nil {
		return fmt.Errorf("invalid count: %s", args[0])
	}

	if count < 0 {
		return fmt.Errorf("count must be non-negative")
	}

	fmt.Printf("Scaling service to %d tasks...\n", count)

	_, err = client.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:      aws.String(ecsCluster),
		Service:      aws.String(defaultService),
		DesiredCount: aws.Int32(int32(count)),
	})
	if err != nil {
		return fmt.Errorf("failed to scale service: %w", err)
	}

	fmt.Printf("%s Service scaled to %d tasks\n", color.GreenString("✓"), count)
	fmt.Println("Note: It may take a moment for new tasks to start or existing tasks to stop")
	return nil
}

// ============================================================================
// ecs logs - Stream task logs
// ============================================================================

var ecsLogsCmd = &cobra.Command{
	Use:   "logs [task-id]",
	Short: "View logs from a Frank task",
	Long: `View logs from a Frank task. If no task ID is provided, shows logs
from the most recent task.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runECSLogs,
}

func runECSLogs(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	ecsClient, err := getECSClient(ctx)
	if err != nil {
		return err
	}

	logsClient, err := getLogsClient(ctx)
	if err != nil {
		return err
	}

	// Get task ID
	var taskID string
	if len(args) > 0 {
		taskID = args[0]
	} else {
		// Find the most recent task
		listResult, err := ecsClient.ListTasks(ctx, &ecs.ListTasksInput{
			Cluster: aws.String(ecsCluster),
		})
		if err != nil {
			return fmt.Errorf("failed to list tasks: %w", err)
		}
		if len(listResult.TaskArns) == 0 {
			return fmt.Errorf("no tasks running")
		}
		taskID = extractTaskID(listResult.TaskArns[0])
	}

	// The log stream name format for Fargate is: prefix/container-name/task-id
	logStreamName := fmt.Sprintf("frank/frank/%s", taskID)

	fmt.Printf("Fetching logs for task %s...\n\n", taskID)

	// Get log events
	input := &cloudwatchlogs.GetLogEventsInput{
		LogGroupName:  aws.String(defaultLogGroup),
		LogStreamName: aws.String(logStreamName),
		StartFromHead: aws.Bool(false),
		Limit:         aws.Int32(int32(ecsLogsTail)),
	}

	result, err := logsClient.GetLogEvents(ctx, input)
	if err != nil {
		// Try with different stream name format (sometimes the container name is different)
		logStreamName = fmt.Sprintf("frank/%s", taskID)
		input.LogStreamName = aws.String(logStreamName)
		result, err = logsClient.GetLogEvents(ctx, input)
		if err != nil {
			return fmt.Errorf("failed to get log events: %w", err)
		}
	}

	// Print existing events
	for _, event := range result.Events {
		timestamp := time.UnixMilli(aws.ToInt64(event.Timestamp)).Format("15:04:05")
		fmt.Printf("%s %s\n", color.YellowString(timestamp), aws.ToString(event.Message))
	}

	// If following, continue to poll for new events
	if ecsLogsFollow {
		fmt.Println(color.CyanString("\n--- Following logs (Ctrl+C to exit) ---\n"))
		nextToken := result.NextForwardToken

		for {
			time.Sleep(2 * time.Second)

			input.NextToken = nextToken
			input.Limit = aws.Int32(100)

			result, err = logsClient.GetLogEvents(ctx, input)
			if err != nil {
				// Log error but continue trying
				PrintVerbose("Error fetching logs: %v", err)
				continue
			}

			for _, event := range result.Events {
				timestamp := time.UnixMilli(aws.ToInt64(event.Timestamp)).Format("15:04:05")
				fmt.Printf("%s %s\n", color.YellowString(timestamp), aws.ToString(event.Message))
			}

			nextToken = result.NextForwardToken
		}
	}

	return nil
}

// ============================================================================
// ecs status - Show service status
// ============================================================================

var ecsStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Frank ECS service status",
	Long:  `Show detailed status of the Frank ECS service.`,
	RunE:  runECSStatus,
}

func runECSStatus(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	client, err := getECSClient(ctx)
	if err != nil {
		return err
	}

	// Describe the service
	descResult, err := client.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  aws.String(ecsCluster),
		Services: []string{defaultService},
	})
	if err != nil {
		return fmt.Errorf("failed to describe service: %w", err)
	}

	if len(descResult.Services) == 0 {
		return fmt.Errorf("service %s not found", defaultService)
	}

	service := descResult.Services[0]

	fmt.Printf("\n%s Service Status\n\n", color.CyanString("Frank ECS"))
	fmt.Printf("  Service:       %s\n", aws.ToString(service.ServiceName))
	fmt.Printf("  Cluster:       %s\n", ecsCluster)
	fmt.Printf("  Status:        %s\n", formatECSStatus(aws.ToString(service.Status)))
	fmt.Printf("  Desired:       %d tasks\n", service.DesiredCount)
	fmt.Printf("  Running:       %d tasks\n", service.RunningCount)
	fmt.Printf("  Pending:       %d tasks\n", service.PendingCount)

	if service.TaskDefinition != nil {
		fmt.Printf("  Task Def:      %s\n", extractTaskDefName(*service.TaskDefinition))
	}

	// Show recent deployments
	if len(service.Deployments) > 0 {
		fmt.Println()
		fmt.Println("  Deployments:")
		for _, d := range service.Deployments {
			status := aws.ToString(d.Status)
			rollout := string(d.RolloutState)
			fmt.Printf("    - %s: %s (running: %d, pending: %d)\n",
				status, rollout, d.RunningCount, d.PendingCount)
		}
	}

	// Show events
	if len(service.Events) > 0 {
		fmt.Println()
		fmt.Println("  Recent Events:")
		limit := 5
		if len(service.Events) < limit {
			limit = len(service.Events)
		}
		for _, e := range service.Events[:limit] {
			timestamp := e.CreatedAt.Format("15:04:05")
			fmt.Printf("    %s %s\n", color.YellowString(timestamp), aws.ToString(e.Message))
		}
	}

	fmt.Println()
	return nil
}

// ============================================================================
// ecs prewarm - Pre-warm repos and worktrees on EFS
// ============================================================================

var ecsPrewarmCmd = &cobra.Command{
	Use:   "prewarm <profile>",
	Short: "Pre-warm repos and worktrees on EFS for faster boot",
	Long: `Pre-warm a profile's repository and worktrees on EFS.

This command SSMs into a running task and creates:
1. A shared base repository clone
2. Multiple worktrees for parallel workers

This dramatically speeds up boot time since containers don't need to clone.

Examples:
  frank ecs prewarm enkai              # Pre-warm with 4 workers (default)
  frank ecs prewarm enkai --workers 8  # Pre-warm with 8 workers`,
	Args: cobra.ExactArgs(1),
	RunE: runECSPrewarm,
}

func runECSPrewarm(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	profileName := args[0]

	// Load profile configuration
	p, err := profile.GetProfile(profileName)
	if err != nil {
		return fmt.Errorf("profile %q not found. Create it with: frank profile add %s --repo <url>", profileName, profileName)
	}

	// Find a running task to execute the prewarm script
	client, err := getECSClient(ctx)
	if err != nil {
		return err
	}

	// List all tasks
	listResult, err := client.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster: aws.String(ecsCluster),
	})
	if err != nil {
		return fmt.Errorf("failed to list tasks: %w", err)
	}

	if len(listResult.TaskArns) == 0 {
		return fmt.Errorf("no running tasks found. Start a task first with 'frank ecs run' or 'frank ecs start <profile>'")
	}

	// Find a running task with execute command enabled
	descResult, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(ecsCluster),
		Tasks:   listResult.TaskArns,
	})
	if err != nil {
		return fmt.Errorf("failed to describe tasks: %w", err)
	}

	var targetTaskID string
	for _, task := range descResult.Tasks {
		if task.EnableExecuteCommand && aws.ToString(task.LastStatus) == "RUNNING" {
			targetTaskID = extractTaskID(*task.TaskArn)
			break
		}
	}

	if targetTaskID == "" {
		return fmt.Errorf("no running task with execute command enabled. Start a task first")
	}

	branch := p.Branch
	if branch == "" {
		branch = "main"
	}

	// Build the prewarm command
	prewarmScript := fmt.Sprintf("/usr/local/bin/prewarm.sh %s %s %d %s",
		profileName, p.Repo, prewarmWorkers, branch)

	awsArgs := []string{
		"ecs", "execute-command",
		"--cluster", ecsCluster,
		"--task", targetTaskID,
		"--container", "frank",
		"--interactive",
		"--command", prewarmScript,
	}

	if ecsRegion != "" {
		awsArgs = append([]string{"--region", ecsRegion}, awsArgs...)
	}

	fmt.Printf("Pre-warming profile %q with %d workers...\n", profileName, prewarmWorkers)
	fmt.Printf("Using task: %s\n", color.CyanString(targetTaskID))
	fmt.Printf("Repository: %s\n", p.Repo)
	fmt.Printf("Branch: %s\n\n", branch)

	// Execute the prewarm script via SSM
	awsCmd := exec.Command("aws", awsArgs...)
	awsCmd.Stdin = os.Stdin
	awsCmd.Stdout = os.Stdout
	awsCmd.Stderr = os.Stderr

	if err := awsCmd.Run(); err != nil {
		return fmt.Errorf("failed to execute prewarm: %w", err)
	}

	fmt.Printf("\n%s Pre-warm complete!\n", color.GreenString("✓"))
	fmt.Printf("\nTo use pre-warmed worktrees, start containers with worker IDs:\n")
	for i := 1; i <= prewarmWorkers; i++ {
		fmt.Printf("  CONTAINER_NAME=%s-%d\n", profileName, i)
	}

	return nil
}

// ============================================================================
// ecs exec - Connect to a task via SSM
// ============================================================================

var ecsExecCmd = &cobra.Command{
	Use:   "exec <profile-or-task-id>",
	Short: "Connect to a Frank task via SSM Session Manager",
	Long: `Connect to a running Frank task using ECS Exec (SSM Session Manager).

Requires the AWS CLI and Session Manager plugin to be installed.
If the argument matches a profile name with a running task, connects to that task.
Otherwise, treats the argument as a task ID.

Examples:
  frank ecs exec enkai              # Connect to profile's task
  frank ecs exec abc123def456       # Connect to task by ID`,
	Args: cobra.ExactArgs(1),
	RunE: runECSExec,
}

func runECSExec(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	arg := args[0]

	// Check if arg is a profile name with a running task
	taskID, _ := findTaskByProfile(ctx, arg)
	if taskID == "" {
		// Treat as task ID
		taskID = arg
	}

	// Verify task exists and has execute command enabled
	client, err := getECSClient(ctx)
	if err != nil {
		return err
	}

	descResult, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(ecsCluster),
		Tasks:   []string{taskID},
	})
	if err != nil {
		return fmt.Errorf("failed to describe task: %w", err)
	}

	if len(descResult.Tasks) == 0 {
		return fmt.Errorf("task %s not found", taskID)
	}

	task := descResult.Tasks[0]
	if !task.EnableExecuteCommand {
		return fmt.Errorf("task %s does not have execute command enabled. Start a new task with 'frank ecs start'", taskID)
	}

	if aws.ToString(task.LastStatus) != "RUNNING" {
		return fmt.Errorf("task %s is not running (status: %s)", taskID, aws.ToString(task.LastStatus))
	}

	// Build the AWS CLI command
	awsArgs := []string{
		"ecs", "execute-command",
		"--cluster", ecsCluster,
		"--task", taskID,
		"--container", "frank",
		"--interactive",
		"--command", "/bin/bash",
	}

	if ecsRegion != "" {
		awsArgs = append([]string{"--region", ecsRegion}, awsArgs...)
	}

	fmt.Printf("Connecting to task %s...\n", color.CyanString(taskID))
	fmt.Printf("Running: aws %s\n\n", strings.Join(awsArgs, " "))

	// Execute AWS CLI - this replaces the current process
	awsCmd := exec.Command("aws", awsArgs...)
	awsCmd.Stdin = os.Stdin
	awsCmd.Stdout = os.Stdout
	awsCmd.Stderr = os.Stderr

	if err := awsCmd.Run(); err != nil {
		return fmt.Errorf("failed to execute command: %w\n\nMake sure you have:\n1. AWS CLI installed\n2. Session Manager plugin installed (https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html)", err)
	}

	return nil
}

// ============================================================================
// ecs cleanup - Remove orphaned ALB resources
// ============================================================================

var ecsCleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Remove orphaned ALB target groups and listener rules",
	Long: `Find and remove ALB target groups and listener rules that belong to
profiles without running tasks.

These orphans accumulate when tasks are stopped without cleaning up ALB
resources. This command identifies them and removes them.`,
	RunE: runECSCleanup,
}

func runECSCleanup(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// Get running tasks to build set of active profiles
	client, err := getECSClient(ctx)
	if err != nil {
		return err
	}

	listResult, err := client.ListTasks(ctx, &ecs.ListTasksInput{
		Cluster: aws.String(ecsCluster),
	})
	if err != nil {
		return fmt.Errorf("failed to list tasks: %w", err)
	}

	runningProfiles := make(map[string]bool)

	if len(listResult.TaskArns) > 0 {
		descResult, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(ecsCluster),
			Tasks:   listResult.TaskArns,
			Include: []types.TaskField{types.TaskFieldTags},
		})
		if err != nil {
			return fmt.Errorf("failed to describe tasks: %w", err)
		}

		for _, task := range descResult.Tasks {
			for _, tag := range task.Tags {
				if aws.ToString(tag.Key) == "frank-profile" {
					runningProfiles[aws.ToString(tag.Value)] = true
				}
			}
		}
	}

	fmt.Printf("Found %d running profile(s)\n", len(runningProfiles))

	// Find orphaned target groups
	albMgr, err := alb.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create ALB manager: %w", err)
	}

	orphans, err := albMgr.FindOrphanedTargetGroups(ctx, runningProfiles)
	if err != nil {
		return fmt.Errorf("failed to find orphaned target groups: %w", err)
	}

	if len(orphans) == 0 {
		fmt.Printf("%s No orphaned ALB resources found\n", color.GreenString("✓"))
		return nil
	}

	fmt.Printf("Found %d orphaned profile(s): %s\n", len(orphans), strings.Join(orphans, ", "))

	// Delete orphaned resources
	deleted := 0
	for _, profileName := range orphans {
		fmt.Printf("  Cleaning up %q...\n", profileName)
		if err := albMgr.DeleteAllListenerRules(ctx, profileName); err != nil {
			fmt.Printf("    Warning: Failed to delete listener rules: %v\n", err)
		}
		if err := albMgr.DeleteAllTargetGroups(ctx, profileName); err != nil {
			fmt.Printf("    Warning: Failed to delete target groups: %v\n", err)
		} else {
			deleted++
		}
	}

	fmt.Printf("%s Cleaned up %d orphaned profile(s)\n", color.GreenString("✓"), deleted)
	return nil
}

// ============================================================================
// Helper functions
// ============================================================================

// extractTaskID extracts the task ID from a full ARN
func extractTaskID(arn string) string {
	parts := strings.Split(arn, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return arn
}

// extractTaskDefName extracts the task definition name from a full ARN
func extractTaskDefName(arn string) string {
	parts := strings.Split(arn, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return arn
}

// formatECSStatus formats an ECS status with color
func formatECSStatus(status string) string {
	statusLower := strings.ToLower(status)
	switch {
	case statusLower == "active" || statusLower == "running":
		return color.GreenString(status)
	case statusLower == "pending" || statusLower == "provisioning":
		return color.YellowString(status)
	case statusLower == "stopped" || statusLower == "deprovisioning":
		return color.RedString(status)
	default:
		return status
	}
}

// formatHealthStatus formats a health status with color
func formatHealthStatus(status types.HealthStatus) string {
	switch status {
	case types.HealthStatusHealthy:
		return color.GreenString("HEALTHY")
	case types.HealthStatusUnhealthy:
		return color.RedString("UNHEALTHY")
	case types.HealthStatusUnknown:
		return color.YellowString("UNKNOWN")
	default:
		return string(status)
	}
}

// outputECSJSON outputs tasks as JSON
func outputECSJSON(tasks []types.Task) error {
	output := make([]map[string]interface{}, len(tasks))
	for i, task := range tasks {
		output[i] = map[string]interface{}{
			"taskArn":    aws.ToString(task.TaskArn),
			"taskId":     extractTaskID(*task.TaskArn),
			"status":     aws.ToString(task.LastStatus),
			"health":     string(task.HealthStatus),
			"startedAt":  task.StartedAt,
			"cpu":        aws.ToString(task.Cpu),
			"memory":     aws.ToString(task.Memory),
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(output)
}
