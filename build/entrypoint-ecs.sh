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

# Capture current task definition revision for update detection
# ECS provides metadata via $ECS_CONTAINER_METADATA_URI_V4
if [ -n "$ECS_CONTAINER_METADATA_URI_V4" ]; then
    echo "Capturing task definition version..."
    TASK_METADATA=$(curl -s "$ECS_CONTAINER_METADATA_URI_V4/task" 2>/dev/null || echo "{}")
    TASK_DEF_ARN=$(echo "$TASK_METADATA" | jq -r '.TaskDefinitionArn // empty' 2>/dev/null)
    if [ -n "$TASK_DEF_ARN" ]; then
        # Extract revision number (e.g., "arn:aws:ecs:...:task-definition/FrankStack-FrankTask:42" -> "42")
        TASK_DEF_REVISION=$(echo "$TASK_DEF_ARN" | grep -oE ':[0-9]+$' | tr -d ':')
        TASK_DEF_FAMILY=$(echo "$TASK_DEF_ARN" | sed 's/:task-definition\//:/' | cut -d'/' -f2 | cut -d':' -f1)
        echo "Task definition: $TASK_DEF_FAMILY:$TASK_DEF_REVISION"

        # Store for status server to read
        mkdir -p /tmp/frank
        echo "$TASK_DEF_REVISION" > /tmp/frank/current-revision
        echo "$TASK_DEF_FAMILY" > /tmp/frank/task-family
        echo "$TASK_DEF_ARN" > /tmp/frank/task-def-arn
    fi
else
    echo "Not running in ECS (no metadata URI)"
fi

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

# Start credential sync (shares OAuth tokens across containers via Secrets Manager)
/usr/local/bin/credential-sync.sh &

# Start Codex credential sync (shares device auth tokens across containers)
/usr/local/bin/codex-credential-sync.sh &

# Create Codex wrapper to disable Landlock sandbox (ECS containers have restrictive security)
# The sandbox prevents Codex from executing shell commands in this environment
if command -v codex >/dev/null 2>&1; then
    CODEX_REAL=$(command -v codex)
    # Only wrap if not already wrapped
    if ! grep -q "codex-wrapper" "$CODEX_REAL" 2>/dev/null; then
        echo "Creating Codex wrapper to disable sandbox..."
        mv "$CODEX_REAL" "${CODEX_REAL}-original"
        cat > "$CODEX_REAL" <<'CODEXWRAP'
#!/bin/bash
# Codex wrapper - disables Landlock sandbox for ECS compatibility
exec "$(dirname "$0")/codex-original" --dangerously-bypass-approvals-and-sandbox "$@"
CODEXWRAP
        chmod +x "$CODEX_REAL"
        echo "Codex sandbox disabled for ECS environment"
    fi
fi

# Setup GitHub authentication
# Priority: 1) GitHub App (auto-refreshing), 2) Personal Access Token
GITHUB_AUTH_OK=false

# Try GitHub App authentication first (preferred - tokens auto-refresh)
if [ -n "$GITHUB_APP_ID" ] && [ -n "$GITHUB_APP_PRIVATE_KEY" ] && [ -n "$GITHUB_APP_INSTALLATION_ID" ]; then
    echo "Configuring GitHub App authentication..."
    if GH_APP_TOKEN=$(/usr/local/bin/github-app-token.sh 2>/tmp/github-app-error.log); then
        export GH_TOKEN="$GH_APP_TOKEN"
        echo "$GH_APP_TOKEN" | gh auth login --with-token 2>/dev/null || true
        gh auth setup-git 2>/dev/null || true
        echo "GitHub App authentication configured (app_id: $GITHUB_APP_ID)"
        GITHUB_AUTH_OK=true
    else
        echo "WARNING: GitHub App token generation failed"
        cat /tmp/github-app-error.log 2>/dev/null || true
        echo "Falling back to token authentication..."
    fi
fi

# Fall back to personal access token
if [ "$GITHUB_AUTH_OK" = false ]; then
    if [ -n "$GITHUB_TOKEN" ]; then
        echo "Configuring GitHub token..."
        export GH_TOKEN="$GITHUB_TOKEN"
        echo "$GITHUB_TOKEN" | gh auth login --with-token 2>/dev/null || true
        gh auth setup-git 2>/dev/null || true
        echo "GitHub token configured"
        GITHUB_AUTH_OK=true
    elif [ -n "$GH_TOKEN" ]; then
        # Token passed directly as GH_TOKEN env var
        echo "$GH_TOKEN" | gh auth login --with-token 2>/dev/null || true
        gh auth setup-git 2>/dev/null || true
        echo "GitHub token configured (from GH_TOKEN)"
        GITHUB_AUTH_OK=true
    fi
fi

if [ "$GITHUB_AUTH_OK" = false ]; then
    echo "WARNING: No GitHub authentication configured"
    echo "  Set GITHUB_APP_* vars for app auth, or GITHUB_TOKEN for PAT auth"
fi

# Start Pnyx credential sync daemon (handles per-agent API keys)
# The daemon will:
#   1. Check for agent-specific secret: /frank/pnyx-api-key/{CONTAINER_NAME}
#   2. Fall back to PNYX_API_KEY env var (backwards compatibility)
#   3. Sync local changes back to agent-specific secret in Secrets Manager
mkdir -p "$HOME/.config/pnyx"
if [ -n "$PNYX_API_KEY" ]; then
    echo "{\"api_key\":\"$PNYX_API_KEY\",\"api_url\":\"https://pnyx.digitaldevops.io\"}" > "$HOME/.config/pnyx/credentials.json"
    chmod 600 "$HOME/.config/pnyx/credentials.json"
fi
echo "Starting Pnyx credential sync daemon..."
/usr/local/bin/pnyx-credential-sync.sh &
export PNYX_API_URL="https://pnyx.digitaldevops.io"
echo "Pnyx credential sync started for agent: ${CONTAINER_NAME:-unknown}"

# Start Ollama for local LLM (used by Pnyx tick)
# Store models on EFS so they persist across container restarts
export OLLAMA_MODELS="/workspace/.ollama/models"
mkdir -p "$OLLAMA_MODELS"
if command -v ollama >/dev/null 2>&1; then
    echo "Starting Ollama server..."
    ollama serve >> /tmp/ollama.log 2>&1 &
    OLLAMA_PID=$!
    echo "Ollama server started (PID $OLLAMA_PID)"
    # Pull default model in background (won't block container startup)
    (
        # Wait for Ollama to be ready
        for i in $(seq 1 30); do
            if curl -sf http://localhost:11434/api/version >/dev/null 2>&1; then
                echo "Ollama ready, pulling model..." >> /tmp/ollama.log
                ollama pull llama3.2 >> /tmp/ollama.log 2>&1 || true
                echo "Model pull complete" >> /tmp/ollama.log
                break
            fi
            sleep 2
        done
    ) &
else
    echo "WARNING: Ollama not installed - Pnyx tick AI features disabled"
fi

# Start analytics sync daemon (uploads local analytics to S3)
echo "Starting analytics sync daemon..."
/usr/local/bin/analytics-sync.sh &
echo "Analytics sync started for profile: ${CONTAINER_NAME:-unknown}"

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
# Claude Code Plugins Setup
# Clones official plugins repo and installs selected plugins to ~/.claude/
# -----------------------------------------------------------------------------
PLUGINS_REPO="https://github.com/anthropics/claude-plugins-official.git"
PLUGINS_CACHE="/opt/claude-plugins-official"
PLUGINS_DIR="$HOME/.claude/plugins"

# List of plugins to install (internal plugins from plugins/ directory)
INTERNAL_PLUGINS=(
    "frontend-design"
    "code-review"
    "feature-dev"
    "security-guidance"
    "code-simplifier"
    "ralph-loop"
    "typescript-lsp"
    "pyright-lsp"
    "gopls-lsp"
    "claude-code-setup"
    "claude-md-management"
    "hookify"
)

# List of external plugins (from external_plugins/ directory)
EXTERNAL_PLUGINS=(
    "context7"
    "serena"
    "playwright"
    "greptile"
)

clone_plugins_repo() {
    echo "=== Preparing Claude Code Plugins ==="

    # Clone or update plugins repo
    if [ -d "$PLUGINS_CACHE/.git" ]; then
        echo "Updating plugins repository..."
        git -C "$PLUGINS_CACHE" pull --quiet 2>/dev/null || true
    else
        echo "Cloning plugins repository..."
        git clone --depth 1 "$PLUGINS_REPO" "$PLUGINS_CACHE" 2>/dev/null || {
            echo "WARNING: Failed to clone plugins repo"
            return 1
        }
    fi

    # Get commit SHA for versioning
    PLUGINS_GIT_SHA=$(git -C "$PLUGINS_CACHE" rev-parse --short=12 HEAD 2>/dev/null || echo "unknown")
    echo "Plugins repo version: $PLUGINS_GIT_SHA"
}

install_plugins() {
    local project_path="$1"
    echo "=== Installing Claude Code Plugins ==="
    echo "Project path: $project_path"

    local cache_base="$PLUGINS_DIR/cache/claude-plugins-official"
    local now
    now=$(date -u +"%Y-%m-%dT%H:%M:%S.000Z")
    local git_sha
    git_sha=$(git -C "$PLUGINS_CACHE" rev-parse HEAD 2>/dev/null || echo "")

    mkdir -p "$PLUGINS_DIR"

    # Start building installed_plugins.json
    local plugins_json='{"version":2,"plugins":{'
    local first_entry=true

    # Install a single plugin to cache and return JSON entry
    install_single_plugin() {
        local plugin_name="$1"
        local src_dir="$2"
        local version="$PLUGINS_GIT_SHA"
        local install_path="$cache_base/$plugin_name/$version"

        if [ ! -d "$src_dir" ]; then
            echo "  WARNING: Plugin not found: $plugin_name"
            return 1
        fi

        echo "  Installing plugin: $plugin_name"

        # Create cache directory and copy plugin files (excluding .claude-plugin metadata)
        mkdir -p "$install_path"
        cp -r "$src_dir/skills" "$install_path/" 2>/dev/null || true
        cp -r "$src_dir/README.md" "$install_path/" 2>/dev/null || true

        # Also create the symlink for backward compat
        ln -sf "$src_dir" "$PLUGINS_DIR/$plugin_name" 2>/dev/null || true

        # Build JSON entry
        local entry="\"${plugin_name}@claude-plugins-official\":[{\"scope\":\"project\",\"projectPath\":\"${project_path}\",\"installPath\":\"${install_path}\",\"version\":\"${version}\",\"installedAt\":\"${now}\",\"lastUpdated\":\"${now}\",\"gitCommitSha\":\"${git_sha}\"}]"

        if [ "$first_entry" = true ]; then
            first_entry=false
        else
            plugins_json="${plugins_json},"
        fi
        plugins_json="${plugins_json}${entry}"
    }

    # Install internal plugins
    for plugin in "${INTERNAL_PLUGINS[@]}"; do
        install_single_plugin "$plugin" "$PLUGINS_CACHE/plugins/$plugin"
    done

    # Install external plugins
    for plugin in "${EXTERNAL_PLUGINS[@]}"; do
        install_single_plugin "$plugin" "$PLUGINS_CACHE/external_plugins/$plugin"
    done

    # Close JSON and write
    plugins_json="${plugins_json}}}"
    echo "$plugins_json" > "$PLUGINS_DIR/installed_plugins.json"

    echo "Plugins installed and registered to $PLUGINS_DIR"
    echo "  Registered $(echo "$plugins_json" | grep -o '@claude-plugins-official' | wc -l) plugin(s)"
}

# Clone plugins repo early (must complete before Claude starts)
clone_plugins_repo

# Sync community skills from GitHub repos (e.g., tegryan-ddo/pedro)
source /usr/local/bin/install-community-skills.sh
sync_community_skills

# -----------------------------------------------------------------------------
# Worktree Setup
# Creates worktrees for container isolation - works with both:
# 1. GIT_REPO env var (clone repo and create worktree)
# 2. Pre-existing git repos on EFS (use /workspace as base, create worktree)
# Worktrees persist on EFS, containers with same name reuse them
# -----------------------------------------------------------------------------

# Install the post-checkout hook into a repo so new worktrees get .claude/ auto-copied.
# Git's init.templateDir only applies at clone/init time, so for repos already on EFS
# we need to install the hook directly.
install_worktree_hook() {
    local repo_path="$1"
    local hook_src="/usr/share/git-core/templates/hooks/post-checkout"
    local hook_dst="$repo_path/.git/hooks/post-checkout"

    [ -f "$hook_src" ] || return 0
    [ -d "$repo_path/.git/hooks" ] || return 0

    # Don't overwrite an existing hook that differs (might be project-specific)
    if [ -f "$hook_dst" ] && ! grep -q "copies .claude/ directory into new git worktrees" "$hook_dst" 2>/dev/null; then
        return 0
    fi

    cp "$hook_src" "$hook_dst"
    chmod +x "$hook_dst"
}

# Copy .claude directory from base repo to worktree for hooks/settings
# Uses merge strategy: copies base repo files without destroying worktree-specific
# content (e.g., hooks that exist in the checked-out branch but not the base).
# Note: We copy instead of symlink because hooks use $CLAUDE_PROJECT_DIR paths
# which don't resolve correctly through symlinks.
copy_claude_directory() {
    local worktree_path="$1"
    local base_repo="$2"

    # Check if base repo has .claude directory
    if [ -d "$base_repo/.claude" ]; then
        # If worktree has a symlink, replace it entirely
        if [ -L "$worktree_path/.claude" ]; then
            echo "Removing .claude symlink in worktree"
            rm -f "$worktree_path/.claude"
        fi

        # Merge: copy base repo .claude/ into worktree without deleting existing files.
        # The -r flag copies recursively, and we overwrite base repo versions but keep
        # worktree-only files (like hooks from the checked-out branch).
        echo "Merging .claude directory into worktree: $base_repo/.claude -> $worktree_path/.claude"
        mkdir -p "$worktree_path/.claude"
        cp -r "$base_repo/.claude/"* "$worktree_path/.claude/" 2>/dev/null || true
        # Also copy hidden files
        cp -r "$base_repo/.claude/".* "$worktree_path/.claude/" 2>/dev/null || true

        # Fix hook script permissions — hooks must be executable
        if [ -d "$worktree_path/.claude/hooks" ]; then
            find "$worktree_path/.claude/hooks" -type f \( -name "*.sh" -o -name "*.py" \) -exec chmod +x {} \; 2>/dev/null || true
            local hook_count
            hook_count=$(find "$worktree_path/.claude/hooks" -type f 2>/dev/null | wc -l)
            echo "  Fixed permissions on $hook_count hook file(s)"
        fi

        # Force sync to ensure EFS has propagated the files
        sync

        # Verify the copy succeeded
        if [ -d "$worktree_path/.claude" ]; then
            echo "  .claude directory merged successfully"
            if [ -d "$worktree_path/.claude/commands" ]; then
                local cmd_count=$(ls -1 "$worktree_path/.claude/commands" 2>/dev/null | wc -l)
                echo "  Found $cmd_count command(s) in .claude/commands/"
            fi
            if [ -d "$worktree_path/.claude/hooks" ]; then
                echo "  Found hooks directory"
            fi
        else
            echo "  ERROR: Failed to copy .claude directory!"
        fi
    else
        echo "No .claude directory in base repo - skipping copy"
        # Even without a base repo .claude/, the worktree might have one from git checkout.
        # Ensure hook permissions are still fixed.
        if [ -d "$worktree_path/.claude/hooks" ]; then
            find "$worktree_path/.claude/hooks" -type f \( -name "*.sh" -o -name "*.py" \) -exec chmod +x {} \; 2>/dev/null || true
        fi
    fi
}
# Check if container name has worker ID suffix (e.g., enkai-1 -> profile=enkai, worker=1)
# Returns: sets PROFILE_NAME and WORKER_ID variables, or empty if no worker suffix
parse_container_name() {
    PROFILE_NAME=""
    WORKER_ID=""

    # Check if CONTAINER_NAME matches pattern: name-number
    if [[ "$CONTAINER_NAME" =~ ^(.+)-([0-9]+)$ ]]; then
        PROFILE_NAME="${BASH_REMATCH[1]}"
        WORKER_ID="${BASH_REMATCH[2]}"
        echo "Container name parsed: profile=$PROFILE_NAME, worker=$WORKER_ID"
    fi
}

# Try to use a pre-warmed worktree (created by prewarm.sh)
# Returns 0 if successful, 1 if no pre-warmed worktree found
setup_prewarmed_worktree() {
    parse_container_name

    if [ -z "$PROFILE_NAME" ] || [ -z "$WORKER_ID" ]; then
        echo "No worker ID in container name - not using pre-warmed worktree"
        return 1
    fi

    local prewarmed_base="/workspace/repos/$PROFILE_NAME/base"
    local prewarmed_worktree="/workspace/repos/$PROFILE_NAME/worktrees/$WORKER_ID"

    # Check if pre-warmed worktree exists
    if [ ! -d "$prewarmed_worktree" ] || [ ! -f "$prewarmed_worktree/.git" ]; then
        echo "No pre-warmed worktree found at $prewarmed_worktree"
        return 1
    fi

    echo "=== Using pre-warmed worktree ==="
    echo "Profile: $PROFILE_NAME"
    echo "Worker: $WORKER_ID"
    echo "Worktree: $prewarmed_worktree"

    # Update the worktree
    echo "Pulling latest changes..."
    git -C "$prewarmed_worktree" pull --ff-only 2>/dev/null || true

    # Install post-checkout hook so future worktrees get .claude/ automatically
    install_worktree_hook "$prewarmed_base"

    # Copy fresh .claude directory
    if [ -d "$prewarmed_base/.claude" ]; then
        copy_claude_directory "$prewarmed_worktree" "$prewarmed_base"
    fi

    # Set environment
    WORKTREE_PATH="$prewarmed_worktree"
    REPO_BASE="$prewarmed_base"
    export WORKTREE_PATH REPO_BASE

    echo "Working directory: $WORKTREE_PATH"
    cd "$WORKTREE_PATH"

    return 0
}

setup_worktree_from_clone() {
    local repo_url="$1"
    local branch="${2:-main}"

    echo "Setting up worktree for container: $CONTAINER_NAME (from clone)"

    # Clone repo to base location if not exists, or re-clone if URL changed
    if [ ! -d "$REPO_BASE/.git" ]; then
        # Remove stale directory if it exists without .git (e.g., from a failed clone)
        if [ -d "$REPO_BASE" ]; then
            echo "Removing stale directory at $REPO_BASE (no .git found)..."
            rm -rf "$REPO_BASE"
        fi
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
            echo "Repository base exists, fetching and pulling updates..."
            git -C "$REPO_BASE" fetch --all 2>/dev/null || true
            git -C "$REPO_BASE" pull --ff-only 2>/dev/null || true
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

    # Install post-checkout hook so future worktrees get .claude/ automatically
    install_worktree_hook "$REPO_BASE"

    # Link .claude directory for hooks and settings
    copy_claude_directory "$WORKTREE_PATH" "$REPO_BASE"

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

    # Install post-checkout hook so future worktrees get .claude/ automatically
    install_worktree_hook "/workspace"

    # Link .claude directory for hooks and settings
    copy_claude_directory "$WORKTREE_PATH" "/workspace"

    echo "Working directory: $WORKTREE_PATH"
    cd "$WORKTREE_PATH"
}

# Setup worktree based on configuration
# Priority: 1) Pre-warmed worktree, 2) Clone from URL, 3) Existing local repo
if setup_prewarmed_worktree; then
    # Successfully using pre-warmed worktree (fastest path)
    WORK_DIR="$WORKTREE_PATH"
elif [ -n "$GIT_REPO" ]; then
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

# Export CLAUDE_PROJECT_DIR so hooks resolve paths correctly in worktrees.
# Claude Code has known issues where CLAUDE_PROJECT_DIR is empty or points
# to the base repo instead of the worktree (GH issues #9447, #12885, #16089).
export CLAUDE_PROJECT_DIR="$WORK_DIR"
echo "CLAUDE_PROJECT_DIR=$CLAUDE_PROJECT_DIR"

# Copy baked-in skills to working directory (e.g., Pnyx)
if [ -d "/root/.claude/skills" ]; then
    mkdir -p "$WORK_DIR/.claude/skills"
    cp -r /root/.claude/skills/* "$WORK_DIR/.claude/skills/" 2>/dev/null || true
    echo "Copied baked-in skills to $WORK_DIR/.claude/skills/"
fi

# Copy baked-in hooks to working directory (e.g., worktree env fix)
if [ -d "/root/.claude/hooks" ]; then
    mkdir -p "$WORK_DIR/.claude/hooks"
    cp -rn /root/.claude/hooks/* "$WORK_DIR/.claude/hooks/" 2>/dev/null || true
    chmod +x "$WORK_DIR/.claude/hooks/"*.sh 2>/dev/null || true
    echo "Copied baked-in hooks to $WORK_DIR/.claude/hooks/"
fi

# Copy community skills and scripts to working directory
copy_community_skills_to_workdir "$WORK_DIR"

# Install and register plugins now that we know the working directory
install_plugins "$WORK_DIR"

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

# Configure tmux directory for session handling
# Must be set before starting status server so tmux commands work
export TMUX_TMPDIR=/tmp/tmux-sessions
mkdir -p "$TMUX_TMPDIR"

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

# Start bash terminal with tmux persistence
echo "Starting bash terminal on port $BASH_PORT (path: $BASH_BASE_PATH)..."
ttyd -p "${BASH_PORT}" -W \
    -t fontSize=16 \
    -t fontFamily="Consolas, 'Courier New', monospace" \
    -t theme="${TTYD_THEME}" \
    --ping-interval 60 \
    --base-path "$BASH_BASE_PATH" \
    tmux-session.sh frank-bash bash &
BASH_PID=$!

# Wait for bash terminal to be ready
wait_for_port "$BASH_PORT" "Bash terminal" 10

# Final verification before starting Claude
echo "=== Pre-flight checks ==="
echo "Working directory: $(pwd)"
if [ -d ".claude" ]; then
    echo "  .claude directory: OK"
    if [ -d ".claude/commands" ]; then
        echo "  .claude/commands: OK ($(ls -1 .claude/commands 2>/dev/null | wc -l) files)"
    else
        echo "  .claude/commands: MISSING"
    fi
else
    echo "  .claude directory: MISSING"
fi

# Start Claude terminal (foreground) with tmux persistence
echo "Starting Claude terminal on port $TTYD_PORT (path: $CLAUDE_BASE_PATH)..."
echo "=== Frank ECS Container Ready ==="

# Note: user-session.sh is available for per-user workspace isolation
# Currently using shared session; per-user sessions can be enabled by changing to:
#   user-session.sh frank-claude claude
# which will create /workspace/users/{user_short_id}/ directories per user

exec ttyd -p "${TTYD_PORT}" -W \
    -t fontSize=16 \
    -t fontFamily="Consolas, 'Courier New', monospace" \
    -t theme="${TTYD_THEME}" \
    --ping-interval 60 \
    --base-path "$CLAUDE_BASE_PATH" \
    tmux-session.sh frank-claude claude
