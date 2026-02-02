package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/barff/frank/internal/alb"
	"github.com/barff/frank/internal/profile"
	"github.com/barff/frank/internal/scrum"
	"github.com/fatih/color"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

var (
	scrumGoal         string
	scrumModel        string
	scrumPlannerModel string
)

var scrumCmd = &cobra.Command{
	Use:   "scrum",
	Short: "Multi-agent scrum orchestration",
	Long: `Orchestrate multi-agent coding tasks using a scrum-like workflow.

The scrum system decomposes a high-level goal into independent work items,
dispatches parallel Codex workers to execute each item, and collects results.

Workflow:
  1. A planner agent analyzes the codebase and decomposes the goal
  2. Work items are grouped into execution waves by dependency order
  3. Each wave is dispatched in parallel (one ECS task per work item)
  4. Results are collected and summarized

Examples:
  frank scrum run myproject --goal "Add comprehensive unit tests"
  frank scrum status
  frank scrum list`,
}

var scrumRunCmd = &cobra.Command{
	Use:   "run <profile>",
	Short: "Run a multi-agent scrum session",
	Long: `Decompose a goal into work items and dispatch parallel Codex workers.

This is the main orchestration command. It will:
  1. Dispatch a planner agent to decompose the goal into work items
  2. Show the decomposed plan grouped by execution wave
  3. Dispatch workers for each wave in parallel
  4. Wait for each wave to complete before starting the next
  5. Collect and display results from all workers

Examples:
  frank scrum run myproject --goal "Refactor the authentication module"
  frank scrum run myproject --goal "Add API endpoints for user management" --model codex-mini-latest
  frank scrum run myproject --goal "Fix all linting errors" --planner-model gpt-5.2-codex`,
	Args: cobra.ExactArgs(1),
	RunE: runScrumRun,
}

var scrumStatusCmd = &cobra.Command{
	Use:   "status [session-id]",
	Short: "Show status of a scrum session",
	Long: `Show the status of a scrum session. If no session ID is provided,
shows the most recent session.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runScrumStatus,
}

var scrumListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all scrum sessions",
	Long:  `List all scrum sessions with their status, goal, and work item counts.`,
	RunE:  runScrumList,
}

func init() {
	rootCmd.AddCommand(scrumCmd)
	scrumCmd.AddCommand(scrumRunCmd)
	scrumCmd.AddCommand(scrumStatusCmd)
	scrumCmd.AddCommand(scrumListCmd)

	// Inherit cluster/region flags so scrum commands can reach ECS
	scrumCmd.PersistentFlags().StringVar(&ecsCluster, "cluster", defaultCluster, "ECS cluster name")
	scrumCmd.PersistentFlags().StringVar(&ecsRegion, "region", "", "AWS region")

	scrumRunCmd.Flags().StringVar(&scrumGoal, "goal", "", "The goal to accomplish")
	scrumRunCmd.MarkFlagRequired("goal")
	scrumRunCmd.Flags().StringVar(&scrumModel, "model", "codex-mini-latest", "Model for workers")
	scrumRunCmd.Flags().StringVar(&scrumPlannerModel, "planner-model", "gpt-5.2-codex", "Model for the planning step")
}

// ============================================================================
// scrum run — Main orchestration command
// ============================================================================

func runScrumRun(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	profileName := args[0]

	// Load profile
	p, err := profile.GetProfile(profileName)
	if err != nil {
		return fmt.Errorf("profile %q not found. Create it with: frank profile add %s --repo <url>", profileName, profileName)
	}

	// Generate session ID
	sessionID := scrum.NewSessionID()

	session := &scrum.ScrumSession{
		ID:        sessionID,
		Profile:   profileName,
		Goal:      scrumGoal,
		CreatedAt: time.Now(),
		Status:    "planning",
	}

	fmt.Printf("\n%s\n\n", color.CyanString("=== Frank Scrum Orchestrator ==="))
	fmt.Printf("  Session:  %s\n", color.CyanString(sessionID))
	fmt.Printf("  Profile:  %s\n", profileName)
	fmt.Printf("  Goal:     %s\n", scrumGoal)
	fmt.Printf("  Planner:  %s\n", scrumPlannerModel)
	fmt.Printf("  Workers:  %s\n", scrumModel)
	fmt.Println()

	// Save initial session state
	if err := scrum.SaveSession(session); err != nil {
		PrintVerbose("Warning: failed to save session: %v", err)
	}

	// ---- Phase 1: Plan ----
	fmt.Printf("%s %s\n", color.YellowString("~"), color.CyanString("Phase 1: Planning"))
	fmt.Printf("  Dispatching planner agent...\n")

	plannerContainerName, plannerTaskID, err := dispatchScrumTask(ctx, p, profileName, sessionID, "planner", scrumPlannerModel, buildPlannerPrompt(scrumGoal), nil)
	if err != nil {
		session.Status = "failed"
		scrum.SaveSession(session)
		return fmt.Errorf("failed to dispatch planner: %w", err)
	}

	fmt.Printf("  Planner task: %s (%s)\n", color.CyanString(plannerTaskID), plannerContainerName)
	fmt.Printf("  Waiting for planner to complete...\n")

	exitCode, err := waitForTask(ctx, plannerTaskID)
	if err != nil {
		session.Status = "failed"
		scrum.SaveSession(session)
		return fmt.Errorf("planner task failed: %w", err)
	}

	if exitCode != 0 {
		session.Status = "failed"
		scrum.SaveSession(session)
		return fmt.Errorf("planner exited with code %d. Check logs: frank ecs logs %s", exitCode, plannerTaskID)
	}

	fmt.Printf("  %s Planner completed\n", color.GreenString("~"))

	// Read and parse plan from results
	plan, err := readPlanFromResults(plannerContainerName)
	if err != nil {
		session.Status = "failed"
		scrum.SaveSession(session)
		return fmt.Errorf("failed to read plan: %w", err)
	}

	session.Plan = plan
	session.Status = "dispatching"
	scrum.SaveSession(session)

	// ---- Display Plan ----
	fmt.Printf("\n%s %s\n\n", color.GreenString("~"), color.CyanString("Decomposed Plan"))
	fmt.Printf("  Goal:    %s\n", plan.Goal)
	fmt.Printf("  Summary: %s\n", plan.Summary)
	fmt.Printf("  Items:   %d\n\n", len(plan.WorkItems))

	waves := scrum.GetExecutionWaves(plan.WorkItems)
	for i, wave := range waves {
		fmt.Printf("  %s\n", color.CyanString("Wave %d", i))
		for _, item := range wave {
			deps := ""
			if len(item.DependsOn) > 0 {
				depStrs := make([]string, len(item.DependsOn))
				for j, d := range item.DependsOn {
					depStrs[j] = fmt.Sprintf("#%d", d)
				}
				deps = color.YellowString(" (depends on %s)", strings.Join(depStrs, ", "))
			}
			files := ""
			if len(item.Files) > 0 {
				files = fmt.Sprintf("\n      Files: %s", strings.Join(item.Files, ", "))
			}
			fmt.Printf("    #%-2d %s%s%s\n", item.ID, item.Title, deps, files)
		}
		fmt.Println()
	}

	// ---- Phase 2: Dispatch ----
	session.Status = "running"
	scrum.SaveSession(session)

	for waveIdx, wave := range waves {
		fmt.Printf("%s %s\n", color.YellowString("~"), color.CyanString("Phase 2: Dispatching Wave %d (%d items)", waveIdx, len(wave)))

		var waveTasks []scrum.TaskStatus

		// Dispatch all items in this wave in parallel
		for _, item := range wave {
			containerName, taskID, err := dispatchScrumTask(
				ctx, p, profileName, sessionID,
				fmt.Sprintf("item-%d", item.ID),
				scrumModel,
				item.Prompt,
				[]types.Tag{
					{Key: aws.String("frank-scrum-id"), Value: aws.String(sessionID)},
					{Key: aws.String("frank-scrum-item"), Value: aws.String(fmt.Sprintf("%d", item.ID))},
				},
			)
			if err != nil {
				fmt.Printf("  %s Failed to dispatch item #%d (%s): %v\n",
					color.RedString("~"), item.ID, item.Title, err)
				waveTasks = append(waveTasks, scrum.TaskStatus{
					WorkItem: item,
					Status:   "FAILED",
				})
				continue
			}

			fmt.Printf("  %s Dispatched #%-2d %-40s %s\n",
				color.GreenString("~"), item.ID, item.Title, color.CyanString(taskID))

			waveTasks = append(waveTasks, scrum.TaskStatus{
				WorkItem:      item,
				ContainerName: containerName,
				TaskArn:       taskID, // We use task ID here; full ARN not always needed
				TaskID:        taskID,
				Status:        "RUNNING",
				StartedAt:     time.Now(),
			})
		}

		session.Tasks = append(session.Tasks, waveTasks...)
		scrum.SaveSession(session)

		// Wait for all tasks in this wave to complete
		fmt.Printf("\n  Waiting for wave %d to complete...\n\n", waveIdx)
		waitForWave(ctx, session, waveTasks)

		// Update session with final statuses
		scrum.SaveSession(session)

		// Print wave results
		allPassed := true
		for _, ts := range waveTasks {
			if ts.Status == "STOPPED" && ts.ExitCode == 0 {
				fmt.Printf("  %s #%-2d %s\n", color.GreenString("~"), ts.WorkItem.ID, ts.WorkItem.Title)
			} else {
				fmt.Printf("  %s #%-2d %s (%s)\n", color.RedString("~"), ts.WorkItem.ID, ts.WorkItem.Title, ts.Status)
				allPassed = false
			}
		}

		if allPassed {
			fmt.Printf("\n  %s Wave %d complete\n\n", color.GreenString("~"), waveIdx)
		} else {
			fmt.Printf("\n  %s Wave %d complete with failures\n\n", color.YellowString("~"), waveIdx)
		}
	}

	// ---- Phase 3: Collect ----
	session.Status = "collecting"
	scrum.SaveSession(session)

	fmt.Printf("%s %s\n\n", color.YellowString("~"), color.CyanString("Phase 3: Collecting Results"))

	printScrumResults(session)

	// Finalize
	session.Status = "done"
	session.CompletedAt = time.Now()

	// Check if any tasks failed
	for _, ts := range session.Tasks {
		if ts.Status != "STOPPED" || ts.ExitCode != 0 {
			session.Status = "done_with_failures"
			break
		}
	}

	scrum.SaveSession(session)

	duration := session.CompletedAt.Sub(session.CreatedAt).Round(time.Second)
	fmt.Printf("\n%s\n\n", color.CyanString("=== Session Complete ==="))
	fmt.Printf("  Session:  %s\n", color.CyanString(sessionID))
	fmt.Printf("  Status:   %s\n", formatScrumStatus(session.Status))
	fmt.Printf("  Duration: %s\n", duration)
	fmt.Printf("  Results:  /workspace/scrum/%s/session.json\n", sessionID)
	fmt.Println()
	fmt.Printf("Review results: %s\n", color.CyanString("frank scrum status %s", sessionID))

	return nil
}

// ============================================================================
// scrum status — Show session status
// ============================================================================

func runScrumStatus(cmd *cobra.Command, args []string) error {
	var sessionID string

	if len(args) > 0 {
		sessionID = args[0]
	} else {
		// Find the most recent session
		sessions, err := scrum.ListSessions()
		if err != nil {
			return err
		}
		if len(sessions) == 0 {
			fmt.Println("No scrum sessions found")
			return nil
		}
		sessionID = sessions[0]
	}

	session, err := scrum.LoadSession(sessionID)
	if err != nil {
		return err
	}

	fmt.Printf("\n%s\n\n", color.CyanString("=== Scrum Session: %s ===", session.ID))
	fmt.Printf("  Profile:  %s\n", session.Profile)
	fmt.Printf("  Goal:     %s\n", session.Goal)
	fmt.Printf("  Status:   %s\n", formatScrumStatus(session.Status))
	fmt.Printf("  Created:  %s\n", session.CreatedAt.Format("2006-01-02 15:04:05"))

	if !session.CompletedAt.IsZero() {
		fmt.Printf("  Completed: %s\n", session.CompletedAt.Format("2006-01-02 15:04:05"))
		fmt.Printf("  Duration:  %s\n", session.CompletedAt.Sub(session.CreatedAt).Round(time.Second))
	}

	if session.Plan != nil {
		fmt.Printf("\n%s\n\n", color.CyanString("Plan"))
		fmt.Printf("  Summary: %s\n", session.Plan.Summary)
		fmt.Printf("  Items:   %d\n\n", len(session.Plan.WorkItems))

		waves := scrum.GetExecutionWaves(session.Plan.WorkItems)
		for i, wave := range waves {
			fmt.Printf("  %s\n", color.CyanString("Wave %d", i))
			for _, item := range wave {
				// Find matching task status
				status := "pending"
				exitCode := -1
				for _, ts := range session.Tasks {
					if ts.WorkItem.ID == item.ID {
						status = ts.Status
						exitCode = ts.ExitCode
						break
					}
				}
				icon := statusIcon(status, exitCode)
				fmt.Printf("    %s #%-2d %s\n", icon, item.ID, item.Title)
			}
		}
	}

	if len(session.Tasks) > 0 {
		fmt.Printf("\n%s\n\n", color.CyanString("Tasks"))
		printScrumResults(session)
	}

	fmt.Println()
	return nil
}

// ============================================================================
// scrum list — List all sessions
// ============================================================================

func runScrumList(cmd *cobra.Command, args []string) error {
	sessions, err := scrum.ListSessions()
	if err != nil {
		return err
	}

	if len(sessions) == 0 {
		fmt.Println("No scrum sessions found")
		return nil
	}

	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"SESSION", "PROFILE", "STATUS", "ITEMS", "CREATED", "GOAL"})
	table.SetBorder(false)
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetCenterSeparator("")
	table.SetColumnSeparator("")
	table.SetRowSeparator("")
	table.SetHeaderLine(false)
	table.SetTablePadding("  ")
	table.SetNoWhiteSpace(true)

	for _, sid := range sessions {
		session, err := scrum.LoadSession(sid)
		if err != nil {
			continue
		}

		items := "0"
		if session.Plan != nil {
			items = fmt.Sprintf("%d", len(session.Plan.WorkItems))
		}

		goal := session.Goal
		if len(goal) > 50 {
			goal = goal[:47] + "..."
		}

		table.Append([]string{
			session.ID,
			session.Profile,
			formatScrumStatus(session.Status),
			items,
			session.CreatedAt.Format("2006-01-02 15:04"),
			goal,
		})
	}

	table.Render()
	return nil
}

// ============================================================================
// Helper functions
// ============================================================================

// buildPlannerPrompt wraps the goal with the decomposition meta-prompt
func buildPlannerPrompt(goal string) string {
	prompt, _ := scrum.BuildDecompositionPrompt(goal)
	return prompt
}

// dispatchScrumTask dispatches a single Codex task for the scrum session.
// It returns the container name and task ID.
func dispatchScrumTask(ctx context.Context, p *profile.Profile, profileName, sessionID, itemLabel, model, taskPrompt string, extraTags []types.Tag) (containerName string, taskID string, err error) {
	client, err := getECSClient(ctx)
	if err != nil {
		return "", "", err
	}

	cfnClient, err := getCFNClient(ctx)
	if err != nil {
		return "", "", err
	}

	codexTaskDefArn, err := getStackOutput(ctx, cfnClient, alb.StackName, "CodexTaskDefinitionArn")
	if err != nil {
		return "", "", fmt.Errorf("failed to find Codex task definition: %w", err)
	}

	descService, err := client.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  aws.String(ecsCluster),
		Services: []string{defaultService},
	})
	if err != nil {
		return "", "", fmt.Errorf("failed to describe service: %w", err)
	}

	if len(descService.Services) == 0 {
		return "", "", fmt.Errorf("service %s not found in cluster %s", defaultService, ecsCluster)
	}

	networkConfig := descService.Services[0].NetworkConfiguration

	containerName = fmt.Sprintf("%s-scrum-%s-%s", profileName, sessionID, itemLabel)

	branch := p.Branch
	if branch == "" {
		branch = "main"
	}

	overrides := &types.TaskOverride{
		ContainerOverrides: []types.ContainerOverride{
			{
				Name: aws.String("codex-worker"),
				Environment: []types.KeyValuePair{
					{Name: aws.String("CONTAINER_NAME"), Value: aws.String(containerName)},
					{Name: aws.String("GIT_REPO"), Value: aws.String(p.Repo)},
					{Name: aws.String("GIT_BRANCH"), Value: aws.String(branch)},
					{Name: aws.String("TASK_PROMPT"), Value: aws.String(taskPrompt)},
					{Name: aws.String("CODEX_MODEL"), Value: aws.String(model)},
				},
			},
		},
	}

	tags := []types.Tag{
		{Key: aws.String("frank-profile"), Value: aws.String(profileName)},
		{Key: aws.String("frank-agent"), Value: aws.String("codex")},
		{Key: aws.String("frank-task-type"), Value: aws.String("headless")},
		{Key: aws.String("frank-scrum-session"), Value: aws.String(sessionID)},
	}
	tags = append(tags, extraTags...)

	runResult, err := client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:              aws.String(ecsCluster),
		TaskDefinition:       aws.String(codexTaskDefArn),
		LaunchType:           types.LaunchTypeFargate,
		NetworkConfiguration: networkConfig,
		Overrides:            overrides,
		Tags:                 tags,
	})
	if err != nil {
		return "", "", fmt.Errorf("failed to run task: %w", err)
	}

	if len(runResult.Tasks) == 0 {
		if len(runResult.Failures) > 0 {
			return "", "", fmt.Errorf("failed to start task: %s - %s",
				aws.ToString(runResult.Failures[0].Reason),
				aws.ToString(runResult.Failures[0].Detail))
		}
		return "", "", fmt.Errorf("failed to start task: no task created")
	}

	task := runResult.Tasks[0]
	taskID = extractTaskID(*task.TaskArn)

	return containerName, taskID, nil
}

// waitForTask polls an ECS task until it reaches STOPPED status.
// Returns the container exit code.
func waitForTask(ctx context.Context, taskID string) (int, error) {
	client, err := getECSClient(ctx)
	if err != nil {
		return -1, err
	}

	pollInterval := 10 * time.Second
	timeout := 30 * time.Minute

	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		descResult, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(ecsCluster),
			Tasks:   []string{taskID},
		})
		if err != nil {
			PrintVerbose("Warning: failed to describe task %s: %v", taskID, err)
			time.Sleep(pollInterval)
			continue
		}

		if len(descResult.Tasks) == 0 {
			return -1, fmt.Errorf("task %s not found", taskID)
		}

		task := descResult.Tasks[0]
		status := strings.ToUpper(aws.ToString(task.LastStatus))

		if status == "STOPPED" {
			if len(task.Containers) > 0 && task.Containers[0].ExitCode != nil {
				return int(aws.ToInt32(task.Containers[0].ExitCode)), nil
			}
			return -1, nil
		}

		time.Sleep(pollInterval)
	}

	return -1, fmt.Errorf("timeout waiting for task %s after %s", taskID, timeout)
}

// waitForWave waits for all tasks in a wave to complete, updating their status in place.
func waitForWave(ctx context.Context, session *scrum.ScrumSession, waveTasks []scrum.TaskStatus) {
	client, err := getECSClient(ctx)
	if err != nil {
		return
	}

	pollInterval := 10 * time.Second
	timeout := 30 * time.Minute
	deadline := time.Now().Add(timeout)

	// Build a set of active task IDs
	activeIDs := make(map[string]int) // taskID -> index in waveTasks
	for i, ts := range waveTasks {
		if ts.TaskID != "" && ts.Status == "RUNNING" {
			activeIDs[ts.TaskID] = i
		}
	}

	for len(activeIDs) > 0 && time.Now().Before(deadline) {
		// Collect task IDs to query
		taskIDs := make([]string, 0, len(activeIDs))
		for tid := range activeIDs {
			taskIDs = append(taskIDs, tid)
		}

		descResult, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
			Cluster: aws.String(ecsCluster),
			Tasks:   taskIDs,
		})
		if err != nil {
			PrintVerbose("Warning: failed to describe tasks: %v", err)
			time.Sleep(pollInterval)
			continue
		}

		// Print live status
		fmt.Printf("\r  ")
		for _, task := range descResult.Tasks {
			tid := extractTaskID(aws.ToString(task.TaskArn))
			status := strings.ToUpper(aws.ToString(task.LastStatus))

			idx, ok := activeIDs[tid]
			if !ok {
				continue
			}

			if status == "STOPPED" {
				exitCode := -1
				if len(task.Containers) > 0 && task.Containers[0].ExitCode != nil {
					exitCode = int(aws.ToInt32(task.Containers[0].ExitCode))
				}

				waveTasks[idx].Status = "STOPPED"
				waveTasks[idx].ExitCode = exitCode
				waveTasks[idx].CompletedAt = time.Now()

				// Update the task in the session
				for si := range session.Tasks {
					if session.Tasks[si].TaskID == tid {
						session.Tasks[si].Status = "STOPPED"
						session.Tasks[si].ExitCode = exitCode
						session.Tasks[si].CompletedAt = time.Now()
						break
					}
				}

				delete(activeIDs, tid)
			}
		}

		if len(activeIDs) > 0 {
			remaining := len(activeIDs)
			fmt.Printf("\r  %s %d task(s) still running...    ",
				color.YellowString("~"), remaining)
			time.Sleep(pollInterval)
		}
	}

	// Mark any remaining tasks as timed out
	for tid, idx := range activeIDs {
		waveTasks[idx].Status = "TIMEOUT"
		for si := range session.Tasks {
			if session.Tasks[si].TaskID == tid {
				session.Tasks[si].Status = "TIMEOUT"
				break
			}
		}
	}

	fmt.Printf("\r%s\r", strings.Repeat(" ", 60)) // Clear the status line
}

// readPlanFromResults reads the planner's output from the EFS results directory
func readPlanFromResults(containerName string) (*scrum.ScrumPlan, error) {
	// Try result.json first (Codex structured output)
	resultPath := filepath.Join("/workspace/results", containerName, "result.json")
	data, err := os.ReadFile(resultPath)
	if err == nil && len(data) > 0 {
		// The result.json may contain the raw Codex output; try to extract the plan
		plan, err := scrum.ParsePlanFromJSON(data)
		if err == nil {
			return plan, nil
		}
		PrintVerbose("result.json didn't parse as plan directly, trying to extract: %v", err)
	}

	// Try summary.json which may have the output
	summaryPath := filepath.Join("/workspace/results", containerName, "summary.json")
	data, err = os.ReadFile(summaryPath)
	if err == nil && len(data) > 0 {
		// summary.json might wrap the output in a metadata envelope
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(data, &envelope); err == nil {
			if output, ok := envelope["output"]; ok {
				plan, err := scrum.ParsePlanFromJSON(output)
				if err == nil {
					return plan, nil
				}
			}
		}
		// Try parsing summary.json directly as a plan
		plan, err := scrum.ParsePlanFromJSON(data)
		if err == nil {
			return plan, nil
		}
	}

	return nil, fmt.Errorf("could not find or parse plan output. Check results at /workspace/results/%s/", containerName)
}

// printScrumResults displays a table of task results
func printScrumResults(session *scrum.ScrumSession) {
	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"#", "TITLE", "STATUS", "EXIT", "CONTAINER", "DURATION"})
	table.SetBorder(false)
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetCenterSeparator("")
	table.SetColumnSeparator("")
	table.SetRowSeparator("")
	table.SetHeaderLine(false)
	table.SetTablePadding("  ")
	table.SetNoWhiteSpace(true)

	for _, ts := range session.Tasks {
		exitStr := "-"
		if ts.Status == "STOPPED" {
			if ts.ExitCode == 0 {
				exitStr = color.GreenString("0")
			} else {
				exitStr = color.RedString("%d", ts.ExitCode)
			}
		}

		duration := "-"
		if !ts.StartedAt.IsZero() {
			end := ts.CompletedAt
			if end.IsZero() {
				end = time.Now()
			}
			duration = end.Sub(ts.StartedAt).Round(time.Second).String()
		}

		statusStr := formatScrumTaskStatus(ts.Status, ts.ExitCode)

		table.Append([]string{
			fmt.Sprintf("%d", ts.WorkItem.ID),
			truncate(ts.WorkItem.Title, 40),
			statusStr,
			exitStr,
			truncate(ts.ContainerName, 30),
			duration,
		})
	}

	table.Render()
}

// formatScrumStatus formats the session status with color
func formatScrumStatus(status string) string {
	switch status {
	case "planning", "dispatching", "running", "collecting":
		return color.YellowString(status)
	case "done":
		return color.GreenString(status)
	case "done_with_failures":
		return color.YellowString("done (with failures)")
	case "failed":
		return color.RedString(status)
	default:
		return status
	}
}

// formatScrumTaskStatus formats a task status with color
func formatScrumTaskStatus(status string, exitCode int) string {
	switch status {
	case "RUNNING", "PENDING":
		return color.YellowString(status)
	case "STOPPED":
		if exitCode == 0 {
			return color.GreenString("DONE")
		}
		return color.RedString("FAILED")
	case "FAILED", "TIMEOUT":
		return color.RedString(status)
	default:
		return status
	}
}

// statusIcon returns a colored icon for a task status
func statusIcon(status string, exitCode int) string {
	switch status {
	case "RUNNING", "PENDING", "pending":
		return color.YellowString("~")
	case "STOPPED":
		if exitCode == 0 {
			return color.GreenString("~")
		}
		return color.RedString("~")
	case "FAILED", "TIMEOUT":
		return color.RedString("~")
	default:
		return " "
	}
}

// truncate shortens a string to maxLen, appending "..." if truncated
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
