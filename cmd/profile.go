package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/barff/frank/internal/profile"
	"github.com/fatih/color"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

var profileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Manage Frank ECS profiles",
	Long: `Manage profiles for Frank ECS deployments.

Profiles define repository configurations that can be quickly launched
as ECS tasks with their own URL paths.

Examples:
  frank profile list                           # List all profiles
  frank profile add enkai --repo https://github.com/org/enkai.git
  frank profile show enkai                     # Show profile details
  frank profile remove enkai                   # Remove a profile`,
}

// Flags for profile add
var (
	profileAddRepo        string
	profileAddBranch      string
	profileAddDescription string
	profileAddAgent       string
	profileAddMode        string
	profileAddTask        string
	profileAddModel       string
)

// SSM parameter name for profiles
const ssmProfilesParam = "/frank/profiles"

func init() {
	rootCmd.AddCommand(profileCmd)

	// Add subcommands
	profileCmd.AddCommand(profileListCmd)
	profileCmd.AddCommand(profileAddCmd)
	profileCmd.AddCommand(profileShowCmd)
	profileCmd.AddCommand(profileRemoveCmd)
	profileCmd.AddCommand(profileSyncCmd)

	// Add command flags
	profileAddCmd.Flags().StringVarP(&profileAddRepo, "repo", "r", "", "Git repository URL (required)")
	profileAddCmd.Flags().StringVarP(&profileAddBranch, "branch", "b", "main", "Git branch")
	profileAddCmd.Flags().StringVarP(&profileAddDescription, "description", "d", "", "Profile description")
	profileAddCmd.Flags().StringVar(&profileAddAgent, "agent", "claude", "AI agent backend (claude, codex)")
	profileAddCmd.Flags().StringVar(&profileAddMode, "mode", "interactive", "Execution mode (interactive, headless)")
	profileAddCmd.Flags().StringVar(&profileAddTask, "task", "", "Task prompt for headless mode")
	profileAddCmd.Flags().StringVar(&profileAddModel, "model", "", "Model override (e.g. codex-mini, gpt-5.2-codex)")
	profileAddCmd.MarkFlagRequired("repo")
}

// ============================================================================
// profile list - List all profiles
// ============================================================================

var profileListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all profiles",
	Long:  `List all configured Frank profiles.`,
	RunE:  runProfileList,
}

func runProfileList(cmd *cobra.Command, args []string) error {
	config, err := profile.LoadProfiles()
	if err != nil {
		return fmt.Errorf("failed to load profiles: %w", err)
	}

	if len(config.Profiles) == 0 {
		fmt.Println("No profiles configured.")
		fmt.Printf("\nAdd a profile with: frank profile add <name> --repo <url>\n")
		fmt.Printf("Profiles are stored in: %s\n", profile.GetProfilesPath())
		return nil
	}

	// Sort profile names
	names := make([]string, 0, len(config.Profiles))
	for name := range config.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)

	// Display as table
	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"PROFILE", "REPO", "BRANCH", "DESCRIPTION"})
	table.SetBorder(false)
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetCenterSeparator("")
	table.SetColumnSeparator("")
	table.SetRowSeparator("")
	table.SetHeaderLine(false)
	table.SetTablePadding("  ")
	table.SetNoWhiteSpace(true)

	for _, name := range names {
		p := config.Profiles[name]
		branch := p.Branch
		if branch == "" {
			branch = "main"
		}
		// Truncate repo URL for display
		repo := p.Repo
		if len(repo) > 50 {
			repo = "..." + repo[len(repo)-47:]
		}
		table.Append([]string{name, repo, branch, p.Description})
	}

	table.Render()
	return nil
}

// ============================================================================
// profile add - Add a new profile
// ============================================================================

var profileAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add a new profile",
	Long: `Add a new Frank profile with the specified repository configuration.

The profile name will be used in the URL path: frank.digitaldevops.io/<name>/`,
	Args: cobra.ExactArgs(1),
	RunE: runProfileAdd,
}

func runProfileAdd(cmd *cobra.Command, args []string) error {
	name := args[0]

	// Check if profile already exists
	existing, _ := profile.GetProfile(name)
	if existing != nil {
		fmt.Printf("Profile %q already exists. Updating...\n", name)
	}

	p := &profile.Profile{
		Name:        name,
		Repo:        profileAddRepo,
		Branch:      profileAddBranch,
		Description: profileAddDescription,
		Agent:       profileAddAgent,
		Mode:        profileAddMode,
		Task:        profileAddTask,
		Model:       profileAddModel,
	}

	if err := profile.AddProfile(p); err != nil {
		return fmt.Errorf("failed to add profile: %w", err)
	}

	fmt.Printf("%s Profile %q saved\n\n", color.GreenString("✓"), name)
	fmt.Printf("  Repo:        %s\n", p.Repo)
	fmt.Printf("  Branch:      %s\n", p.Branch)
	if p.Description != "" {
		fmt.Printf("  Description: %s\n", p.Description)
	}
	fmt.Printf("  Agent:       %s\n", p.GetAgent())
	fmt.Printf("  Mode:        %s\n", p.GetMode())
	if p.Task != "" {
		fmt.Printf("  Task:        %s\n", p.Task)
	}
	if p.Model != "" {
		fmt.Printf("  Model:       %s\n", p.Model)
	}
	fmt.Println()
	fmt.Printf("Start with: frank ecs start %s\n", name)
	fmt.Printf("URL will be: https://frank.digitaldevops.io/%s/\n", name)

	return nil
}

// ============================================================================
// profile show - Show profile details
// ============================================================================

var profileShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show profile details",
	Long:  `Show detailed information about a profile.`,
	Args:  cobra.ExactArgs(1),
	RunE:  runProfileShow,
}

func runProfileShow(cmd *cobra.Command, args []string) error {
	name := args[0]

	p, err := profile.GetProfile(name)
	if err != nil {
		return err
	}

	fmt.Printf("\n%s Profile: %s\n\n", color.CyanString("●"), color.CyanString(name))
	fmt.Printf("  Repository:  %s\n", p.Repo)
	fmt.Printf("  Branch:      %s\n", p.Branch)
	if p.Description != "" {
		fmt.Printf("  Description: %s\n", p.Description)
	}
	fmt.Printf("  Agent:       %s\n", p.GetAgent())
	fmt.Printf("  Mode:        %s\n", p.GetMode())
	if p.Task != "" {
		fmt.Printf("  Task:        %s\n", p.Task)
	}
	if p.Model != "" {
		fmt.Printf("  Model:       %s\n", p.Model)
	}
	fmt.Println()
	fmt.Printf("  URL:         https://frank.digitaldevops.io/%s/\n", name)
	fmt.Println()

	return nil
}

// ============================================================================
// profile remove - Remove a profile
// ============================================================================

var profileRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a profile",
	Long:  `Remove a profile from the configuration.`,
	Args:  cobra.ExactArgs(1),
	RunE:  runProfileRemove,
}

func runProfileRemove(cmd *cobra.Command, args []string) error {
	name := args[0]

	if err := profile.RemoveProfile(name); err != nil {
		return err
	}

	fmt.Printf("%s Profile %q removed\n", color.GreenString("✓"), name)
	return nil
}

// ============================================================================
// profile sync - Sync profiles to AWS SSM Parameter Store
// ============================================================================

var profileSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync profiles to AWS SSM Parameter Store",
	Long: `Sync local profiles to AWS SSM Parameter Store for the launch page.

The web-based launch page reads profiles from SSM Parameter Store.
This command uploads your local profiles to AWS so they appear on the web UI.`,
	RunE: runProfileSync,
}

func runProfileSync(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// Load local profiles
	cfg, err := profile.LoadProfiles()
	if err != nil {
		return fmt.Errorf("failed to load profiles: %w", err)
	}

	if len(cfg.Profiles) == 0 {
		fmt.Println("No profiles to sync.")
		return nil
	}

	// Convert to JSON array format for Lambda
	type ssmProfile struct {
		Name        string `json:"name"`
		Repo        string `json:"repo"`
		Branch      string `json:"branch,omitempty"`
		Description string `json:"description,omitempty"`
	}

	profiles := make([]ssmProfile, 0, len(cfg.Profiles))
	for name, p := range cfg.Profiles {
		profiles = append(profiles, ssmProfile{
			Name:        name,
			Repo:        p.Repo,
			Branch:      p.Branch,
			Description: p.Description,
		})
	}

	jsonData, err := json.Marshal(profiles)
	if err != nil {
		return fmt.Errorf("failed to marshal profiles: %w", err)
	}

	// Load AWS config
	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to load AWS config: %w", err)
	}

	// Update SSM parameter
	ssmClient := ssm.NewFromConfig(awsCfg)
	_, err = ssmClient.PutParameter(ctx, &ssm.PutParameterInput{
		Name:      aws.String(ssmProfilesParam),
		Value:     aws.String(string(jsonData)),
		Type:      "String",
		Overwrite: aws.Bool(true),
	})
	if err != nil {
		return fmt.Errorf("failed to update SSM parameter: %w", err)
	}

	fmt.Printf("%s Synced %d profile(s) to AWS SSM\n", color.GreenString("✓"), len(profiles))
	fmt.Printf("  Parameter: %s\n", ssmProfilesParam)
	fmt.Println()
	fmt.Println("Profiles are now available on the launch page.")

	return nil
}
