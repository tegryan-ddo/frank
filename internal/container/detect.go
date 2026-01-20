package container

import (
	"os/exec"
	"runtime"
)

// DetectRuntime detects and returns the best available container runtime
func DetectRuntime(preferred string) (Runtime, error) {
	// If a specific runtime is preferred, try that first
	switch preferred {
	case "docker":
		return tryDocker()
	case "podman":
		return tryPodman()
	case "orbstack":
		return tryOrbStack()
	}

	// Auto-detect: try runtimes in order of preference
	// On macOS, prefer OrbStack if available
	if runtime.GOOS == "darwin" {
		if r, err := tryOrbStack(); err == nil && r.IsAvailable() {
			return r, nil
		}
	}

	// Try Docker
	if r, err := tryDocker(); err == nil && r.IsAvailable() {
		return r, nil
	}

	// Try Podman
	if r, err := tryPodman(); err == nil && r.IsAvailable() {
		return r, nil
	}

	// Try OrbStack as fallback on any platform
	if r, err := tryOrbStack(); err == nil && r.IsAvailable() {
		return r, nil
	}

	return nil, ErrNoRuntimeFound
}

func tryDocker() (Runtime, error) {
	return NewDockerRuntime()
}

func tryPodman() (Runtime, error) {
	// Check if podman command exists
	if _, err := exec.LookPath("podman"); err != nil {
		return nil, err
	}
	return NewPodmanRuntime()
}

func tryOrbStack() (Runtime, error) {
	// Check if orbctl command exists (OrbStack CLI)
	if _, err := exec.LookPath("orbctl"); err != nil {
		// Also check for docker symlink from OrbStack
		if _, err := exec.LookPath("docker"); err != nil {
			return nil, err
		}
		// Could be OrbStack's docker, will be detected by IsAvailable()
	}
	return NewOrbStackRuntime()
}

// RuntimeError represents a container runtime error
type RuntimeError struct {
	message string
}

func (e RuntimeError) Error() string {
	return e.message
}

// ErrNoRuntimeFound is returned when no container runtime is found
var ErrNoRuntimeFound = RuntimeError{message: "no container runtime found (tried docker, podman, orbstack)"}
