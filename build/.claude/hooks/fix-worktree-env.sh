#!/bin/bash
# SessionStart hook: Fix CLAUDE_PROJECT_DIR for worktree environments.
#
# Claude Code has known issues resolving CLAUDE_PROJECT_DIR in git worktrees
# (GH issues #9447, #12885, #16089). This hook ensures the variable is set
# correctly by writing it to CLAUDE_ENV_FILE, which persists across all
# subsequent tool calls in the session.

set -e

# Read hook input from stdin
INPUT=$(cat)

# Get the env file path from input (only available in SessionStart)
ENV_FILE=$(echo "$INPUT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('env_file',''))" 2>/dev/null || true)

if [ -z "$ENV_FILE" ]; then
    # No env file available — output JSON and exit
    echo '{"decision":"allow"}'
    exit 0
fi

# Determine correct project directory
# Priority: 1) git toplevel, 2) CWD from hook input, 3) existing CLAUDE_PROJECT_DIR
PROJECT_DIR=""
CWD=$(echo "$INPUT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('cwd',''))" 2>/dev/null || true)

if [ -n "$CWD" ] && [ -d "$CWD" ]; then
    PROJECT_DIR=$(git -C "$CWD" rev-parse --show-toplevel 2>/dev/null || echo "$CWD")
elif [ -n "$CLAUDE_PROJECT_DIR" ]; then
    PROJECT_DIR="$CLAUDE_PROJECT_DIR"
fi

# Write to env file so it persists for all hooks in this session
if [ -n "$PROJECT_DIR" ] && [ -n "$ENV_FILE" ]; then
    echo "CLAUDE_PROJECT_DIR=$PROJECT_DIR" >> "$ENV_FILE"
fi

echo '{"decision":"allow"}'
