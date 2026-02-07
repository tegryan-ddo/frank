package profile

// Profile represents a Frank ECS profile configuration
type Profile struct {
	Name        string `yaml:"name,omitempty" json:"name"`
	Repo        string `yaml:"repo" json:"repo"`
	Branch      string `yaml:"branch,omitempty" json:"branch,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	SiteURL     string `yaml:"site_url,omitempty" json:"site_url,omitempty"`
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
