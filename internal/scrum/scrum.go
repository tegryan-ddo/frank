package scrum

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// WorkItem represents a single unit of work to be dispatched
type WorkItem struct {
	ID        int      `json:"id"`
	Title     string   `json:"title"`
	Prompt    string   `json:"prompt"`
	Files     []string `json:"files,omitempty"`
	DependsOn []int    `json:"depends_on,omitempty"`
}

// ScrumPlan represents the decomposed plan
type ScrumPlan struct {
	Goal      string     `json:"goal"`
	WorkItems []WorkItem `json:"work_items"`
	Summary   string     `json:"summary"`
}

// TaskStatus tracks a dispatched task
type TaskStatus struct {
	WorkItem      WorkItem  `json:"work_item"`
	ContainerName string    `json:"container_name"`
	TaskArn       string    `json:"task_arn"`
	TaskID        string    `json:"task_id"`
	Status        string    `json:"status"`
	ExitCode      int       `json:"exit_code"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	CompletedAt   time.Time `json:"completed_at,omitempty"`
}

// ScrumSession tracks the full orchestration session
type ScrumSession struct {
	ID          string       `json:"id"`
	Profile     string       `json:"profile"`
	Goal        string       `json:"goal"`
	Plan        *ScrumPlan   `json:"plan"`
	Tasks       []TaskStatus `json:"tasks"`
	CreatedAt   time.Time    `json:"created_at"`
	CompletedAt time.Time    `json:"completed_at,omitempty"`
	Status      string       `json:"status"`
}

const scrumBaseDir = "/workspace/scrum"

// BuildDecompositionPrompt creates the meta-prompt and JSON schema for the planner task.
// It returns the prompt string and the expected output schema description that the CLI
// layer will use to dispatch a single Codex planner task via ECS.
func BuildDecompositionPrompt(goal string) (prompt string, outputSchema string) {
	prompt = fmt.Sprintf(`You are a scrum master for an AI coding team. Analyze the codebase in the current working directory and decompose the following goal into independent work items that can be executed in parallel by coding agents.

Each work item should be self-contained with a clear, specific prompt that an AI coding agent can execute without asking clarifying questions.

Goal: %s

Important guidelines:
- Keep work items independent where possible (minimize depends_on references)
- Each prompt should be specific enough for an AI coding agent to execute without clarification
- Include the specific files to modify in each work item when you can identify them
- Aim for 2-6 work items (don't over-decompose simple tasks)
- Use depends_on only when one work item truly cannot start until another finishes
- IDs should be sequential starting from 1

Output your plan as a single JSON object (no markdown fences, no commentary) matching this exact schema:

{
  "goal": "the original goal restated",
  "work_items": [
    {
      "id": 1,
      "title": "Short descriptive title",
      "prompt": "Detailed prompt for the coding agent",
      "files": ["path/to/file1.go", "path/to/file2.go"],
      "depends_on": []
    }
  ],
  "summary": "Brief summary of the decomposition strategy"
}`, goal)

	outputSchema = `{
  "type": "object",
  "required": ["goal", "work_items", "summary"],
  "properties": {
    "goal": { "type": "string" },
    "summary": { "type": "string" },
    "work_items": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["id", "title", "prompt"],
        "properties": {
          "id": { "type": "integer" },
          "title": { "type": "string" },
          "prompt": { "type": "string" },
          "files": { "type": "array", "items": { "type": "string" } },
          "depends_on": { "type": "array", "items": { "type": "integer" } }
        }
      }
    }
  }
}`

	return prompt, outputSchema
}

// GetExecutionWaves groups work items by dependency order.
// Wave 0 contains items with no dependencies, Wave 1 contains items that
// depend only on Wave 0 items, and so on.
func GetExecutionWaves(items []WorkItem) [][]WorkItem {
	if len(items) == 0 {
		return nil
	}

	// Build a map of item ID to item for quick lookup
	itemByID := make(map[int]WorkItem)
	for _, item := range items {
		itemByID[item.ID] = item
	}

	// Track which wave each item is assigned to
	waveOf := make(map[int]int)
	assigned := make(map[int]bool)

	// Iteratively assign waves
	maxIterations := len(items) + 1 // guard against cycles
	for iteration := 0; iteration < maxIterations; iteration++ {
		progress := false
		for _, item := range items {
			if assigned[item.ID] {
				continue
			}

			// Check if all dependencies are assigned
			maxDepWave := -1
			allDepsAssigned := true
			for _, depID := range item.DependsOn {
				if !assigned[depID] {
					allDepsAssigned = false
					break
				}
				if waveOf[depID] > maxDepWave {
					maxDepWave = waveOf[depID]
				}
			}

			if allDepsAssigned {
				wave := 0
				if maxDepWave >= 0 {
					wave = maxDepWave + 1
				}
				waveOf[item.ID] = wave
				assigned[item.ID] = true
				progress = true
			}
		}

		if !progress {
			// Remaining items have circular dependencies; force them into the next wave
			nextWave := 0
			for _, w := range waveOf {
				if w >= nextWave {
					nextWave = w + 1
				}
			}
			for _, item := range items {
				if !assigned[item.ID] {
					waveOf[item.ID] = nextWave
					assigned[item.ID] = true
				}
			}
			break
		}

		if len(assigned) == len(items) {
			break
		}
	}

	// Group items by wave
	maxWave := 0
	for _, w := range waveOf {
		if w > maxWave {
			maxWave = w
		}
	}

	waves := make([][]WorkItem, maxWave+1)
	for _, item := range items {
		w := waveOf[item.ID]
		waves[w] = append(waves[w], item)
	}

	// Sort items within each wave by ID for deterministic ordering
	for i := range waves {
		sort.Slice(waves[i], func(a, b int) bool {
			return waves[i][a].ID < waves[i][b].ID
		})
	}

	return waves
}

// ParsePlanFromJSON parses a ScrumPlan from Codex's JSON output.
// It handles cases where the JSON may be wrapped in markdown code fences.
func ParsePlanFromJSON(data []byte) (*ScrumPlan, error) {
	// Strip markdown code fences if present
	text := strings.TrimSpace(string(data))
	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		// Remove first line (```json or ```) and last line (```)
		start := 1
		end := len(lines) - 1
		if end > start {
			text = strings.Join(lines[start:end], "\n")
		}
	}

	var plan ScrumPlan
	if err := json.Unmarshal([]byte(text), &plan); err != nil {
		return nil, fmt.Errorf("failed to parse plan JSON: %w\n\nRaw output:\n%s", err, string(data))
	}

	if len(plan.WorkItems) == 0 {
		return nil, fmt.Errorf("plan contains no work items")
	}

	return &plan, nil
}

// sessionDir returns the directory path for a session
func sessionDir(sessionID string) string {
	return filepath.Join(scrumBaseDir, sessionID)
}

// SaveSession saves a scrum session to disk at /workspace/scrum/<session-id>/session.json
func SaveSession(session *ScrumSession) error {
	dir := sessionDir(session.ID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create session directory: %w", err)
	}

	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session: %w", err)
	}

	path := filepath.Join(dir, "session.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write session file: %w", err)
	}

	return nil
}

// LoadSession loads a scrum session from disk
func LoadSession(sessionID string) (*ScrumSession, error) {
	path := filepath.Join(sessionDir(sessionID), "session.json")

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read session %s: %w", sessionID, err)
	}

	var session ScrumSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("failed to parse session %s: %w", sessionID, err)
	}

	return &session, nil
}

// ListSessions lists all scrum session IDs, sorted most recent first.
func ListSessions() ([]string, error) {
	entries, err := os.ReadDir(scrumBaseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}

	var sessions []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// Verify it contains a session.json
		sessionPath := filepath.Join(scrumBaseDir, entry.Name(), "session.json")
		if _, err := os.Stat(sessionPath); err == nil {
			sessions = append(sessions, entry.Name())
		}
	}

	// Sort descending (most recent first, since IDs are timestamp-based)
	sort.Sort(sort.Reverse(sort.StringSlice(sessions)))

	return sessions, nil
}

// NewSessionID generates a unique session ID based on the current timestamp.
func NewSessionID() string {
	return time.Now().Format("20060102-150405")
}
