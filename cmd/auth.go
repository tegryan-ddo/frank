package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/barff/frank/internal/aws"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage authentication for frank containers",
	Long:  `Configure authentication credentials that will be passed to frank containers.`,
}

var authGitHubCmd = &cobra.Command{
	Use:   "github",
	Short: "Configure GitHub authentication",
	Long: `Configure GitHub authentication for frank containers.

You can provide a GitHub Personal Access Token (PAT) which will be stored
securely and passed to containers automatically.

To create a token:
  1. Go to https://github.com/settings/tokens
  2. Click "Generate new token (classic)"
  3. Select scopes: repo, read:org, workflow
  4. Copy the token

Alternatively, if you're already logged in with 'gh auth login', frank will
automatically mount your GitHub CLI config.`,
	RunE: runAuthGitHub,
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show authentication status",
	RunE:  runAuthStatus,
}

var authClaudeCmd = &cobra.Command{
	Use:   "claude",
	Short: "Configure Claude authentication",
	Long: `Configure Claude authentication for frank containers.

You can provide a Claude access token which will be stored securely and
passed to containers automatically.

To get your token:
  1. Run 'claude' in your terminal
  2. Complete the browser authentication
  3. The token is stored at ~/.claude/.credentials.json

Alternatively, set the CLAUDE_ACCESS_TOKEN environment variable.`,
	RunE: runAuthClaude,
}

var authPnyxCmd = &cobra.Command{
	Use:   "pnyx",
	Short: "Configure Pnyx API key",
	Long: `Configure the Pnyx API key for agent deliberation platform access.

Pnyx is a deliberation platform for AI agents to share patterns,
discuss approaches, and build collective intelligence.

To get your API key:
  1. Visit https://pnyx.digitaldevops.io/agents
  2. Create an agent account
  3. Generate an API key (shown once, format: pnyx_...)

The key will be stored locally and passed to all frank containers.
For ECS containers, sync it to AWS Secrets Manager:
  aws secretsmanager put-secret-value --secret-id /frank/pnyx-api-key --secret-string "pnyx_..."`,
	RunE: runAuthPnyx,
}

var (
	authPnyxToken string
	authPnyxClear bool
)

var authOpenAICmd = &cobra.Command{
	Use:   "openai",
	Short: "Configure OpenAI authentication for Codex workers",
	Long: `Configure the OpenAI API key for Codex worker containers.

This key is used by headless Codex workers dispatched via 'frank ecs dispatch'
and 'frank scrum run'. Usage is billed per-token at standard OpenAI API rates.

To get your API key:
  1. Go to https://platform.openai.com/api-keys
  2. Click "Create new secret key"
  3. Copy the key (starts with sk-)

After storing locally, push to AWS with:
  frank auth push`,
	RunE: runAuthOpenAI,
}

var authPushCmd = &cobra.Command{
	Use:   "push",
	Short: "Push local credentials to AWS Secrets Manager",
	Long: `Push locally stored credentials to AWS Secrets Manager for ECS containers.

This syncs your local auth tokens to the secrets that ECS tasks read at startup.
Only credentials that are configured locally will be pushed.

Secrets updated:
  /frank/github-token       ← frank auth github
  /frank/claude-credentials ← ~/.claude/.credentials.json
  /frank/pnyx-api-key       ← frank auth pnyx
  /frank/openai-api-key     ← frank auth openai`,
	RunE: runAuthPush,
}

var (
	authOpenAIToken string
	authOpenAIClear bool
)

var authAWSCmd = &cobra.Command{
	Use:   "aws [profile]",
	Short: "Generate temporary AWS credentials",
	Long: `Generate temporary AWS credentials from an SSO profile.

This command exports temporary AWS credentials that can be used in environments
that don't support AWS SSO directly. The credentials are short-lived and will
need to be refreshed periodically.

Output formats:
  --format env      Environment variable format (default)
  --format export   Shell export statements (copy/paste ready)
  --format json     JSON format for programmatic use
  --format powershell  PowerShell $env: format

Examples:
  frank auth aws dev                    # Show credentials for 'dev' profile
  frank auth aws dev --format export    # Get export statements
  frank auth aws dev --format json      # Get JSON output
  eval $(frank auth aws dev --format export)  # Set in current shell`,
	Args: cobra.MaximumNArgs(1),
	RunE: runAuthAWS,
}

var (
	authGitHubToken  string
	authGitHubClear  bool
	authClaudeToken  string
	authClaudeClear  bool
	authAWSFormat    string
	authAWSLogin     bool
)

func init() {
	rootCmd.AddCommand(authCmd)
	authCmd.AddCommand(authGitHubCmd)
	authCmd.AddCommand(authClaudeCmd)
	authCmd.AddCommand(authStatusCmd)
	authCmd.AddCommand(authAWSCmd)
	authCmd.AddCommand(authPnyxCmd)
	authCmd.AddCommand(authOpenAICmd)
	authCmd.AddCommand(authPushCmd)

	authGitHubCmd.Flags().StringVarP(&authGitHubToken, "token", "t", "", "GitHub Personal Access Token")
	authGitHubCmd.Flags().BoolVar(&authGitHubClear, "clear", false, "Clear stored GitHub token")

	authClaudeCmd.Flags().StringVarP(&authClaudeToken, "token", "t", "", "Claude access token")
	authClaudeCmd.Flags().BoolVar(&authClaudeClear, "clear", false, "Clear stored Claude token")

	authAWSCmd.Flags().StringVar(&authAWSFormat, "format", "env", "Output format: env, export, json, powershell")
	authAWSCmd.Flags().BoolVar(&authAWSLogin, "login", false, "Perform SSO login if credentials are expired")

	authPnyxCmd.Flags().StringVarP(&authPnyxToken, "token", "t", "", "Pnyx API key")
	authPnyxCmd.Flags().BoolVar(&authPnyxClear, "clear", false, "Clear stored Pnyx API key")

	authOpenAICmd.Flags().StringVarP(&authOpenAIToken, "token", "t", "", "OpenAI API key")
	authOpenAICmd.Flags().BoolVar(&authOpenAIClear, "clear", false, "Clear stored OpenAI API key")
}

func runAuthGitHub(cmd *cobra.Command, args []string) error {
	tokenFile := getAuthTokenFile("github")

	if authGitHubClear {
		if err := os.Remove(tokenFile); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to clear token: %w", err)
		}
		fmt.Println("GitHub token cleared.")
		return nil
	}

	token := authGitHubToken

	// If no token provided, prompt for it
	if token == "" {
		// Check if already set via environment
		if envToken := os.Getenv("GH_TOKEN"); envToken != "" {
			fmt.Println("GH_TOKEN environment variable is already set.")
			fmt.Print("Store it for future sessions? [y/N]: ")
			reader := bufio.NewReader(os.Stdin)
			response, _ := reader.ReadString('\n')
			if strings.TrimSpace(strings.ToLower(response)) == "y" {
				token = envToken
			} else {
				return nil
			}
		} else if envToken := os.Getenv("GITHUB_TOKEN"); envToken != "" {
			fmt.Println("GITHUB_TOKEN environment variable is already set.")
			fmt.Print("Store it for future sessions? [y/N]: ")
			reader := bufio.NewReader(os.Stdin)
			response, _ := reader.ReadString('\n')
			if strings.TrimSpace(strings.ToLower(response)) == "y" {
				token = envToken
			} else {
				return nil
			}
		} else {
			fmt.Println("Enter your GitHub Personal Access Token:")
			fmt.Println("(Create one at https://github.com/settings/tokens)")
			fmt.Print("> ")
			reader := bufio.NewReader(os.Stdin)
			token, _ = reader.ReadString('\n')
			token = strings.TrimSpace(token)
		}
	}

	if token == "" {
		return fmt.Errorf("no token provided")
	}

	// Validate token format (basic check)
	if !strings.HasPrefix(token, "ghp_") && !strings.HasPrefix(token, "github_pat_") && !strings.HasPrefix(token, "gho_") {
		fmt.Println(color.YellowString("Warning: Token doesn't match expected GitHub token format."))
		fmt.Println("Expected prefixes: ghp_, github_pat_, or gho_")
	}

	// Store token
	if err := os.MkdirAll(filepath.Dir(tokenFile), 0700); err != nil {
		return fmt.Errorf("failed to create auth directory: %w", err)
	}

	if err := os.WriteFile(tokenFile, []byte(token), 0600); err != nil {
		return fmt.Errorf("failed to store token: %w", err)
	}

	fmt.Printf("%s GitHub token stored successfully.\n", color.GreenString("✓"))
	fmt.Println("This token will be passed to all frank containers.")
	return nil
}

func runAuthClaude(cmd *cobra.Command, args []string) error {
	tokenFile := getAuthTokenFile("claude")

	if authClaudeClear {
		if err := os.Remove(tokenFile); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to clear token: %w", err)
		}
		fmt.Println("Claude token cleared.")
		return nil
	}

	token := authClaudeToken

	// If no token provided, try to find it or prompt
	if token == "" {
		// Check environment variable
		if envToken := os.Getenv("CLAUDE_ACCESS_TOKEN"); envToken != "" {
			fmt.Println("CLAUDE_ACCESS_TOKEN environment variable is set.")
			fmt.Print("Store it for future sessions? [y/N]: ")
			reader := bufio.NewReader(os.Stdin)
			response, _ := reader.ReadString('\n')
			if strings.TrimSpace(strings.ToLower(response)) == "y" {
				token = envToken
			} else {
				return nil
			}
		} else if credToken := getClaudeCredentialsToken(); credToken != "" {
			// Found token in Claude's credentials file
			fmt.Println("Found token in Claude credentials file.")
			fmt.Print("Store it for frank? [y/N]: ")
			reader := bufio.NewReader(os.Stdin)
			response, _ := reader.ReadString('\n')
			if strings.TrimSpace(strings.ToLower(response)) == "y" {
				token = credToken
			} else {
				return nil
			}
		} else {
			fmt.Println("Enter your Claude access token:")
			fmt.Println("(Run 'claude' to authenticate and get your token from ~/.claude/.credentials.json)")
			fmt.Print("> ")
			reader := bufio.NewReader(os.Stdin)
			token, _ = reader.ReadString('\n')
			token = strings.TrimSpace(token)
		}
	}

	if token == "" {
		return fmt.Errorf("no token provided")
	}

	// Store token
	if err := os.MkdirAll(filepath.Dir(tokenFile), 0700); err != nil {
		return fmt.Errorf("failed to create auth directory: %w", err)
	}

	if err := os.WriteFile(tokenFile, []byte(token), 0600); err != nil {
		return fmt.Errorf("failed to store token: %w", err)
	}

	fmt.Printf("%s Claude token stored successfully.\n", color.GreenString("✓"))
	fmt.Println("This token will be passed to all frank containers.")
	return nil
}

func runAuthStatus(cmd *cobra.Command, args []string) error {
	fmt.Println("Authentication Status:")
	fmt.Println()

	// Check Claude
	fmt.Print("Claude: ")
	if token := getStoredClaudeToken(); token != "" {
		masked := maskToken(token)
		fmt.Printf("%s (stored: %s)\n", color.GreenString("configured"), masked)
	} else if token := os.Getenv("CLAUDE_ACCESS_TOKEN"); token != "" {
		fmt.Printf("%s (from CLAUDE_ACCESS_TOKEN env)\n", color.GreenString("configured"))
	} else if token := getClaudeCredentialsToken(); token != "" {
		fmt.Printf("%s (from ~/.claude credentials)\n", color.GreenString("configured"))
	} else {
		fmt.Printf("%s\n", color.YellowString("not configured"))
	}

	// Check GitHub
	fmt.Print("GitHub: ")
	if token := getStoredGitHubToken(); token != "" {
		masked := maskToken(token)
		fmt.Printf("%s (stored: %s)\n", color.GreenString("configured"), masked)
	} else if token := os.Getenv("GH_TOKEN"); token != "" {
		fmt.Printf("%s (from GH_TOKEN env)\n", color.GreenString("configured"))
	} else if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		fmt.Printf("%s (from GITHUB_TOKEN env)\n", color.GreenString("configured"))
	} else if ghConfigExists() {
		fmt.Printf("%s (gh CLI config available)\n", color.GreenString("configured"))
	} else {
		fmt.Printf("%s\n", color.YellowString("not configured"))
	}

	// Check Pnyx
	fmt.Print("Pnyx:   ")
	if token := getStoredPnyxToken(); token != "" {
		masked := maskToken(token)
		fmt.Printf("%s (stored: %s)\n", color.GreenString("configured"), masked)
	} else if token := os.Getenv("PNYX_API_KEY"); token != "" {
		fmt.Printf("%s (from PNYX_API_KEY env)\n", color.GreenString("configured"))
	} else {
		fmt.Printf("%s\n", color.YellowString("not configured"))
	}

	// Check OpenAI
	fmt.Print("OpenAI: ")
	if token := getStoredOpenAIToken(); token != "" {
		masked := maskToken(token)
		fmt.Printf("%s (stored: %s)\n", color.GreenString("configured"), masked)
	} else if token := os.Getenv("OPENAI_API_KEY"); token != "" {
		fmt.Printf("%s (from OPENAI_API_KEY env)\n", color.GreenString("configured"))
	} else {
		fmt.Printf("%s\n", color.YellowString("not configured"))
	}

	// Check SSH
	fmt.Print("SSH:    ")
	if sshKeyExists() {
		fmt.Printf("%s (keys found in ~/.ssh)\n", color.GreenString("available"))
	} else {
		fmt.Printf("%s\n", color.YellowString("no keys found"))
	}

	// Check AWS
	fmt.Print("AWS:    ")
	if awsConfigExists() {
		fmt.Printf("%s (~/.aws found)\n", color.GreenString("available"))
	} else {
		fmt.Printf("%s\n", color.YellowString("not configured"))
	}

	fmt.Println()
	return nil
}

// getAuthTokenFile returns the path to store auth tokens
func getAuthTokenFile(service string) string {
	return filepath.Join(getHomeDir(), ".config", "frank", "auth", service+".token")
}

// getStoredGitHubToken reads the stored GitHub token
func getStoredGitHubToken() string {
	data, err := os.ReadFile(getAuthTokenFile("github"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// getStoredClaudeToken reads the stored Claude token
func getStoredClaudeToken() string {
	data, err := os.ReadFile(getAuthTokenFile("claude"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// getClaudeCredentialsToken reads Claude's credentials file
func getClaudeCredentialsToken() string {
	credFile := filepath.Join(getHomeDir(), ".claude", ".credentials.json")
	data, err := os.ReadFile(credFile)
	if err != nil {
		return ""
	}

	// Try the nested claudeAiOauth structure first (current format)
	var nestedCreds struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &nestedCreds); err == nil && nestedCreds.ClaudeAiOauth.AccessToken != "" {
		return nestedCreds.ClaudeAiOauth.AccessToken
	}

	// Fallback to flat structure (older format)
	var flatCreds struct {
		AccessToken string `json:"accessToken"`
		Token       string `json:"token"`
	}
	if err := json.Unmarshal(data, &flatCreds); err == nil {
		if flatCreds.AccessToken != "" {
			return flatCreds.AccessToken
		}
		return flatCreds.Token
	}

	return ""
}

// maskToken returns a masked version of a token for display
func maskToken(token string) string {
	if len(token) < 8 {
		return "***"
	}
	return token[:4] + "..." + token[len(token)-4:]
}

// GetClaudeToken returns the Claude token from stored, env, or credentials
func GetClaudeToken() string {
	// Priority: stored token > CLAUDE_ACCESS_TOKEN > credentials file
	if token := getStoredClaudeToken(); token != "" {
		return token
	}
	if token := os.Getenv("CLAUDE_ACCESS_TOKEN"); token != "" {
		return token
	}
	return getClaudeCredentialsToken()
}

// ghConfigExists checks if GitHub CLI config exists
func ghConfigExists() bool {
	ghConfigDir := filepath.Join(getHomeDir(), ".config", "gh")
	if _, err := os.Stat(filepath.Join(ghConfigDir, "hosts.yml")); err == nil {
		return true
	}
	return false
}

// sshKeyExists checks if SSH keys exist
func sshKeyExists() bool {
	sshDir := filepath.Join(getHomeDir(), ".ssh")
	keys := []string{"id_rsa", "id_ed25519", "id_ecdsa"}
	for _, key := range keys {
		if _, err := os.Stat(filepath.Join(sshDir, key)); err == nil {
			return true
		}
	}
	return false
}

// awsConfigExists checks if AWS config exists
func awsConfigExists() bool {
	awsDir := filepath.Join(getHomeDir(), ".aws")
	if _, err := os.Stat(awsDir); err == nil {
		return true
	}
	return false
}

// GetGitHubToken returns the GitHub token from stored or environment
func GetGitHubToken() string {
	// Priority: stored token > GH_TOKEN > GITHUB_TOKEN
	if token := getStoredGitHubToken(); token != "" {
		return token
	}
	if token := os.Getenv("GH_TOKEN"); token != "" {
		return token
	}
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return token
	}
	return ""
}

// GetSSHDir returns the SSH directory path if it exists
func GetSSHDir() string {
	sshDir := filepath.Join(getHomeDir(), ".ssh")
	if _, err := os.Stat(sshDir); err == nil {
		return sshDir
	}
	return ""
}

// GetGHConfigDir returns the GitHub CLI config directory if it exists
func GetGHConfigDir() string {
	ghDir := filepath.Join(getHomeDir(), ".config", "gh")
	if _, err := os.Stat(filepath.Join(ghDir, "hosts.yml")); err == nil {
		return ghDir
	}
	return ""
}

func runAuthPnyx(cmd *cobra.Command, args []string) error {
	tokenFile := getAuthTokenFile("pnyx")

	if authPnyxClear {
		if err := os.Remove(tokenFile); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to clear API key: %w", err)
		}
		fmt.Println("Pnyx API key cleared.")
		return nil
	}

	token := authPnyxToken

	// If no token provided, check env or prompt
	if token == "" {
		if envToken := os.Getenv("PNYX_API_KEY"); envToken != "" {
			fmt.Println("PNYX_API_KEY environment variable is already set.")
			fmt.Print("Store it for future sessions? [y/N]: ")
			reader := bufio.NewReader(os.Stdin)
			response, _ := reader.ReadString('\n')
			if strings.TrimSpace(strings.ToLower(response)) == "y" {
				token = envToken
			} else {
				return nil
			}
		} else {
			fmt.Println("Enter your Pnyx API key:")
			fmt.Println("(Get one at https://pnyx.digitaldevops.io/agents)")
			fmt.Print("> ")
			reader := bufio.NewReader(os.Stdin)
			token, _ = reader.ReadString('\n')
			token = strings.TrimSpace(token)
		}
	}

	if token == "" {
		return fmt.Errorf("no API key provided")
	}

	// Validate token format
	if !strings.HasPrefix(token, "pnyx_") {
		fmt.Println(color.YellowString("Warning: API key doesn't match expected format (pnyx_...)."))
	}

	// Store token
	if err := os.MkdirAll(filepath.Dir(tokenFile), 0700); err != nil {
		return fmt.Errorf("failed to create auth directory: %w", err)
	}

	if err := os.WriteFile(tokenFile, []byte(token), 0600); err != nil {
		return fmt.Errorf("failed to store API key: %w", err)
	}

	fmt.Printf("%s Pnyx API key stored successfully.\n", color.GreenString("✓"))
	fmt.Println("This key will be passed to all frank containers.")
	fmt.Println()
	fmt.Println("To sync to ECS containers, run:")
	fmt.Println("  aws secretsmanager put-secret-value --secret-id /frank/pnyx-api-key --secret-string \"" + maskToken(token) + "\"")
	return nil
}

// getStoredPnyxToken reads the stored Pnyx API key
func getStoredPnyxToken() string {
	data, err := os.ReadFile(getAuthTokenFile("pnyx"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// GetPnyxToken returns the Pnyx API key from stored or environment
func GetPnyxToken() string {
	if token := getStoredPnyxToken(); token != "" {
		return token
	}
	if token := os.Getenv("PNYX_API_KEY"); token != "" {
		return token
	}
	return ""
}

func runAuthOpenAI(cmd *cobra.Command, args []string) error {
	tokenFile := getAuthTokenFile("openai")

	if authOpenAIClear {
		if err := os.Remove(tokenFile); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to clear API key: %w", err)
		}
		fmt.Println("OpenAI API key cleared.")
		return nil
	}

	token := authOpenAIToken

	// If no token provided, check env or prompt
	if token == "" {
		if envToken := os.Getenv("OPENAI_API_KEY"); envToken != "" {
			fmt.Println("OPENAI_API_KEY environment variable is already set.")
			fmt.Print("Store it for future sessions? [y/N]: ")
			reader := bufio.NewReader(os.Stdin)
			response, _ := reader.ReadString('\n')
			if strings.TrimSpace(strings.ToLower(response)) == "y" {
				token = envToken
			} else {
				return nil
			}
		} else {
			fmt.Println("Enter your OpenAI API key:")
			fmt.Println("(Get one at https://platform.openai.com/api-keys)")
			fmt.Print("> ")
			reader := bufio.NewReader(os.Stdin)
			token, _ = reader.ReadString('\n')
			token = strings.TrimSpace(token)
		}
	}

	if token == "" {
		return fmt.Errorf("no API key provided")
	}

	// Validate token format
	if !strings.HasPrefix(token, "sk-") {
		fmt.Println(color.YellowString("Warning: API key doesn't match expected format (sk-...)."))
	}

	// Store token
	if err := os.MkdirAll(filepath.Dir(tokenFile), 0700); err != nil {
		return fmt.Errorf("failed to create auth directory: %w", err)
	}

	if err := os.WriteFile(tokenFile, []byte(token), 0600); err != nil {
		return fmt.Errorf("failed to store API key: %w", err)
	}

	fmt.Printf("%s OpenAI API key stored successfully.\n", color.GreenString("✓"))
	fmt.Println("This key will be used by Codex workers.")
	fmt.Println()
	fmt.Println("Push to ECS with:")
	fmt.Printf("  frank auth push\n")
	return nil
}

// getStoredOpenAIToken reads the stored OpenAI API key
func getStoredOpenAIToken() string {
	data, err := os.ReadFile(getAuthTokenFile("openai"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// GetOpenAIToken returns the OpenAI API key from stored or environment
func GetOpenAIToken() string {
	if token := getStoredOpenAIToken(); token != "" {
		return token
	}
	if token := os.Getenv("OPENAI_API_KEY"); token != "" {
		return token
	}
	return ""
}

func runAuthPush(cmd *cobra.Command, args []string) error {
	fmt.Printf("%s Pushing credentials to AWS Secrets Manager...\n\n", color.CyanString("~"))

	type secretPush struct {
		name     string
		secretID string
		value    string
		source   string
	}

	var pushes []secretPush

	// GitHub token
	if token := GetGitHubToken(); token != "" {
		pushes = append(pushes, secretPush{
			name:     "GitHub",
			secretID: "/frank/github-token",
			value:    token,
			source:   "frank auth",
		})
	}

	// Claude credentials — push the full credentials JSON file, not just the token
	claudeCredFile := filepath.Join(getHomeDir(), ".claude", ".credentials.json")
	if data, err := os.ReadFile(claudeCredFile); err == nil && len(data) > 0 {
		pushes = append(pushes, secretPush{
			name:     "Claude",
			secretID: "/frank/claude-credentials",
			value:    string(data),
			source:   "~/.claude/.credentials.json",
		})
	} else if token := GetClaudeToken(); token != "" {
		// Fallback: wrap the token in a JSON envelope
		envelope := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"%s"}}`, token)
		pushes = append(pushes, secretPush{
			name:     "Claude",
			secretID: "/frank/claude-credentials",
			value:    envelope,
			source:   "frank auth",
		})
	}

	// Pnyx API key
	if token := GetPnyxToken(); token != "" {
		pushes = append(pushes, secretPush{
			name:     "Pnyx",
			secretID: "/frank/pnyx-api-key",
			value:    token,
			source:   "frank auth",
		})
	}

	// OpenAI API key
	if token := GetOpenAIToken(); token != "" {
		pushes = append(pushes, secretPush{
			name:     "OpenAI",
			secretID: "/frank/openai-api-key",
			value:    token,
			source:   "frank auth",
		})
	}

	if len(pushes) == 0 {
		fmt.Println("No credentials configured. Use 'frank auth <service>' to set up credentials.")
		return nil
	}

	// Push each credential
	ssoManager := aws.NewSSOManager()
	succeeded := 0
	failed := 0

	for _, p := range pushes {
		fmt.Printf("  %-10s → %s ", p.name, p.secretID)
		err := ssoManager.PutSecretValue(p.secretID, p.value)
		if err != nil {
			fmt.Printf("%s (%v)\n", color.RedString("FAILED"), err)
			failed++
		} else {
			fmt.Printf("%s\n", color.GreenString("OK"))
			succeeded++
		}
	}

	fmt.Println()
	if failed == 0 {
		fmt.Printf("%s All %d credentials pushed successfully.\n", color.GreenString("✓"), succeeded)
	} else {
		fmt.Printf("%s %d succeeded, %d failed.\n", color.YellowString("~"), succeeded, failed)
	}

	return nil
}

func runAuthAWS(cmd *cobra.Command, args []string) error {
	ssoManager := aws.NewSSOManager()

	// If no profile specified, list available profiles
	if len(args) == 0 {
		profiles, err := ssoManager.ListProfiles()
		if err != nil {
			return fmt.Errorf("failed to list profiles: %w", err)
		}

		if len(profiles) == 0 {
			fmt.Println("No AWS profiles found.")
			fmt.Println("Configure profiles in ~/.aws/config")
			return nil
		}

		fmt.Println("Available AWS profiles:")
		fmt.Println()
		for _, p := range profiles {
			valid, expiresAt, _ := ssoManager.CheckCredentials(p.Name)
			if valid {
				fmt.Printf("  %s %s (expires: %s)\n",
					color.GreenString("●"),
					p.Name,
					expiresAt.Format("15:04:05"))
			} else {
				fmt.Printf("  %s %s (not logged in)\n",
					color.YellowString("○"),
					p.Name)
			}
		}
		fmt.Println()
		fmt.Println("Usage: frank auth aws <profile> [--format env|export|json|powershell]")
		return nil
	}

	profile := args[0]

	// Check credentials and optionally login
	valid, _, _ := ssoManager.CheckCredentials(profile)
	if !valid {
		if authAWSLogin {
			fmt.Fprintf(os.Stderr, "Credentials expired, logging in...\n")
			if err := ssoManager.Login(profile); err != nil {
				return fmt.Errorf("SSO login failed: %w", err)
			}
		} else {
			fmt.Fprintf(os.Stderr, "%s Credentials expired for profile '%s'\n", color.RedString("✗"), profile)
			fmt.Fprintf(os.Stderr, "Run: frank auth aws %s --login\n", profile)
			return fmt.Errorf("credentials expired")
		}
	}

	// Get credentials
	creds, err := ssoManager.GetCredentials(profile)
	if err != nil {
		return fmt.Errorf("failed to get credentials: %w", err)
	}

	// Output in requested format
	switch authAWSFormat {
	case "env":
		fmt.Printf("AWS_ACCESS_KEY_ID=%s\n", creds.AccessKeyID)
		fmt.Printf("AWS_SECRET_ACCESS_KEY=%s\n", creds.SecretAccessKey)
		if creds.SessionToken != "" {
			fmt.Printf("AWS_SESSION_TOKEN=%s\n", creds.SessionToken)
		}
		if creds.Region != "" {
			fmt.Printf("AWS_DEFAULT_REGION=%s\n", creds.Region)
			fmt.Printf("AWS_REGION=%s\n", creds.Region)
		}

	case "export":
		fmt.Printf("export AWS_ACCESS_KEY_ID=\"%s\"\n", creds.AccessKeyID)
		fmt.Printf("export AWS_SECRET_ACCESS_KEY=\"%s\"\n", creds.SecretAccessKey)
		if creds.SessionToken != "" {
			fmt.Printf("export AWS_SESSION_TOKEN=\"%s\"\n", creds.SessionToken)
		}
		if creds.Region != "" {
			fmt.Printf("export AWS_DEFAULT_REGION=\"%s\"\n", creds.Region)
			fmt.Printf("export AWS_REGION=\"%s\"\n", creds.Region)
		}

	case "powershell":
		fmt.Printf("$env:AWS_ACCESS_KEY_ID=\"%s\"\n", creds.AccessKeyID)
		fmt.Printf("$env:AWS_SECRET_ACCESS_KEY=\"%s\"\n", creds.SecretAccessKey)
		if creds.SessionToken != "" {
			fmt.Printf("$env:AWS_SESSION_TOKEN=\"%s\"\n", creds.SessionToken)
		}
		if creds.Region != "" {
			fmt.Printf("$env:AWS_DEFAULT_REGION=\"%s\"\n", creds.Region)
			fmt.Printf("$env:AWS_REGION=\"%s\"\n", creds.Region)
		}

	case "json":
		output := map[string]interface{}{
			"AccessKeyId":     creds.AccessKeyID,
			"SecretAccessKey": creds.SecretAccessKey,
			"Version":         1,
		}
		if creds.SessionToken != "" {
			output["SessionToken"] = creds.SessionToken
		}
		if creds.Region != "" {
			output["Region"] = creds.Region
		}
		if !creds.Expiration.IsZero() {
			output["Expiration"] = creds.Expiration.Format("2006-01-02T15:04:05Z")
		}
		jsonBytes, _ := json.MarshalIndent(output, "", "  ")
		fmt.Println(string(jsonBytes))

	default:
		return fmt.Errorf("unknown format: %s (use: env, export, json, powershell)", authAWSFormat)
	}

	return nil
}
