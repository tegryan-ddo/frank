package aws

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Credentials represents AWS credentials
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Expiration      time.Time
	Region          string
}

// Profile represents an AWS profile
type Profile struct {
	Name      string
	SSORegion string
	AccountID string
	RoleName  string
	Region    string
}

// SSOManager manages AWS SSO credentials
type SSOManager struct {
	configPath string
	cachePath  string
}

// NewSSOManager creates a new SSO manager
func NewSSOManager() *SSOManager {
	home := getHomeDir()
	return &SSOManager{
		configPath: filepath.Join(home, ".aws", "config"),
		cachePath:  filepath.Join(home, ".aws", "sso", "cache"),
	}
}

// CheckCredentials checks if credentials are valid for a profile
func (m *SSOManager) CheckCredentials(profile string) (bool, time.Time, error) {
	// Try to get caller identity to verify credentials
	cmd := exec.Command("aws", "sts", "get-caller-identity", "--profile", profile)
	if err := cmd.Run(); err != nil {
		// Credentials may be expired or not logged in
		return false, time.Time{}, nil
	}

	// Try to find expiration from SSO cache
	expiresAt, err := m.getCredentialExpiration(profile)
	if err != nil {
		// If we can't determine expiration, assume valid for 1 hour
		return true, time.Now().Add(time.Hour), nil
	}

	return true, expiresAt, nil
}

// getCredentialExpiration tries to determine credential expiration from cache
func (m *SSOManager) getCredentialExpiration(profile string) (time.Time, error) {
	// Read SSO cache files
	files, err := os.ReadDir(m.cachePath)
	if err != nil {
		return time.Time{}, err
	}

	var latestExpiry time.Time
	for _, f := range files {
		if filepath.Ext(f.Name()) != ".json" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(m.cachePath, f.Name()))
		if err != nil {
			continue
		}

		var cache struct {
			AccessToken string    `json:"accessToken"`
			ExpiresAt   time.Time `json:"expiresAt"`
		}

		if err := json.Unmarshal(data, &cache); err != nil {
			continue
		}

		if cache.AccessToken != "" && cache.ExpiresAt.After(latestExpiry) {
			latestExpiry = cache.ExpiresAt
		}
	}

	if latestExpiry.IsZero() {
		return time.Time{}, fmt.Errorf("no valid SSO cache found")
	}

	return latestExpiry, nil
}

// Login performs AWS SSO login for a profile
func (m *SSOManager) Login(profile string) error {
	fmt.Printf("Logging in to AWS SSO for profile: %s\n", profile)

	cmd := exec.Command("aws", "sso", "login", "--profile", profile)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// GetCredentials exports credentials for a profile
func (m *SSOManager) GetCredentials(profile string) (*Credentials, error) {
	// Use aws configure export-credentials
	cmd := exec.Command("aws", "configure", "export-credentials", "--profile", profile, "--format", "env")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to export credentials: %w", err)
	}

	creds := &Credentials{}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimPrefix(line, "export ")
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := parts[0]
		value := strings.Trim(parts[1], "\"'")

		switch key {
		case "AWS_ACCESS_KEY_ID":
			creds.AccessKeyID = value
		case "AWS_SECRET_ACCESS_KEY":
			creds.SecretAccessKey = value
		case "AWS_SESSION_TOKEN":
			creds.SessionToken = value
		}
	}

	// Get region from profile
	region, _ := m.GetProfileRegion(profile)
	creds.Region = region

	if creds.AccessKeyID == "" {
		return nil, fmt.Errorf("failed to get credentials for profile: %s", profile)
	}

	return creds, nil
}

// GetProfileRegion gets the region for a profile
func (m *SSOManager) GetProfileRegion(profile string) (string, error) {
	cmd := exec.Command("aws", "configure", "get", "region", "--profile", profile)
	output, err := cmd.Output()
	if err != nil {
		return "us-east-1", nil // Default region
	}
	return strings.TrimSpace(string(output)), nil
}

// ListProfiles lists all AWS profiles
func (m *SSOManager) ListProfiles() ([]Profile, error) {
	cmd := exec.Command("aws", "configure", "list-profiles")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list profiles: %w", err)
	}

	var profiles []Profile
	for _, line := range strings.Split(string(output), "\n") {
		name := strings.TrimSpace(line)
		if name == "" || name == "default" {
			continue
		}
		profiles = append(profiles, Profile{Name: name})
	}

	return profiles, nil
}

// EnsureLoggedIn ensures credentials are valid, prompting for login if needed
func (m *SSOManager) EnsureLoggedIn(profile string, autoLogin bool) error {
	valid, expiresAt, err := m.CheckCredentials(profile)
	if err != nil {
		return fmt.Errorf("failed to check credentials: %w", err)
	}

	// Check if credentials are valid and not expiring soon
	if valid && time.Until(expiresAt) > 5*time.Minute {
		return nil
	}

	if !autoLogin {
		return fmt.Errorf("AWS credentials expired or invalid for profile: %s", profile)
	}

	// Perform login
	return m.Login(profile)
}

// GetAWSDir returns the path to the .aws directory
func GetAWSDir() string {
	return filepath.Join(getHomeDir(), ".aws")
}

// getHomeDir returns the user's home directory
func getHomeDir() string {
	if runtime.GOOS == "windows" {
		if home := os.Getenv("USERPROFILE"); home != "" {
			return home
		}
	}
	if home := os.Getenv("HOME"); home != "" {
		return home
	}
	home, _ := os.UserHomeDir()
	return home
}

// CredentialsToEnv converts credentials to environment variable format
func CredentialsToEnv(creds *Credentials) []string {
	env := []string{
		fmt.Sprintf("AWS_ACCESS_KEY_ID=%s", creds.AccessKeyID),
		fmt.Sprintf("AWS_SECRET_ACCESS_KEY=%s", creds.SecretAccessKey),
	}

	if creds.SessionToken != "" {
		env = append(env, fmt.Sprintf("AWS_SESSION_TOKEN=%s", creds.SessionToken))
	}

	if creds.Region != "" {
		env = append(env, fmt.Sprintf("AWS_DEFAULT_REGION=%s", creds.Region))
		env = append(env, fmt.Sprintf("AWS_REGION=%s", creds.Region))
	}

	return env
}
