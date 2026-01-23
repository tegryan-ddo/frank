#!/bin/bash
set -e

# Frank Container Entrypoint - ECS Version
# Fetches credentials from AWS Secrets Manager and starts ttyd with Claude Code

# Default ports
WEB_PORT="${WEB_PORT:-7680}"
TTYD_PORT="${TTYD_PORT:-7681}"
BASH_PORT="${BASH_PORT:-7682}"
STATUS_PORT="${STATUS_PORT:-7683}"

# URL prefix for path-based routing (e.g., /enkai for profile "enkai")
# When set, ttyd serves on /<prefix> instead of /claude
URL_PREFIX="${URL_PREFIX:-}"

# Get container name (used for worktree naming)
# In ECS, use ECS_CONTAINER_METADATA or CONTAINER_NAME env var
CONTAINER_NAME="${CONTAINER_NAME:-$(hostname)}"
WORKTREE_PATH=""
# Each profile gets its own repo directory to avoid conflicts on shared EFS
REPO_BASE="/workspace/repos/$CONTAINER_NAME"

# Ensure uv tools are in PATH
export PATH="$HOME/.local/bin:$PATH"

# Cleanup function - removes worktree on container shutdown
cleanup_worktree() {
    echo ""
    echo "=== Container shutting down ==="
    if [ -n "$WORKTREE_PATH" ] && [ -d "$WORKTREE_PATH" ]; then
        echo "Cleaning up worktree: $WORKTREE_PATH"
        git -C "$REPO_BASE" worktree remove "$WORKTREE_PATH" --force 2>/dev/null || true
        rm -rf "$WORKTREE_PATH" 2>/dev/null || true
        echo "Worktree cleaned up"
    fi
    exit 0
}

# Set up trap for cleanup on container stop
trap cleanup_worktree SIGTERM SIGINT EXIT

echo "=== Frank ECS Container Starting ==="
echo "Container name: $CONTAINER_NAME"

# Setup Claude credentials (injected by Copilot secrets)
if [ -n "$CLAUDE_CREDENTIALS" ]; then
    echo "Configuring Claude credentials..."
    mkdir -p "$HOME/.claude"
    echo "$CLAUDE_CREDENTIALS" > "$HOME/.claude/.credentials.json"
    chmod 600 "$HOME/.claude/.credentials.json"
    echo "Claude OAuth credentials configured"
else
    echo "WARNING: CLAUDE_CREDENTIALS not set - Claude auth may fail"
fi

# Setup GitHub token (injected by Copilot secrets)
if [ -n "$GITHUB_TOKEN" ]; then
    echo "Configuring GitHub token..."
    export GH_TOKEN="$GITHUB_TOKEN"
    echo "$GITHUB_TOKEN" | gh auth login --with-token 2>/dev/null || true
    gh auth setup-git 2>/dev/null || true
    echo "GitHub token configured"
elif [ -n "$GH_TOKEN" ]; then
    # Token passed directly as GH_TOKEN env var
    echo "$GH_TOKEN" | gh auth login --with-token 2>/dev/null || true
    gh auth setup-git 2>/dev/null || true
    echo "GitHub token configured (from GH_TOKEN)"
else
    echo "WARNING: No GitHub token configured"
fi

# AWS credentials come automatically from ECS task IAM role
echo "AWS credentials: using ECS task IAM role"
echo "  Region: ${AWS_REGION:-us-east-1}"

# Configure git
if [ -z "$(git config --global user.name 2>/dev/null)" ]; then
    git config --global user.name "${GIT_USER_NAME:-Developer}"
fi
if [ -z "$(git config --global user.email 2>/dev/null)" ]; then
    git config --global user.email "${GIT_USER_EMAIL:-developer@frank.local}"
fi
git config --global init.defaultBranch main
git config --global --add safe.directory /workspace
git config --global --add safe.directory '*'

# -----------------------------------------------------------------------------
# Worktree Setup
# Creates worktrees for container isolation - works with both:
# 1. GIT_REPO env var (clone repo and create worktree)
# 2. Pre-existing git repos on EFS (use /workspace as base, create worktree)
# Worktrees persist on EFS, containers with same name reuse them
# -----------------------------------------------------------------------------

# Link .claude directory from base repo to worktree for hooks/settings
link_claude_directory() {
    local worktree_path="$1"
    local base_repo="$2"

    # Check if base repo has .claude directory
    if [ -d "$base_repo/.claude" ]; then
        # Remove existing .claude in worktree if it exists (but isn't a symlink)
        if [ -d "$worktree_path/.claude" ] && [ ! -L "$worktree_path/.claude" ]; then
            echo "Removing existing .claude directory in worktree"
            rm -rf "$worktree_path/.claude"
        fi

        # Create symlink if it doesn't exist
        if [ ! -e "$worktree_path/.claude" ]; then
            echo "Linking .claude directory: $worktree_path/.claude -> $base_repo/.claude"
            ln -s "$base_repo/.claude" "$worktree_path/.claude"
        else
            echo ".claude symlink already exists"
        fi
    else
        echo "No .claude directory in base repo - skipping symlink"
    fi
}
setup_worktree_from_clone() {
    local repo_url="$1"
    local branch="${2:-main}"

    echo "Setting up worktree for container: $CONTAINER_NAME (from clone)"

    # Clone repo to base location if not exists, or re-clone if URL changed
    if [ ! -d "$REPO_BASE/.git" ]; then
        echo "Cloning repository to $REPO_BASE..."
        git clone "$repo_url" "$REPO_BASE"
    else
        # Check if existing repo matches the requested URL
        local existing_url
        existing_url=$(git -C "$REPO_BASE" remote get-url origin 2>/dev/null || echo "")
        if [ "$existing_url" != "$repo_url" ]; then
            echo "Repository URL changed: $existing_url -> $repo_url"
            echo "Removing old repo and worktrees..."
            rm -rf "$REPO_BASE" /workspace/worktrees/*
            echo "Cloning new repository to $REPO_BASE..."
            git clone "$repo_url" "$REPO_BASE"
        else
            echo "Repository base exists, fetching updates..."
            git -C "$REPO_BASE" fetch --all 2>/dev/null || true
        fi
    fi

    # Set worktree path - keep under profile directory to avoid cross-profile issues
    WORKTREE_PATH="/workspace/repos/$CONTAINER_NAME/work"
    export WORKTREE_PATH

    # Check if worktree already exists (reuse if same container name)
    if [ -d "$WORKTREE_PATH" ]; then
        echo "Reusing existing worktree: $WORKTREE_PATH"
        if [ -f "$WORKTREE_PATH/.git" ]; then
            echo "Worktree is valid"
        else
            echo "Invalid worktree, recreating..."
            rm -rf "$WORKTREE_PATH"
            git -C "$REPO_BASE" worktree add "$WORKTREE_PATH" "$branch" 2>/dev/null || \
                git -C "$REPO_BASE" worktree add -b "$CONTAINER_NAME" "$WORKTREE_PATH" "$branch"
        fi
    else
        echo "Creating new worktree: $WORKTREE_PATH"
        mkdir -p "/workspace/repos/$CONTAINER_NAME"
        git -C "$REPO_BASE" worktree add "$WORKTREE_PATH" "$branch" 2>/dev/null || \
            git -C "$REPO_BASE" worktree add -b "$CONTAINER_NAME" "$WORKTREE_PATH" "$branch" 2>/dev/null || \
            git -C "$REPO_BASE" worktree add "$WORKTREE_PATH" "origin/$branch" 2>/dev/null || \
            git -C "$REPO_BASE" worktree add -b "$CONTAINER_NAME" "$WORKTREE_PATH" "origin/$branch"
    fi

    # Link .claude directory for hooks and settings
    link_claude_directory "$WORKTREE_PATH" "$REPO_BASE"

    echo "Working directory: $WORKTREE_PATH"
    cd "$WORKTREE_PATH"
}

setup_worktree_from_local() {
    # For pre-existing repos on EFS, /workspace is the main repo
    local branch="${1:-main}"

    echo "Setting up worktree for container: $CONTAINER_NAME (from existing repo)"

    # Get current branch of the repo
    local current_branch
    current_branch=$(git -C /workspace branch --show-current 2>/dev/null || echo "main")

    # Set worktree path - use .worktrees inside the repo
    WORKTREE_PATH="/workspace/.worktrees/$CONTAINER_NAME"
    export WORKTREE_PATH

    # Update REPO_BASE to point to the workspace
    REPO_BASE="/workspace"

    # Check if worktree already exists
    if [ -d "$WORKTREE_PATH" ]; then
        echo "Reusing existing worktree: $WORKTREE_PATH"
        if [ -f "$WORKTREE_PATH/.git" ]; then
            echo "Worktree is valid"
        else
            echo "Invalid worktree, recreating..."
            rm -rf "$WORKTREE_PATH"
            git -C /workspace worktree add -b "$CONTAINER_NAME" "$WORKTREE_PATH" "$current_branch" 2>/dev/null || \
                git -C /workspace worktree add "$WORKTREE_PATH" "$current_branch"
        fi
    else
        echo "Creating new worktree: $WORKTREE_PATH"
        mkdir -p /workspace/.worktrees
        git -C /workspace worktree add -b "$CONTAINER_NAME" "$WORKTREE_PATH" "$current_branch" 2>/dev/null || \
            git -C /workspace worktree add "$WORKTREE_PATH" "$current_branch" 2>/dev/null || \
            git -C /workspace worktree add -b "$CONTAINER_NAME" "$WORKTREE_PATH" HEAD
    fi

    # Link .claude directory for hooks and settings
    link_claude_directory "$WORKTREE_PATH" "/workspace"

    echo "Working directory: $WORKTREE_PATH"
    cd "$WORKTREE_PATH"
}

# Setup worktree based on configuration
if [ -n "$GIT_REPO" ]; then
    # Clone from URL and create worktree
    setup_worktree_from_clone "$GIT_REPO" "${GIT_BRANCH:-main}"
    WORK_DIR="$WORKTREE_PATH"
elif [ -d "/workspace/.git" ]; then
    # Pre-existing repo on EFS - create worktree from it
    setup_worktree_from_local "${GIT_BRANCH:-main}"
    WORK_DIR="$WORKTREE_PATH"
else
    # No git repo - just use /workspace directly
    echo "No git repository detected - using /workspace directly"
    WORK_DIR="/workspace"
fi

cd "$WORK_DIR"
echo "Current directory: $(pwd)"


# Common ttyd theme
TTYD_THEME='{"background":"#1e1e1e","foreground":"#d4d4d4","cursor":"#d4d4d4","selectionBackground":"#264f78","black":"#1e1e1e","red":"#f44747","green":"#6a9955","yellow":"#dcdcaa","blue":"#569cd6","magenta":"#c586c0","cyan":"#4ec9b0","white":"#d4d4d4","brightBlack":"#808080","brightRed":"#f44747","brightGreen":"#6a9955","brightYellow":"#dcdcaa","brightBlue":"#569cd6","brightMagenta":"#c586c0","brightCyan":"#4ec9b0","brightWhite":"#ffffff"}'

# Function to wait for a port to be available
wait_for_port() {
    local port=$1
    local service_name=$2
    local max_attempts=${3:-10}
    local attempt=1

    echo "Waiting for $service_name on port $port..."
    while [ $attempt -le $max_attempts ]; do
        if nc -z localhost "$port" 2>/dev/null; then
            echo "  $service_name is ready on port $port"
            return 0
        fi
        sleep 1
        attempt=$((attempt + 1))
    done

    echo "  WARNING: $service_name on port $port not responding after $max_attempts seconds"
    return 1
}

# Generate web view HTML
WEB_DIR="/tmp/frank-web"
mkdir -p "$WEB_DIR"
sed "s|CLAUDE_URL|http://localhost:${HOST_CLAUDE_PORT:-8081}|g; s|BASH_URL|http://localhost:${HOST_BASH_PORT:-8082}|g; s|STATUS_ENDPOINT|http://localhost:${HOST_STATUS_PORT:-8083}/status|g" \
    /usr/local/share/frank/index.html > "$WEB_DIR/index.html" 2>/dev/null || true

# Start combined web+status server (serves static files and status API on WEB_PORT)
echo "Starting web+status server on port $WEB_PORT..."
export WEB_DIR="$WEB_DIR"
export URL_PREFIX="$URL_PREFIX"
export WEB_PORT="$WEB_PORT"
export TTYD_PORT="$TTYD_PORT"
export BASH_PORT="$BASH_PORT"
python3 /usr/local/bin/status-server.py >> /tmp/status-server-stdout.log 2>&1 &
WEB_PID=$!

# Wait for web server to be ready (serves on both WEB_PORT and STATUS_PORT)
wait_for_port "$WEB_PORT" "Web server" 15
if [ $? -ne 0 ]; then
    echo "ERROR: Web server failed to start. Check /tmp/status-server-stdout.log"
    cat /tmp/status-server-stdout.log 2>/dev/null || true
fi

# Wait for health server on STATUS_PORT (used by ECS health checks)
wait_for_port "$STATUS_PORT" "Health server" 10
if [ $? -ne 0 ]; then
    echo "WARNING: Health server on port $STATUS_PORT not responding"
fi

# Determine base paths for ttyd
# If URL_PREFIX is set (e.g., /enkai), use subpaths for terminals:
#   - /enkai/ serves the HTML wrapper (from port 7680)
#   - /enkai/_t/ serves Claude terminal (from port 7681)
#   - /enkai/_b/ serves Bash terminal (from port 7682)
# This avoids iframe loops where the wrapper loads itself
if [ -n "$URL_PREFIX" ]; then
    CLAUDE_BASE_PATH="${URL_PREFIX}/_t"
    BASH_BASE_PATH="${URL_PREFIX}/_b"
    echo "Using URL prefix: $URL_PREFIX"
    echo "  Wrapper path: $URL_PREFIX/"
    echo "  Claude terminal path: $CLAUDE_BASE_PATH/"
    echo "  Bash terminal path: $BASH_BASE_PATH/"
else
    CLAUDE_BASE_PATH="/claude"
    BASH_BASE_PATH="/bash"
fi

# Start bash terminal with base path for ALB routing
echo "Starting bash terminal on port $BASH_PORT (path: $BASH_BASE_PATH)..."
ttyd -p "${BASH_PORT}" -W \
    -t fontSize=16 \
    -t fontFamily="Consolas, 'Courier New', monospace" \
    -t theme="${TTYD_THEME}" \
    --ping-interval 60 \
    --base-path "$BASH_BASE_PATH" \
    bash &
BASH_PID=$!

# Wait for bash terminal to be ready
wait_for_port "$BASH_PORT" "Bash terminal" 10

# Start Claude terminal (foreground) with base path for ALB routing
echo "Starting Claude terminal on port $TTYD_PORT (path: $CLAUDE_BASE_PATH)..."
echo "=== Frank ECS Container Ready ==="

exec ttyd -p "${TTYD_PORT}" -W \
    -t fontSize=16 \
    -t fontFamily="Consolas, 'Courier New', monospace" \
    -t theme="${TTYD_THEME}" \
    --ping-interval 60 \
    --base-path "$CLAUDE_BASE_PATH" \
    claude
