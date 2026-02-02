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

RESULTS_DIR="/tmp/codex-results"
mkdir -p "$RESULTS_DIR"

MODEL="${CODEX_MODEL:-codex-mini-latest}"
TIMESTAMP="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

echo "=== Codex Task Execution ==="
echo "Model:     $MODEL"
echo "Prompt:    $TASK_PROMPT"
echo "Timestamp: $TIMESTAMP"
echo "============================"

EXIT_CODE=0
codex exec \
    --full-auto \
    --json \
    --model "$MODEL" \
    -o "$RESULTS_DIR/result.json" \
    "$TASK_PROMPT" \
    2>&1 || EXIT_CODE=$?

# Emit result as a structured log line that can be parsed from CloudWatch
# The FRANK_RESULT marker lets the CLI extract the plan/result from logs
COMPLETED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

echo "=== Task Complete ==="
echo "Exit code: $EXIT_CODE"

# Output the result file content as a marked block for CloudWatch parsing
if [ -f "$RESULTS_DIR/result.json" ]; then
    echo "FRANK_RESULT_BEGIN"
    cat "$RESULTS_DIR/result.json"
    echo ""
    echo "FRANK_RESULT_END"
fi

# Output structured summary as a marked block
echo "FRANK_SUMMARY_BEGIN"
cat <<EOF
{
  "task_prompt": $(printf '%s' "$TASK_PROMPT" | jq -Rs .),
  "exit_code": $EXIT_CODE,
  "model": "$MODEL",
  "container": "$CONTAINER_NAME",
  "timestamp": "$TIMESTAMP",
  "completed_at": "$COMPLETED_AT"
}
EOF
echo "FRANK_SUMMARY_END"

exit "$EXIT_CODE"
