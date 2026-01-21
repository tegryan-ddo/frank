package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"gopkg.in/yaml.v3"
)

const profilesFileName = "profiles.yaml"

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

// getProfilesPath returns the path to the profiles file
func getProfilesPath() string {
	return filepath.Join(getConfigDir(), profilesFileName)
}

// LoadProfiles loads profiles from the config file
func LoadProfiles() (*ProfileConfig, error) {
	path := getProfilesPath()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Return empty config if file doesn't exist
			return NewProfileConfig(), nil
		}
		return nil, fmt.Errorf("failed to read profiles file: %w", err)
	}

	var config ProfileConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse profiles file: %w", err)
	}

	// Initialize map if nil
	if config.Profiles == nil {
		config.Profiles = make(map[string]*Profile)
	}

	// Set the name field from the map key
	for name, profile := range config.Profiles {
		if profile != nil {
			profile.Name = name
		}
	}

	return &config, nil
}

// SaveProfiles saves profiles to the config file
func SaveProfiles(config *ProfileConfig) error {
	path := getProfilesPath()

	// Ensure config directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal profiles: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write profiles file: %w", err)
	}

	return nil
}

// GetProfile returns a profile by name
func GetProfile(name string) (*Profile, error) {
	config, err := LoadProfiles()
	if err != nil {
		return nil, err
	}

	profile, ok := config.Profiles[name]
	if !ok {
		return nil, fmt.Errorf("profile %q not found", name)
	}

	return profile, nil
}

// AddProfile adds or updates a profile
func AddProfile(profile *Profile) error {
	config, err := LoadProfiles()
	if err != nil {
		return err
	}

	config.Profiles[profile.Name] = profile
	return SaveProfiles(config)
}

// RemoveProfile removes a profile by name
func RemoveProfile(name string) error {
	config, err := LoadProfiles()
	if err != nil {
		return err
	}

	if _, ok := config.Profiles[name]; !ok {
		return fmt.Errorf("profile %q not found", name)
	}

	delete(config.Profiles, name)
	return SaveProfiles(config)
}

// ListProfiles returns all profile names
func ListProfiles() ([]string, error) {
	config, err := LoadProfiles()
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(config.Profiles))
	for name := range config.Profiles {
		names = append(names, name)
	}

	return names, nil
}

// GetProfilesPath returns the path to the profiles file (for display purposes)
func GetProfilesPath() string {
	return getProfilesPath()
}
