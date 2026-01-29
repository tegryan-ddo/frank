#!/bin/bash
# tmux-session.sh - Wraps a command in a persistent tmux session
# Usage: tmux-session.sh <session-name> <command> [args...]
#
# If the tmux session already exists, attaches to it.
# If not, creates a new session running the specified command.
# This allows the process to survive web terminal disconnects.

SESSION_NAME="$1"
shift
COMMAND="$@"

if [ -z "$SESSION_NAME" ] || [ -z "$COMMAND" ]; then
    echo "Usage: tmux-session.sh <session-name> <command> [args...]"
    exit 1
fi

# Check if session already exists
if tmux has-session -t "$SESSION_NAME" 2>/dev/null; then
    # Session exists - attach to it
    exec tmux attach-session -t "$SESSION_NAME"
else
    # Create new session with the command
    exec tmux new-session -s "$SESSION_NAME" $COMMAND
fi
