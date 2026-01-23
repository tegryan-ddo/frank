#!/bin/bash
set -e

# Frank Container Entrypoint
# Starts ttyd with Claude Code CLI and a separate bash terminal

# Default ports
WEB_PORT="${WEB_PORT:-7680}"      # Combined web view
TTYD_PORT="${TTYD_PORT:-7681}"    # Claude terminal
BASH_PORT="${BASH_PORT:-7682}"    # Bash terminal
STATUS_PORT="${STATUS_PORT:-7683}" # Status API

# Get container name (used for worktree naming)
CONTAINER_NAME="${CONTAINER_NAME:-$(hostname)}"
WORKTREE_PATH=""
REPO_BASE="/workspace/.repo"

# Ensure uv tools are in PATH
export PATH="$HOME/.local/bin:$PATH"

# Cleanup function - removes worktree on container shutdown
cleanup_worktree() {
    echo ""
    echo "=== Container shutting down ==="
    if [ -n "$WORKTREE_PATH" ] && [ -d "$WORKTREE_PATH" ]; then
        echo "Cleaning up worktree: $WORKTREE_PATH"
        # Remove the worktree
        git -C "$REPO_BASE" worktree remove "$WORKTREE_PATH" --force 2>/dev/null || true
        # Also try direct removal if git worktree remove fails
        rm -rf "$WORKTREE_PATH" 2>/dev/null || true
        echo "Worktree cleaned up"
    fi
    exit 0
}

# Set up trap for cleanup on container stop
trap cleanup_worktree SIGTERM SIGINT EXIT

# Configure git if not already configured
if [ -z "$(git config --global user.name 2>/dev/null)" ]; then
    git config --global user.name "Developer"
fi
if [ -z "$(git config --global user.email 2>/dev/null)" ]; then
    git config --global user.email "developer@frank.local"
fi

# Set git to use main as default branch
git config --global init.defaultBranch main

# Trust the workspace directory (and any subdirectories)
git config --global --add safe.directory /workspace
git config --global --add safe.directory '*'

# Fix git remote if it points to Windows path (C:\ or D:\)
if [ -d /workspace/.git ]; then
    REMOTE_URL=$(git -C /workspace remote get-url origin 2>/dev/null || true)
    if echo "$REMOTE_URL" | grep -qE '^[A-Za-z]:'; then
        echo "Warning: Git remote points to Windows path: $REMOTE_URL"
        echo "You may need to set the remote manually: git remote set-url origin <github-url>"
    fi
fi

# Setup GitHub CLI authentication for git if GH_TOKEN is set
if [ -n "$GH_TOKEN" ]; then
    echo "GitHub token configured"
    # Setup gh CLI auth and git credential helper
    echo "$GH_TOKEN" | gh auth login --with-token 2>/dev/null || true
    gh auth setup-git 2>/dev/null || true
fi

# Check for Claude authentication
if [ -f "$HOME/.claude/.credentials.json" ]; then
    echo "Claude OAuth credentials found"
elif [ -n "$ANTHROPIC_API_KEY" ]; then
    echo "Anthropic API key configured"
else
    echo "Note: No Claude credentials found - browser auth may be required"
fi

# Check AWS configuration
if [ -n "$AWS_ACCESS_KEY_ID" ] || [ -d "$HOME/.aws" ]; then
    echo "AWS credentials configured (profile: ${AWS_PROFILE:-default}, region: ${AWS_REGION:-us-east-1})"
fi

# -----------------------------------------------------------------------------
# Worktree Setup
# Creates worktrees for container isolation - works with both:
# 1. GIT_REPO env var (clone repo and create worktree)
# 2. Locally mounted git repos (use /workspace as base, create worktree)
# -----------------------------------------------------------------------------
setup_worktree_from_clone() {
    local repo_url="$1"
    local branch="${2:-main}"

    echo "Setting up worktree for container: $CONTAINER_NAME (from clone)"

    # Clone repo to base location if not exists
    if [ ! -d "$REPO_BASE/.git" ]; then
        echo "Cloning repository to $REPO_BASE..."
        git clone --bare "$repo_url" "$REPO_BASE/.git" || git clone "$repo_url" "$REPO_BASE"
        # If we cloned normally, convert to bare-like setup
        if [ -d "$REPO_BASE/.git" ] && [ -f "$REPO_BASE/.git/config" ]; then
            # It's a normal clone, that's fine for worktrees
            true
        fi
    else
        echo "Repository base exists, fetching updates..."
        git -C "$REPO_BASE" fetch --all 2>/dev/null || true
    fi

    # Set worktree path
    WORKTREE_PATH="/workspace/worktrees/$CONTAINER_NAME"
    export WORKTREE_PATH

    # Check if worktree already exists
    if [ -d "$WORKTREE_PATH" ]; then
        echo "Reusing existing worktree: $WORKTREE_PATH"
        # Verify it's a valid worktree
        if [ -f "$WORKTREE_PATH/.git" ]; then
            echo "Worktree is valid, using it"
        else
            echo "Invalid worktree directory, recreating..."
            rm -rf "$WORKTREE_PATH"
            git -C "$REPO_BASE" worktree add "$WORKTREE_PATH" "$branch" 2>/dev/null || \
                git -C "$REPO_BASE" worktree add -b "$CONTAINER_NAME" "$WORKTREE_PATH" "$branch"
        fi
    else
        echo "Creating new worktree: $WORKTREE_PATH"
        mkdir -p /workspace/worktrees
        # Try to checkout existing branch, or create new one based on target branch
        git -C "$REPO_BASE" worktree add "$WORKTREE_PATH" "$branch" 2>/dev/null || \
            git -C "$REPO_BASE" worktree add -b "$CONTAINER_NAME" "$WORKTREE_PATH" "$branch" 2>/dev/null || \
            git -C "$REPO_BASE" worktree add "$WORKTREE_PATH" "origin/$branch" 2>/dev/null || \
            git -C "$REPO_BASE" worktree add -b "$CONTAINER_NAME" "$WORKTREE_PATH" "origin/$branch"
    fi

    echo "Working directory: $WORKTREE_PATH"
    cd "$WORKTREE_PATH"
}

setup_worktree_from_local() {
    # For locally mounted repos, /workspace is the main repo
    local branch="${1:-main}"

    echo "Setting up worktree for container: $CONTAINER_NAME (from local mount)"

    # Get current branch of the mounted repo
    local current_branch
    current_branch=$(git -C /workspace branch --show-current 2>/dev/null || echo "main")

    # Set worktree path - use .worktrees inside the mounted repo
    WORKTREE_PATH="/workspace/.worktrees/$CONTAINER_NAME"
    export WORKTREE_PATH

    # Update REPO_BASE to point to the mounted workspace
    REPO_BASE="/workspace"

    # Check if worktree already exists
    if [ -d "$WORKTREE_PATH" ]; then
        echo "Reusing existing worktree: $WORKTREE_PATH"
        # Verify it's a valid worktree
        if [ -f "$WORKTREE_PATH/.git" ]; then
            echo "Worktree is valid, using it"
        else
            echo "Invalid worktree directory, recreating..."
            rm -rf "$WORKTREE_PATH"
            # Create worktree from current branch with a new branch named after container
            git -C /workspace worktree add -b "$CONTAINER_NAME" "$WORKTREE_PATH" "$current_branch" 2>/dev/null || \
                git -C /workspace worktree add "$WORKTREE_PATH" "$current_branch"
        fi
    else
        echo "Creating new worktree: $WORKTREE_PATH"
        mkdir -p /workspace/.worktrees
        # Create worktree - try to create a branch named after container, or just checkout current branch
        git -C /workspace worktree add -b "$CONTAINER_NAME" "$WORKTREE_PATH" "$current_branch" 2>/dev/null || \
            git -C /workspace worktree add "$WORKTREE_PATH" "$current_branch" 2>/dev/null || \
            git -C /workspace worktree add -b "$CONTAINER_NAME" "$WORKTREE_PATH" HEAD
    fi

    echo "Working directory: $WORKTREE_PATH"
    cd "$WORKTREE_PATH"
}

# Setup worktree based on configuration
if [ -n "$GIT_REPO" ]; then
    # Clone from URL and create worktree
    setup_worktree_from_clone "$GIT_REPO" "${GIT_BRANCH:-main}"
    WORK_DIR="$WORKTREE_PATH"
elif [ -d "/workspace/.git" ]; then
    # Local mount with git repo - create worktree from it
    setup_worktree_from_local "${GIT_BRANCH:-main}"
    WORK_DIR="$WORKTREE_PATH"
else
    # No git repo - just use /workspace directly
    echo "No git repository detected - using /workspace directly"
    WORK_DIR="/workspace"
fi

# Change to working directory
cd "$WORK_DIR"
echo "Current directory: $(pwd)"

# Common ttyd theme settings
TTYD_THEME='{"background":"#1e1e1e","foreground":"#d4d4d4","cursor":"#d4d4d4","selectionBackground":"#264f78","black":"#1e1e1e","red":"#f44747","green":"#6a9955","yellow":"#dcdcaa","blue":"#569cd6","magenta":"#c586c0","cyan":"#4ec9b0","white":"#d4d4d4","brightBlack":"#808080","brightRed":"#f44747","brightGreen":"#6a9955","brightYellow":"#dcdcaa","brightBlue":"#569cd6","brightMagenta":"#c586c0","brightCyan":"#4ec9b0","brightWhite":"#ffffff"}'

# Generate the combined view HTML with correct ports
# The HTML file uses relative ports from the host perspective
WEB_DIR="/tmp/frank-web"
mkdir -p "$WEB_DIR"
sed "s|CLAUDE_URL|http://localhost:${HOST_CLAUDE_PORT:-8081}|g; s|BASH_URL|http://localhost:${HOST_BASH_PORT:-8082}|g; s|STATUS_ENDPOINT|http://localhost:${HOST_STATUS_PORT:-8083}/status|g" \
    /usr/local/share/frank/index.html > "$WEB_DIR/index.html"

# Start status server (background, with logging)
echo "Starting status server on port $STATUS_PORT..."
python3 /usr/local/bin/status-server.py >> /tmp/status-server-stdout.log 2>&1 &
STATUS_PID=$!
sleep 1
if kill -0 $STATUS_PID 2>/dev/null; then
    echo "Status server started (PID: $STATUS_PID)"
else
    echo "WARNING: Status server failed to start! Check /tmp/status-server-stdout.log"
fi

# Start the combined web view (background)
echo "Starting combined web view on port $WEB_PORT..."
python3 -m http.server "$WEB_PORT" --directory "$WEB_DIR" &> /dev/null &

# Start bash terminal on secondary port (background)
echo "Starting bash terminal on port $BASH_PORT..."
ttyd \
    -p "${BASH_PORT}" \
    -W \
    -t fontSize=16 \
    -t fontFamily="Consolas, 'Courier New', monospace" \
    -t lineHeight=1.2 \
    -t cursorBlink=true \
    -t cursorStyle=bar \
    -t theme="${TTYD_THEME}" \
    --ping-interval 60 \
    bash &

# Start ttyd with Claude Code (foreground)
echo "Starting Claude terminal on port $TTYD_PORT..."
exec ttyd \
    -p "${TTYD_PORT}" \
    -W \
    -t fontSize=16 \
    -t fontFamily="Consolas, 'Courier New', monospace" \
    -t lineHeight=1.2 \
    -t cursorBlink=true \
    -t cursorStyle=bar \
    -t theme="${TTYD_THEME}" \
    --ping-interval 60 \
    claude
