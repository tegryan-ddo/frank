#!/bin/bash
# Pre-warm script for Frank ECS containers
# Creates a shared base repo and multiple worktrees to speed up boot time
#
# Usage: prewarm.sh <profile> <repo-url> [num-workers] [branch]
# Example: prewarm.sh enkai https://github.com/user/repo.git 4 main

set -e

PROFILE="$1"
REPO_URL="$2"
NUM_WORKERS="${3:-4}"
BRANCH="${4:-main}"

if [ -z "$PROFILE" ] || [ -z "$REPO_URL" ]; then
    echo "Usage: prewarm.sh <profile> <repo-url> [num-workers] [branch]"
    echo "Example: prewarm.sh enkai https://github.com/user/repo.git 4 main"
    exit 1
fi

PROFILE_DIR="/workspace/repos/$PROFILE"
BASE_REPO="$PROFILE_DIR/base"
WORKTREES_DIR="$PROFILE_DIR/worktrees"

echo "=== Pre-warming profile: $PROFILE ==="
echo "  Repository: $REPO_URL"
echo "  Branch: $BRANCH"
echo "  Workers: $NUM_WORKERS"
echo ""

# Clone or update base repo
if [ -d "$BASE_REPO/.git" ]; then
    echo "Updating existing base repo..."
    git -C "$BASE_REPO" fetch --all
    git -C "$BASE_REPO" pull --ff-only || true
else
    echo "Cloning base repo..."
    mkdir -p "$PROFILE_DIR"
    git clone "$REPO_URL" "$BASE_REPO"
fi

# Checkout the target branch in base repo
echo "Checking out branch: $BRANCH"
git -C "$BASE_REPO" checkout "$BRANCH" 2>/dev/null || git -C "$BASE_REPO" checkout -b "$BRANCH" "origin/$BRANCH"
git -C "$BASE_REPO" pull --ff-only || true

# Create worktrees directory
mkdir -p "$WORKTREES_DIR"

# Create worktrees for each worker
for i in $(seq 1 "$NUM_WORKERS"); do
    WORKTREE_PATH="$WORKTREES_DIR/$i"

    if [ -d "$WORKTREE_PATH" ]; then
        echo "Worktree $i already exists, updating..."
        if [ -f "$WORKTREE_PATH/.git" ]; then
            git -C "$WORKTREE_PATH" pull --ff-only 2>/dev/null || true
        else
            echo "  Invalid worktree, recreating..."
            rm -rf "$WORKTREE_PATH"
            git -C "$BASE_REPO" worktree add "$WORKTREE_PATH" "$BRANCH" 2>/dev/null || \
                git -C "$BASE_REPO" worktree add -b "worker-$i" "$WORKTREE_PATH" "$BRANCH"
        fi
    else
        echo "Creating worktree $i..."
        git -C "$BASE_REPO" worktree add "$WORKTREE_PATH" "$BRANCH" 2>/dev/null || \
            git -C "$BASE_REPO" worktree add -b "worker-$i" "$WORKTREE_PATH" "$BRANCH"
    fi

    # Copy .claude directory to worktree
    if [ -d "$BASE_REPO/.claude" ]; then
        echo "  Copying .claude to worktree $i..."
        rm -rf "$WORKTREE_PATH/.claude"
        cp -r "$BASE_REPO/.claude" "$WORKTREE_PATH/.claude"
    fi
done

# Sync to ensure EFS has propagated
sync

echo ""
echo "=== Pre-warm complete ==="
echo "Base repo: $BASE_REPO"
echo "Worktrees: $WORKTREES_DIR/1 through $WORKTREES_DIR/$NUM_WORKERS"
echo ""
echo "To use these worktrees, start containers with names like:"
echo "  ${PROFILE}-1, ${PROFILE}-2, ${PROFILE}-3, ${PROFILE}-4"

# Show disk usage
echo ""
echo "Disk usage:"
du -sh "$PROFILE_DIR"
du -sh "$BASE_REPO"
du -sh "$WORKTREES_DIR"/* 2>/dev/null || true
