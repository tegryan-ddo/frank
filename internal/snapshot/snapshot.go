package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

// GenerateSnapshotName creates a consistent snapshot image name from a repo URL/path
// Format: frank-snapshot-{hash}:latest where hash is first 12 chars of SHA256
func GenerateSnapshotName(repoURL string) string {
	// Normalize the repo URL
	normalized := normalizeRepoURL(repoURL)

	// Create hash
	hash := sha256.Sum256([]byte(normalized))
	shortHash := hex.EncodeToString(hash[:])[:12]

	return "frank-snapshot-" + shortHash + ":latest"
}

// GenerateSnapshotNameWithTag creates a snapshot name with a specific tag
func GenerateSnapshotNameWithTag(repoURL, tag string) string {
	normalized := normalizeRepoURL(repoURL)
	hash := sha256.Sum256([]byte(normalized))
	shortHash := hex.EncodeToString(hash[:])[:12]

	return "frank-snapshot-" + shortHash + ":" + tag
}

// normalizeRepoURL normalizes a repo URL for consistent hashing
func normalizeRepoURL(repoURL string) string {
	// Remove trailing slashes
	repoURL = strings.TrimSuffix(repoURL, "/")

	// Remove .git suffix for comparison
	repoURL = strings.TrimSuffix(repoURL, ".git")

	// Convert SSH URLs to HTTPS format for consistency
	// git@github.com:user/repo -> https://github.com/user/repo
	if strings.HasPrefix(repoURL, "git@") {
		repoURL = strings.TrimPrefix(repoURL, "git@")
		repoURL = strings.Replace(repoURL, ":", "/", 1)
		repoURL = "https://" + repoURL
	}

	// Convert to lowercase for consistency
	repoURL = strings.ToLower(repoURL)

	// For local paths, use the absolute path and normalize separators
	if isLocalPath(repoURL) {
		// Get just the repo name for local paths
		repoURL = filepath.Base(repoURL)
	}

	return repoURL
}

// isLocalPath checks if the given path is a local filesystem path
func isLocalPath(path string) bool {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") ||
		strings.HasPrefix(path, "git://") || strings.HasPrefix(path, "ssh://") ||
		strings.HasPrefix(path, "git@") {
		return false
	}

	// Check for Windows drive letters
	if len(path) >= 2 && path[1] == ':' {
		return true
	}

	// Check for Unix absolute paths
	if strings.HasPrefix(path, "/") {
		return true
	}

	// Check for relative paths
	if strings.HasPrefix(path, "./") || strings.HasPrefix(path, "../") ||
		strings.HasPrefix(path, ".\\") || strings.HasPrefix(path, "..\\") {
		return true
	}

	return false
}
