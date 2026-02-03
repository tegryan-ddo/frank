package profile

// Profile represents a Frank ECS profile configuration
type Profile struct {
	Name        string `yaml:"name,omitempty"`
	Repo        string `yaml:"repo"`
	Branch      string `yaml:"branch,omitempty"`
	Description string `yaml:"description,omitempty"`
}

// ProfileConfig holds all profiles
type ProfileConfig struct {
	Profiles map[string]*Profile `yaml:"profiles"`
}

// NewProfileConfig creates a new empty ProfileConfig
func NewProfileConfig() *ProfileConfig {
	return &ProfileConfig{
		Profiles: make(map[string]*Profile),
	}
}
