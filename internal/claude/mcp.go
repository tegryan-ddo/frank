package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// MCPServer represents an MCP server configuration
type MCPServer struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// MCPConfig represents Claude Code MCP configuration
type MCPConfig struct {
	MCPServers map[string]MCPServerConfig `json:"mcpServers"`
}

// MCPServerConfig represents a single MCP server's configuration
type MCPServerConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// MCPManager manages MCP server configurations
type MCPManager struct {
	configDir string
}

// NewMCPManager creates a new MCP manager
func NewMCPManager(configDir string) *MCPManager {
	return &MCPManager{
		configDir: configDir,
	}
}

// GetDefaultServers returns the default MCP server configurations
func GetDefaultServers() map[string]MCPServerConfig {
	return map[string]MCPServerConfig{
		"context7": {
			Command: "npx",
			Args:    []string{"-y", "@context7/mcp-server"},
		},
		"sequential-thinking": {
			Command: "npx",
			Args:    []string{"-y", "@modelcontextprotocol/server-sequential-thinking"},
		},
		"aws-documentation": {
			Command: "uvx",
			Args:    []string{"mcp-server-aws-documentation"},
		},
		"aws-knowledge": {
			Command: "uvx",
			Args:    []string{"awslabs.amazon-q-developer-mcp-server"},
		},
		"aws-core": {
			Command: "uvx",
			Args:    []string{"mcp-server-aws"},
		},
	}
}

// GenerateConfig generates the MCP configuration for enabled servers
func (m *MCPManager) GenerateConfig(enabledServers []MCPServer) (*MCPConfig, error) {
	defaultServers := GetDefaultServers()
	config := &MCPConfig{
		MCPServers: make(map[string]MCPServerConfig),
	}

	for _, server := range enabledServers {
		if !server.Enabled {
			continue
		}

		if serverConfig, ok := defaultServers[server.Name]; ok {
			config.MCPServers[server.Name] = serverConfig
		}
	}

	return config, nil
}

// WriteConfig writes the MCP configuration to a file
func (m *MCPManager) WriteConfig(config *MCPConfig, outputPath string) error {
	// Ensure directory exists
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(outputPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

// GetConfigPath returns the path for MCP configuration
func (m *MCPManager) GetConfigPath() string {
	return filepath.Join(m.configDir, "mcp-config.json")
}

// CreateContainerMCPConfig creates an MCP config file for container use
func (m *MCPManager) CreateContainerMCPConfig(enabledServers []MCPServer) (string, error) {
	config, err := m.GenerateConfig(enabledServers)
	if err != nil {
		return "", err
	}

	configPath := m.GetConfigPath()
	if err := m.WriteConfig(config, configPath); err != nil {
		return "", err
	}

	return configPath, nil
}

// GetClaudeMCPPath returns the path where Claude Code expects MCP config
func GetClaudeMCPPath() string {
	return filepath.Join(GetClaudeConfigDir(), "mcp.json")
}
