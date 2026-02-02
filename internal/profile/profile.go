package profile

const (
	AgentClaude = "claude"
	AgentCodex  = "codex"

	ModeInteractive = "interactive"
	ModeHeadless    = "headless"
)

// Profile represents a Frank ECS profile configuration
type Profile struct {
	Name        string `yaml:"name,omitempty"`
	Repo        string `yaml:"repo"`
	Branch      string `yaml:"branch,omitempty"`
	Description string `yaml:"description,omitempty"`
	Agent       string `yaml:"agent,omitempty"`
	Mode        string `yaml:"mode,omitempty"`
	Task        string `yaml:"task,omitempty"`
	Model       string `yaml:"model,omitempty"`
}

// GetAgent returns the agent backend for this profile, defaulting to Claude.
func (p *Profile) GetAgent() string {
	if p.Agent == "" {
		return AgentClaude
	}
	return p.Agent
}

// GetMode returns the mode for this profile, defaulting to interactive.
func (p *Profile) GetMode() string {
	if p.Mode == "" {
		return ModeInteractive
	}
	return p.Mode
}

// IsHeadless returns true if the profile is configured for headless mode.
func (p *Profile) IsHeadless() bool {
	return p.GetMode() == ModeHeadless
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
