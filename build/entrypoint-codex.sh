#!/bin/bash
set -euo pipefail

# Frank Codex Worker Entrypoint
# Headless task execution via codex exec

CONTAINER_NAME="${CONTAINER_NAME:-$(hostname)}"
REPO_BASE="/workspace/.repo"
WORK_DIR="/workspace"
WORKTREE_PATH=""

# ---------------------------------------------------------------------------
# Git Setup
# ---------------------------------------------------------------------------

git config --global init.defaultBranch main
git config --global --add safe.directory '*'

if [ -z "$(git config --global user.name 2>/dev/null)" ]; then
    git config --global user.name "Codex Worker"
fi
if [ -z "$(git config --global user.email 2>/dev/null)" ]; then
    git config --global user.email "codex-worker@frank.local"
fi

# Setup GitHub CLI-style credential helper if GH_TOKEN is set
if [ -n "${GH_TOKEN:-}" ]; then
    echo "GitHub token configured"
    git config --global url."https://${GH_TOKEN}@github.com/".insteadOf "https://github.com/"
fi

# ---------------------------------------------------------------------------
# Worktree Setup (simplified from main entrypoint)
# ---------------------------------------------------------------------------

if [ -n "${GIT_REPO:-}" ]; then
    BRANCH="${GIT_BRANCH:-main}"
    echo "Setting up worktree for container: $CONTAINER_NAME"

    # Clone repo if not already present
    if [ ! -d "$REPO_BASE/.git" ]; then
        echo "Cloning repository to $REPO_BASE..."
        git clone "$GIT_REPO" "$REPO_BASE"
    else
        echo "Repository base exists, fetching updates..."
        git -C "$REPO_BASE" fetch --all 2>/dev/null || true
    fi

    WORKTREE_PATH="/workspace/worktrees/$CONTAINER_NAME"

    if [ -d "$WORKTREE_PATH" ] && [ -f "$WORKTREE_PATH/.git" ]; then
        echo "Reusing existing worktree: $WORKTREE_PATH"
    else
        rm -rf "$WORKTREE_PATH" 2>/dev/null || true
        mkdir -p /workspace/worktrees
        echo "Creating worktree: $WORKTREE_PATH"
        git -C "$REPO_BASE" worktree add "$WORKTREE_PATH" "$BRANCH" 2>/dev/null || \
            git -C "$REPO_BASE" worktree add -b "$CONTAINER_NAME" "$WORKTREE_PATH" "$BRANCH" 2>/dev/null || \
            git -C "$REPO_BASE" worktree add "$WORKTREE_PATH" "origin/$BRANCH" 2>/dev/null || \
            git -C "$REPO_BASE" worktree add -b "$CONTAINER_NAME" "$WORKTREE_PATH" "origin/$BRANCH"
    fi

    WORK_DIR="$WORKTREE_PATH"
fi

cd "$WORK_DIR"
echo "Working directory: $(pwd)"

# ---------------------------------------------------------------------------
# Auth Check
# ---------------------------------------------------------------------------

if [ -n "${OPENAI_API_KEY:-}" ]; then
    echo "OpenAI API key configured"
    # Pre-authenticate Codex CLI with the API key
    mkdir -p ~/.codex
    printf '%s' "$OPENAI_API_KEY" | codex login --with-api-key 2>/dev/null || true
elif [ -f "${CODEX_AUTH_JSON:-/dev/null}" ]; then
    echo "Codex auth.json credentials configured"
    mkdir -p ~/.codex
    cp "$CODEX_AUTH_JSON" ~/.codex/auth.json
else
    echo "WARNING: No Codex authentication configured"
    echo "  Set OPENAI_API_KEY or mount auth.json via CODEX_AUTH_JSON"
fi

# ---------------------------------------------------------------------------
# Task Execution
# ---------------------------------------------------------------------------

if [ -z "${TASK_PROMPT:-}" ]; then
    echo "ERROR: TASK_PROMPT environment variable is not set"
    exit 1
fi

RESULTS_DIR="/workspace/results/${CONTAINER_NAME}"
mkdir -p "$RESULTS_DIR"

MODEL="${CODEX_MODEL:-codex-mini-latest}"
TIMESTAMP="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

echo "=== Codex Task Execution ==="
echo "Model:     $MODEL"
echo "Prompt:    $TASK_PROMPT"
echo "Results:   $RESULTS_DIR"
echo "Timestamp: $TIMESTAMP"
echo "============================"

EXIT_CODE=0
codex exec \
    --full-auto \
    --json \
    --model "$MODEL" \
    -o "$RESULTS_DIR/result.json" \
    "$TASK_PROMPT" \
    2>"$RESULTS_DIR/stderr.log" || EXIT_CODE=$?

# Write summary
cat > "$RESULTS_DIR/summary.json" <<EOF
{
  "task_prompt": $(printf '%s' "$TASK_PROMPT" | jq -Rs .),
  "exit_code": $EXIT_CODE,
  "model": "$MODEL",
  "container": "$CONTAINER_NAME",
  "timestamp": "$TIMESTAMP",
  "completed_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF

echo "=== Task Complete ==="
echo "Exit code: $EXIT_CODE"
echo "Summary:   $RESULTS_DIR/summary.json"

exit "$EXIT_CODE"
