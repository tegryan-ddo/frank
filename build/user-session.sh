#!/bin/bash
# user-session.sh - Wraps tmux-session.sh with per-user workspace isolation
#
# Usage: user-session.sh <base-session-name> <command> [args...]
#
# Environment variables (set by the web frontend via status-server):
#   USER_ID       - Full Cognito user ID (sub)
#   USER_SHORT_ID - 8-character hash of user ID (for paths/sessions)
#   USER_NAME     - Display name for the user
#
# If no user info is provided, falls back to "shared" workspace.
#
# Each user gets:
#   - Their own workspace: /workspace/users/{short_id}/
#   - Their own tmux session: {base-session-name}-{short_id}
#   - A copy of .claude config from the base repo

BASE_SESSION_NAME="$1"
shift
COMMAND="$@"

if [ -z "$BASE_SESSION_NAME" ] || [ -z "$COMMAND" ]; then
    echo "Usage: user-session.sh <base-session-name> <command> [args...]"
    exit 1
fi

# Get user info from environment (set by ttyd or web server)
USER_ID="${USER_ID:-}"
SHORT_ID="${USER_SHORT_ID:-}"

# If no short_id provided but user_id exists, generate short_id
if [ -z "$SHORT_ID" ] && [ -n "$USER_ID" ]; then
    SHORT_ID=$(echo -n "$USER_ID" | sha256sum | cut -c1-8)
fi

# Fall back to "shared" if no user info
if [ -z "$SHORT_ID" ]; then
    SHORT_ID="shared"
fi

# Construct session name and workspace path
SESSION_NAME="${BASE_SESSION_NAME}-${SHORT_ID}"
USER_WORKSPACE="/workspace/users/${SHORT_ID}"

# Find the base repo (could be in several locations)
BASE_REPO=""
if [ -n "$WORKTREE_PATH" ] && [ -d "$WORKTREE_PATH" ]; then
    BASE_REPO="$WORKTREE_PATH"
elif [ -n "$REPO_BASE" ] && [ -d "$REPO_BASE" ]; then
    BASE_REPO="$REPO_BASE"
elif [ -d "/workspace/repos/$CONTAINER_NAME/work" ]; then
    BASE_REPO="/workspace/repos/$CONTAINER_NAME/work"
elif [ -d "/workspace/.git" ]; then
    BASE_REPO="/workspace"
fi

# Create user workspace if it doesn't exist
if [ ! -d "$USER_WORKSPACE" ]; then
    echo "Creating user workspace: $USER_WORKSPACE"
    mkdir -p "$USER_WORKSPACE"

    # If we have a base repo, set up git worktree for the user
    if [ -n "$BASE_REPO" ] && [ -d "$BASE_REPO/.git" -o -f "$BASE_REPO/.git" ]; then
        echo "Setting up git worktree for user..."

        # Find the actual git directory (handle worktree case)
        if [ -f "$BASE_REPO/.git" ]; then
            # This is already a worktree, get the main repo
            MAIN_GIT_DIR=$(cat "$BASE_REPO/.git" | grep "gitdir:" | cut -d' ' -f2)
            if [ -n "$MAIN_GIT_DIR" ]; then
                # Extract the main worktree path
                MAIN_REPO=$(dirname "$(dirname "$MAIN_GIT_DIR")")
            else
                MAIN_REPO="$BASE_REPO"
            fi
        else
            MAIN_REPO="$BASE_REPO"
        fi

        # Get current branch
        CURRENT_BRANCH=$(git -C "$BASE_REPO" branch --show-current 2>/dev/null || echo "main")

        # Try to create worktree
        if git -C "$MAIN_REPO" worktree add "$USER_WORKSPACE" "$CURRENT_BRANCH" 2>/dev/null; then
            echo "Created git worktree at $USER_WORKSPACE"
        elif git -C "$MAIN_REPO" worktree add -b "user-${SHORT_ID}" "$USER_WORKSPACE" "$CURRENT_BRANCH" 2>/dev/null; then
            echo "Created git worktree with new branch user-${SHORT_ID}"
        else
            # Worktree creation failed, just copy files
            echo "Git worktree failed, copying files instead..."
            cp -r "$BASE_REPO"/* "$USER_WORKSPACE/" 2>/dev/null || true
            cp -r "$BASE_REPO"/.[!.]* "$USER_WORKSPACE/" 2>/dev/null || true
        fi
    fi
fi

# Copy/update .claude directory from base repo
if [ -n "$BASE_REPO" ] && [ -d "$BASE_REPO/.claude" ]; then
    if [ ! -d "$USER_WORKSPACE/.claude" ] || [ "$BASE_REPO/.claude" -nt "$USER_WORKSPACE/.claude" ]; then
        echo "Copying .claude config to user workspace..."
        rm -rf "$USER_WORKSPACE/.claude" 2>/dev/null || true
        cp -r "$BASE_REPO/.claude" "$USER_WORKSPACE/.claude"
    fi
fi

# Also ensure the user-level .claude directory has proper symlinks
if [ -d "/root/.claude" ] && [ ! -L "$USER_WORKSPACE/.claude/settings.json" ]; then
    # Copy any additional settings from root's .claude
    for f in /root/.claude/*.json; do
        if [ -f "$f" ]; then
            fname=$(basename "$f")
            if [ ! -f "$USER_WORKSPACE/.claude/$fname" ]; then
                cp "$f" "$USER_WORKSPACE/.claude/$fname" 2>/dev/null || true
            fi
        fi
    done
fi

# Change to user workspace
cd "$USER_WORKSPACE"

# Export environment for child processes
export USER_WORKSPACE
export USER_SHORT_ID="$SHORT_ID"

# Log the session start
echo "Starting session '$SESSION_NAME' in workspace '$USER_WORKSPACE'"

# Delegate to tmux-session.sh with the user-specific session name
exec tmux-session.sh "$SESSION_NAME" $COMMAND
