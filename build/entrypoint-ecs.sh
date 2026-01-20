#!/bin/bash
set -e

# Frank Container Entrypoint - ECS Version
# Fetches credentials from AWS Secrets Manager and starts ttyd with Claude Code

# Default ports
WEB_PORT="${WEB_PORT:-7680}"
TTYD_PORT="${TTYD_PORT:-7681}"
BASH_PORT="${BASH_PORT:-7682}"
STATUS_PORT="${STATUS_PORT:-7683}"

# Get container name (used for worktree naming)
# In ECS, use ECS_CONTAINER_METADATA or CONTAINER_NAME env var
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

# Fetch Claude credentials from Secrets Manager
if [ -n "$CLAUDE_CREDENTIALS_SECRET_ARN" ]; then
    echo "Fetching Claude credentials from Secrets Manager..."
    mkdir -p "$HOME/.claude"

    # Fetch the secret and write to credentials file
    aws secretsmanager get-secret-value \
        --secret-id "$CLAUDE_CREDENTIALS_SECRET_ARN" \
        --query 'SecretString' \
        --output text > "$HOME/.claude/.credentials.json"

    chmod 600 "$HOME/.claude/.credentials.json"
    echo "Claude OAuth credentials configured"
else
    echo "WARNING: CLAUDE_CREDENTIALS_SECRET_ARN not set - Claude auth may fail"
fi

# Fetch GitHub token from Secrets Manager
if [ -n "$GITHUB_TOKEN_SECRET_ARN" ]; then
    echo "Fetching GitHub token from Secrets Manager..."

    export GH_TOKEN=$(aws secretsmanager get-secret-value \
        --secret-id "$GITHUB_TOKEN_SECRET_ARN" \
        --query 'SecretString' \
        --output text)

    # Setup gh CLI auth
    echo "$GH_TOKEN" | gh auth login --with-token 2>/dev/null || true
    gh auth setup-git 2>/dev/null || true
    echo "GitHub token configured"
elif [ -n "$GH_TOKEN" ]; then
    # Token passed directly as env var
    echo "$GH_TOKEN" | gh auth login --with-token 2>/dev/null || true
    gh auth setup-git 2>/dev/null || true
    echo "GitHub token configured (from env)"
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
setup_worktree_from_clone() {
    local repo_url="$1"
    local branch="${2:-main}"

    echo "Setting up worktree for container: $CONTAINER_NAME (from clone)"

    # Clone repo to base location if not exists
    if [ ! -d "$REPO_BASE/.git" ]; then
        echo "Cloning repository to $REPO_BASE..."
        git clone "$repo_url" "$REPO_BASE"
    else
        echo "Repository base exists, fetching updates..."
        git -C "$REPO_BASE" fetch --all 2>/dev/null || true
    fi

    # Set worktree path using container name
    WORKTREE_PATH="/workspace/worktrees/$CONTAINER_NAME"
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
        mkdir -p /workspace/worktrees
        git -C "$REPO_BASE" worktree add "$WORKTREE_PATH" "$branch" 2>/dev/null || \
            git -C "$REPO_BASE" worktree add -b "$CONTAINER_NAME" "$WORKTREE_PATH" "$branch" 2>/dev/null || \
            git -C "$REPO_BASE" worktree add "$WORKTREE_PATH" "origin/$branch" 2>/dev/null || \
            git -C "$REPO_BASE" worktree add -b "$CONTAINER_NAME" "$WORKTREE_PATH" "origin/$branch"
    fi

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

# Verify MCP Launchpad
if command -v mcpl &> /dev/null; then
    echo "MCP Launchpad available"
    mcpl verify &> /dev/null &
fi

# Common ttyd theme
TTYD_THEME='{"background":"#1e1e1e","foreground":"#d4d4d4","cursor":"#d4d4d4","selectionBackground":"#264f78","black":"#1e1e1e","red":"#f44747","green":"#6a9955","yellow":"#dcdcaa","blue":"#569cd6","magenta":"#c586c0","cyan":"#4ec9b0","white":"#d4d4d4","brightBlack":"#808080","brightRed":"#f44747","brightGreen":"#6a9955","brightYellow":"#dcdcaa","brightBlue":"#569cd6","brightMagenta":"#c586c0","brightCyan":"#4ec9b0","brightWhite":"#ffffff"}'

# Generate web view HTML
WEB_DIR="/tmp/frank-web"
mkdir -p "$WEB_DIR"
sed "s|CLAUDE_URL|http://localhost:${HOST_CLAUDE_PORT:-8081}|g; s|BASH_URL|http://localhost:${HOST_BASH_PORT:-8082}|g; s|STATUS_ENDPOINT|http://localhost:${HOST_STATUS_PORT:-8083}/status|g" \
    /usr/local/share/frank/index.html > "$WEB_DIR/index.html" 2>/dev/null || true

# Start status server
echo "Starting status server on port $STATUS_PORT..."
python3 /usr/local/bin/status-server.py >> /tmp/status-server-stdout.log 2>&1 &

# Start web view
echo "Starting web view on port $WEB_PORT..."
python3 -m http.server "$WEB_PORT" --directory "$WEB_DIR" &> /dev/null &

# Start bash terminal
echo "Starting bash terminal on port $BASH_PORT..."
ttyd -p "${BASH_PORT}" -W \
    -t fontSize=16 \
    -t fontFamily="Consolas, 'Courier New', monospace" \
    -t theme="${TTYD_THEME}" \
    --ping-interval 60 \
    bash &

# Start Claude terminal (foreground)
echo "Starting Claude terminal on port $TTYD_PORT..."
echo "=== Frank ECS Container Ready ==="

exec ttyd -p "${TTYD_PORT}" -W \
    -t fontSize=16 \
    -t fontFamily="Consolas, 'Courier New', monospace" \
    -t theme="${TTYD_THEME}" \
    --ping-interval 60 \
    claude
