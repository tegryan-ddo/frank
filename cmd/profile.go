package cmd

import (
	"fmt"
	"os"
	"sort"

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
as ECS tasks with their own subdomains.

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
)

func init() {
	rootCmd.AddCommand(profileCmd)

	// Add subcommands
	profileCmd.AddCommand(profileListCmd)
	profileCmd.AddCommand(profileAddCmd)
	profileCmd.AddCommand(profileShowCmd)
	profileCmd.AddCommand(profileRemoveCmd)

	// Add command flags
	profileAddCmd.Flags().StringVarP(&profileAddRepo, "repo", "r", "", "Git repository URL (required)")
	profileAddCmd.Flags().StringVarP(&profileAddBranch, "branch", "b", "main", "Git branch")
	profileAddCmd.Flags().StringVarP(&profileAddDescription, "description", "d", "", "Profile description")
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

The profile name will be used as the subdomain: <name>.frank.digitaldevops.io`,
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
	fmt.Println()
	fmt.Printf("Start with: frank ecs start %s\n", name)
	fmt.Printf("URL will be: https://%s.frank.digitaldevops.io/claude/\n", name)

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
	fmt.Println()
	fmt.Printf("  URL:         https://%s.frank.digitaldevops.io/claude/\n", name)
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
