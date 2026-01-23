package config

import (
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/viper"
)

// Config holds all configuration for frank
type Config struct {
	Version       string              `mapstructure:"version"`
	Runtime       RuntimeConfig       `mapstructure:"runtime"`
	Container     ContainerConfig     `mapstructure:"container"`
	AWS           AWSConfig           `mapstructure:"aws"`
	ECS           ECSConfig           `mapstructure:"ecs"`
	Claude        ClaudeConfig        `mapstructure:"claude"`
	GitHub        GitHubConfig        `mapstructure:"github"`
	Notifications NotificationConfig  `mapstructure:"notifications"`
	MCP           MCPConfig           `mapstructure:"mcp"`
	Git           GitConfig           `mapstructure:"git"`
	Logging       LoggingConfig       `mapstructure:"logging"`
}

// RuntimeConfig holds container runtime settings
type RuntimeConfig struct {
	Preferred string        `mapstructure:"preferred"` // auto, docker, podman, orbstack
	Timeout   time.Duration `mapstructure:"timeout"`
}

// ContainerConfig holds container settings
type ContainerConfig struct {
	Image          string `mapstructure:"image"`
	BasePort       int    `mapstructure:"basePort"`
	MaxPort        int    `mapstructure:"maxPort"`
	WorkspaceMount string `mapstructure:"workspaceMount"`
}

// AWSConfig holds AWS settings
type AWSConfig struct {
	DefaultProfile          string        `mapstructure:"defaultProfile"`
	AutoLogin               bool          `mapstructure:"autoLogin"`
	CredentialRefreshBuffer time.Duration `mapstructure:"credentialRefreshBuffer"`
}

// ECSConfig holds ECS deployment settings
type ECSConfig struct {
	Domain  string `mapstructure:"domain"`  // Domain name for ALB (e.g., frank.digitaldevops.io)
	Cluster string `mapstructure:"cluster"` // ECS cluster name
}

// ClaudeConfig holds Claude Code settings
type ClaudeConfig struct {
	TokenEnvVar string `mapstructure:"tokenEnvVar"`
}

// GitHubConfig holds GitHub authentication settings
type GitHubConfig struct {
	MountSSH      bool `mapstructure:"mountSSH"`      // Always mount ~/.ssh
	MountGHConfig bool `mapstructure:"mountGHConfig"` // Always mount ~/.config/gh
}

// NotificationConfig holds notification settings
type NotificationConfig struct {
	Enabled           bool                   `mapstructure:"enabled"`
	Cooldown          time.Duration          `mapstructure:"cooldown"`
	Sound             bool                   `mapstructure:"sound"`
	InactivityTimeout time.Duration          `mapstructure:"inactivityTimeout"`
	Patterns          NotificationPatterns   `mapstructure:"patterns"`
}

// NotificationPatterns holds the patterns for detecting notifications
type NotificationPatterns struct {
	Questions []string `mapstructure:"questions"`
	Keywords  []string `mapstructure:"keywords"`
	Prompts   []string `mapstructure:"prompts"`
}

// MCPConfig holds MCP server settings
type MCPConfig struct {
	ConfigDir string      `mapstructure:"configDir"`
	Servers   []MCPServer `mapstructure:"servers"`
}

// MCPServer represents an MCP server configuration
type MCPServer struct {
	Name    string `mapstructure:"name"`
	Enabled bool   `mapstructure:"enabled"`
}

// GitConfig holds git settings
type GitConfig struct {
	WorktreeBase      string `mapstructure:"worktreeBase"`
	CleanupOnStop     bool   `mapstructure:"cleanupOnStop"`
	AutoCommitMessage string `mapstructure:"autoCommitMessage"`
}

// LoggingConfig holds logging settings
type LoggingConfig struct {
	Level   string `mapstructure:"level"`
	Verbose bool   `mapstructure:"verbose"`
	File    string `mapstructure:"file"`
}

// DefaultConfig returns the default configuration
func DefaultConfig() *Config {
	home, _ := os.UserHomeDir()

	return &Config{
		Version: "1.0",
		Runtime: RuntimeConfig{
			Preferred: "auto",
			Timeout:   30 * time.Second,
		},
		Container: ContainerConfig{
			Image:          "frank-dev:latest",
			BasePort:       8080,
			MaxPort:        8180,
			WorkspaceMount: "/workspace",
		},
		AWS: AWSConfig{
			DefaultProfile:          "",
			AutoLogin:               true,
			CredentialRefreshBuffer: 5 * time.Minute,
		},
		ECS: ECSConfig{
			Domain:  "frank.digitaldevops.io",
			Cluster: "frank",
		},
		Claude: ClaudeConfig{
			TokenEnvVar: "CLAUDE_ACCESS_TOKEN",
		},
		GitHub: GitHubConfig{
			MountSSH:      false,
			MountGHConfig: false,
		},
		Notifications: NotificationConfig{
			Enabled:           true,
			Cooldown:          30 * time.Second,
			Sound:             true,
			InactivityTimeout: 30 * time.Second,
			Patterns: NotificationPatterns{
				Questions: []string{
					`\?$`,
				},
				Keywords: []string{
					"continue",
					"approve",
					"proceed",
					"waiting",
					"input",
					"response",
					"confirm",
					"permission",
				},
				Prompts: []string{
					`\[Y/n\]`,
					`\(yes/no\)`,
					`Press Enter`,
					`Do you want`,
					`Should I`,
					`Would you like`,
				},
			},
		},
		MCP: MCPConfig{
			ConfigDir: filepath.Join(home, ".config", "frank", "mcp"),
			Servers: []MCPServer{
				{Name: "context7", Enabled: true},
				{Name: "sequential-thinking", Enabled: true},
				{Name: "aws-documentation", Enabled: true},
				{Name: "aws-knowledge", Enabled: true},
				{Name: "aws-core", Enabled: true},
			},
		},
		Git: GitConfig{
			WorktreeBase:      filepath.Join(home, ".frank", "worktrees"),
			CleanupOnStop:     true,
			AutoCommitMessage: "WIP: Auto-save before container stop",
		},
		Logging: LoggingConfig{
			Level:   "info",
			Verbose: false,
			File:    "",
		},
	}
}

// Load loads configuration from file and environment
func Load(cfgFile string) (*Config, error) {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		configDir := getConfigDir()
		viper.AddConfigPath(configDir)
		viper.SetConfigName("config")
		viper.SetConfigType("yaml")
	}

	// Set defaults
	defaults := DefaultConfig()
	setDefaults(defaults)

	// Environment variables
	viper.SetEnvPrefix("FRANK")
	viper.AutomaticEnv()

	// Read config file (ignore if not found)
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, err
		}
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// getConfigDir returns the configuration directory based on OS
func getConfigDir() string {
	home, _ := os.UserHomeDir()

	switch runtime.GOOS {
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "frank")
		}
		return filepath.Join(home, "AppData", "Roaming", "frank")
	default:
		if xdgConfig := os.Getenv("XDG_CONFIG_HOME"); xdgConfig != "" {
			return filepath.Join(xdgConfig, "frank")
		}
		return filepath.Join(home, ".config", "frank")
	}
}

// GetConfigDir returns the configuration directory
func GetConfigDir() string {
	return getConfigDir()
}

func setDefaults(cfg *Config) {
	viper.SetDefault("version", cfg.Version)
	viper.SetDefault("runtime.preferred", cfg.Runtime.Preferred)
	viper.SetDefault("runtime.timeout", cfg.Runtime.Timeout)
	viper.SetDefault("container.image", cfg.Container.Image)
	viper.SetDefault("container.basePort", cfg.Container.BasePort)
	viper.SetDefault("container.maxPort", cfg.Container.MaxPort)
	viper.SetDefault("container.workspaceMount", cfg.Container.WorkspaceMount)
	viper.SetDefault("aws.defaultProfile", cfg.AWS.DefaultProfile)
	viper.SetDefault("aws.autoLogin", cfg.AWS.AutoLogin)
	viper.SetDefault("aws.credentialRefreshBuffer", cfg.AWS.CredentialRefreshBuffer)
	viper.SetDefault("ecs.domain", cfg.ECS.Domain)
	viper.SetDefault("ecs.cluster", cfg.ECS.Cluster)
	viper.SetDefault("claude.tokenEnvVar", cfg.Claude.TokenEnvVar)
	viper.SetDefault("github.mountSSH", cfg.GitHub.MountSSH)
	viper.SetDefault("github.mountGHConfig", cfg.GitHub.MountGHConfig)
	viper.SetDefault("notifications.enabled", cfg.Notifications.Enabled)
	viper.SetDefault("notifications.cooldown", cfg.Notifications.Cooldown)
	viper.SetDefault("notifications.sound", cfg.Notifications.Sound)
	viper.SetDefault("notifications.inactivityTimeout", cfg.Notifications.InactivityTimeout)
	viper.SetDefault("notifications.patterns.questions", cfg.Notifications.Patterns.Questions)
	viper.SetDefault("notifications.patterns.keywords", cfg.Notifications.Patterns.Keywords)
	viper.SetDefault("notifications.patterns.prompts", cfg.Notifications.Patterns.Prompts)
	viper.SetDefault("mcp.configDir", cfg.MCP.ConfigDir)
	viper.SetDefault("mcp.servers", cfg.MCP.Servers)
	viper.SetDefault("git.worktreeBase", cfg.Git.WorktreeBase)
	viper.SetDefault("git.cleanupOnStop", cfg.Git.CleanupOnStop)
	viper.SetDefault("git.autoCommitMessage", cfg.Git.AutoCommitMessage)
	viper.SetDefault("logging.level", cfg.Logging.Level)
	viper.SetDefault("logging.verbose", cfg.Logging.Verbose)
	viper.SetDefault("logging.file", cfg.Logging.File)
}
