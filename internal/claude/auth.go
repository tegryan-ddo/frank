package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Auth handles Claude Code authentication
type Auth struct {
	tokenEnvVar string
}

// NewAuth creates a new Auth handler
func NewAuth(tokenEnvVar string) *Auth {
	if tokenEnvVar == "" {
		tokenEnvVar = "CLAUDE_ACCESS_TOKEN"
	}
	return &Auth{
		tokenEnvVar: tokenEnvVar,
	}
}

// GetToken retrieves the Claude access token from environment
func (a *Auth) GetToken() (string, error) {
	token := os.Getenv(a.tokenEnvVar)
	if token == "" {
		return "", fmt.Errorf("Claude access token not found. Set %s environment variable", a.tokenEnvVar)
	}
	return token, nil
}

// HasToken checks if a Claude access token is available
func (a *Auth) HasToken() bool {
	return os.Getenv(a.tokenEnvVar) != ""
}

// GetTokenEnvVar returns the environment variable name for the token
func (a *Auth) GetTokenEnvVar() string {
	return a.tokenEnvVar
}

// TokenToEnv returns the token as an environment variable string
func (a *Auth) TokenToEnv() (string, error) {
	token, err := a.GetToken()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s=%s", a.tokenEnvVar, token), nil
}

// GetClaudeConfigDir returns the Claude Code configuration directory
func GetClaudeConfigDir() string {
	home := getHomeDir()

	switch runtime.GOOS {
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "claude-code")
		}
		return filepath.Join(home, "AppData", "Roaming", "claude-code")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "claude-code")
	default:
		if xdgConfig := os.Getenv("XDG_CONFIG_HOME"); xdgConfig != "" {
			return filepath.Join(xdgConfig, "claude-code")
		}
		return filepath.Join(home, ".config", "claude-code")
	}
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
