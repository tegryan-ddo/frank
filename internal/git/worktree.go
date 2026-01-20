package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// WorktreeManager manages git worktrees for containers
type WorktreeManager struct {
	baseDir string
}

// NewWorktreeManager creates a new worktree manager
func NewWorktreeManager(baseDir string) *WorktreeManager {
	return &WorktreeManager{
		baseDir: baseDir,
	}
}

// isLocalPath checks if the given path is a local filesystem path
func isLocalPath(path string) bool {
	// Check for common URL schemes
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") ||
		strings.HasPrefix(path, "git://") || strings.HasPrefix(path, "ssh://") ||
		strings.HasPrefix(path, "git@") {
		return false
	}

	// Check if it's a filesystem path that exists
	if _, err := os.Stat(path); err == nil {
		return true
	}

	// Check for relative paths (., ..)
	if strings.HasPrefix(path, "./") || strings.HasPrefix(path, "../") ||
		strings.HasPrefix(path, ".\\") || strings.HasPrefix(path, "..\\") {
		return true
	}

	// Check for absolute paths
	if filepath.IsAbs(path) {
		return true
	}

	return false
}

// Create creates a new git worktree for a container
func (w *WorktreeManager) Create(containerName, repoURL, branch string) (string, error) {
	worktreePath := filepath.Join(w.baseDir, containerName)

	// Ensure base directory exists
	if err := os.MkdirAll(w.baseDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create worktree base directory: %w", err)
	}

	// Check if worktree already exists
	if _, err := os.Stat(worktreePath); err == nil {
		return worktreePath, nil // Already exists
	}

	// Check if this is a local path - if so, use CreateFromExisting
	if isLocalPath(repoURL) {
		absPath, err := filepath.Abs(repoURL)
		if err != nil {
			return "", fmt.Errorf("failed to resolve local path: %w", err)
		}
		return w.CreateFromExisting(containerName, absPath, branch)
	}

	// Check if we have a main repo clone
	mainRepoPath := filepath.Join(w.baseDir, ".main-repo")

	// Prune stale worktrees before proceeding
	if _, err := os.Stat(mainRepoPath); err == nil {
		pruneCmd := exec.Command("git", "-C", mainRepoPath, "worktree", "prune")
		pruneCmd.Run() // Ignore errors
	}

	if _, err := os.Stat(mainRepoPath); os.IsNotExist(err) {
		// Clone the repository first
		fmt.Printf("Cloning repository: %s\n", repoURL)
		cmd := exec.Command("git", "clone", "--bare", repoURL, mainRepoPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("failed to clone repository: %w", err)
		}
	} else {
		// Fetch latest changes
		cmd := exec.Command("git", "-C", mainRepoPath, "fetch", "--all")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			// Non-fatal, continue with existing state
			fmt.Printf("Warning: failed to fetch latest changes: %v\n", err)
		}
	}

	// Create worktree
	if branch == "" {
		branch = "main"
	}

	// Check if branch exists, if not try master
	checkCmd := exec.Command("git", "-C", mainRepoPath, "rev-parse", "--verify", branch)
	if err := checkCmd.Run(); err != nil {
		// Try master
		checkMaster := exec.Command("git", "-C", mainRepoPath, "rev-parse", "--verify", "master")
		if err := checkMaster.Run(); err == nil {
			branch = "master"
		}
	}

	// Try to create worktree with the branch
	cmd := exec.Command("git", "-C", mainRepoPath, "worktree", "add", worktreePath, branch)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// If branch is already checked out, create a new branch for this worktree
		newBranch := fmt.Sprintf("%s-%s", branch, containerName)
		fmt.Printf("Branch '%s' already in use, creating worktree with new branch '%s'\n", branch, newBranch)

		cmd = exec.Command("git", "-C", mainRepoPath, "worktree", "add", "-b", newBranch, worktreePath, branch)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("failed to create worktree: %w", err)
		}
	}

	return worktreePath, nil
}

// CreateFromExisting creates a clone from an existing local repository
// Note: We use clone instead of worktree because worktrees store absolute paths
// that don't work when mounted inside containers
func (w *WorktreeManager) CreateFromExisting(containerName, localRepoPath, branch string) (string, error) {
	worktreePath := filepath.Join(w.baseDir, containerName)

	// Ensure base directory exists
	if err := os.MkdirAll(w.baseDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create worktree base directory: %w", err)
	}

	// Check if clone already exists
	if _, err := os.Stat(worktreePath); err == nil {
		return worktreePath, nil
	}

	// Determine branch to checkout
	if branch == "" {
		branch = "main"
	}

	// Check if branch exists, if not try master
	checkCmd := exec.Command("git", "-C", localRepoPath, "rev-parse", "--verify", branch)
	if err := checkCmd.Run(); err != nil {
		checkMaster := exec.Command("git", "-C", localRepoPath, "rev-parse", "--verify", "master")
		if err := checkMaster.Run(); err == nil {
			branch = "master"
		}
	}

	// Get the upstream remote URL from the source repo (to avoid Windows paths in container)
	getRemoteCmd := exec.Command("git", "-C", localRepoPath, "remote", "get-url", "origin")
	remoteOutput, _ := getRemoteCmd.Output()
	upstreamURL := strings.TrimSpace(string(remoteOutput))

	// Clone the local repository (this creates a standalone copy that works in containers)
	fmt.Printf("Cloning local repository to: %s\n", worktreePath)
	cmd := exec.Command("git", "clone", "--branch", branch, localRepoPath, worktreePath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to clone local repository: %w", err)
	}

	// Set up remote to point to upstream (GitHub/GitLab) instead of local Windows path
	if upstreamURL != "" && !isLocalPath(upstreamURL) {
		fmt.Printf("Setting remote origin to: %s\n", upstreamURL)
		remoteCmd := exec.Command("git", "-C", worktreePath, "remote", "set-url", "origin", upstreamURL)
		remoteCmd.Run()
	}

	return worktreePath, nil
}

// Remove removes a git worktree
func (w *WorktreeManager) Remove(containerName string) error {
	worktreePath := filepath.Join(w.baseDir, containerName)

	// Check if worktree exists
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return nil // Nothing to remove
	}

	// Find the main repo
	mainRepoPath := filepath.Join(w.baseDir, ".main-repo")

	if _, err := os.Stat(mainRepoPath); err == nil {
		// Remove worktree using git
		cmd := exec.Command("git", "-C", mainRepoPath, "worktree", "remove", "--force", worktreePath)
		if err := cmd.Run(); err != nil {
			// If git worktree remove fails, try manual removal
			if err := os.RemoveAll(worktreePath); err != nil {
				return fmt.Errorf("failed to remove worktree: %w", err)
			}
		}

		// Prune worktrees
		pruneCmd := exec.Command("git", "-C", mainRepoPath, "worktree", "prune")
		pruneCmd.Run() // Ignore errors
	} else {
		// No main repo, just remove the directory
		if err := os.RemoveAll(worktreePath); err != nil {
			return fmt.Errorf("failed to remove worktree: %w", err)
		}
	}

	return nil
}

// List lists all worktrees
func (w *WorktreeManager) List() ([]WorktreeInfo, error) {
	mainRepoPath := filepath.Join(w.baseDir, ".main-repo")

	if _, err := os.Stat(mainRepoPath); os.IsNotExist(err) {
		return nil, nil
	}

	cmd := exec.Command("git", "-C", mainRepoPath, "worktree", "list", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list worktrees: %w", err)
	}

	var worktrees []WorktreeInfo
	var current WorktreeInfo

	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			if current.Path != "" {
				worktrees = append(worktrees, current)
				current = WorktreeInfo{}
			}
			continue
		}

		if strings.HasPrefix(line, "worktree ") {
			current.Path = strings.TrimPrefix(line, "worktree ")
			current.Name = filepath.Base(current.Path)
		} else if strings.HasPrefix(line, "HEAD ") {
			current.HEAD = strings.TrimPrefix(line, "HEAD ")
		} else if strings.HasPrefix(line, "branch ") {
			current.Branch = strings.TrimPrefix(line, "branch refs/heads/")
		}
	}

	// Add last worktree if exists
	if current.Path != "" {
		worktrees = append(worktrees, current)
	}

	return worktrees, nil
}

// GetPath returns the path for a container's worktree
func (w *WorktreeManager) GetPath(containerName string) string {
	return filepath.Join(w.baseDir, containerName)
}

// WorktreeInfo holds information about a worktree
type WorktreeInfo struct {
	Name   string
	Path   string
	Branch string
	HEAD   string
}
