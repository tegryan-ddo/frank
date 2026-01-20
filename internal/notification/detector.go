package notification

import (
	"regexp"
	"strings"

	"github.com/barff/frank/internal/config"
)

// PatternDetector detects notification patterns in text
type PatternDetector struct {
	questionPatterns []*regexp.Regexp
	keywordPatterns  []*regexp.Regexp
	promptPatterns   []*regexp.Regexp
}

// NewPatternDetector creates a new pattern detector from config
func NewPatternDetector(cfg config.NotificationConfig) *PatternDetector {
	d := &PatternDetector{}

	// Compile question patterns
	for _, p := range cfg.Patterns.Questions {
		if r, err := regexp.Compile(p); err == nil {
			d.questionPatterns = append(d.questionPatterns, r)
		}
	}

	// Compile keyword patterns (case insensitive)
	for _, k := range cfg.Patterns.Keywords {
		if r, err := regexp.Compile("(?i)" + regexp.QuoteMeta(k)); err == nil {
			d.keywordPatterns = append(d.keywordPatterns, r)
		}
	}

	// Compile prompt patterns
	for _, p := range cfg.Patterns.Prompts {
		if r, err := regexp.Compile(p); err == nil {
			d.promptPatterns = append(d.promptPatterns, r)
		}
	}

	return d
}

// ShouldNotify checks if a line should trigger a notification
func (d *PatternDetector) ShouldNotify(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}

	// Skip very short lines
	if len(line) < 3 {
		return false
	}

	// Check question patterns (e.g., ends with ?)
	for _, p := range d.questionPatterns {
		if p.MatchString(line) {
			return true
		}
	}

	// Check keyword patterns
	for _, p := range d.keywordPatterns {
		if p.MatchString(line) {
			return true
		}
	}

	// Check prompt patterns
	for _, p := range d.promptPatterns {
		if p.MatchString(line) {
			return true
		}
	}

	return false
}

// ExtractMessage extracts a displayable message from a line
func (d *PatternDetector) ExtractMessage(line string) string {
	const maxLen = 100
	line = strings.TrimSpace(line)

	// Remove ANSI escape codes
	line = stripAnsi(line)

	if len(line) > maxLen {
		return line[:maxLen-3] + "..."
	}
	return line
}

// stripAnsi removes ANSI escape codes from a string
func stripAnsi(s string) string {
	ansiRegex := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	return ansiRegex.ReplaceAllString(s, "")
}
