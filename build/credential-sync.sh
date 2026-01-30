#!/bin/bash
# credential-sync.sh - Syncs Claude credentials across containers via Secrets Manager
# Runs as a background process; best-effort, failures don't affect container operation.

set -o pipefail

CRED_FILE="$HOME/.claude/.credentials.json"
SECRET_ID="/frank/claude-credentials"
LOG_FILE="/tmp/credential-sync.log"
PULL_INTERVAL=60   # seconds between Secrets Manager pulls
LOCAL_CHECK=5       # seconds between local file mtime checks
REGION="${AWS_REGION:-us-east-1}"

log() {
    echo "[$(date -u '+%Y-%m-%d %H:%M:%S')] $*" >> "$LOG_FILE"
}

# Only run in ECS
if [ -z "$ECS_CONTAINER_METADATA_URI_V4" ]; then
    echo "Not running in ECS - credential sync disabled" >> "$LOG_FILE"
    exit 0
fi

log "Credential sync started (pull every ${PULL_INTERVAL}s, local check every ${LOCAL_CHECK}s)"

get_file_hash() {
    if [ -f "$CRED_FILE" ]; then
        sha256sum "$CRED_FILE" 2>/dev/null | cut -d' ' -f1
    else
        echo ""
    fi
}

get_file_mtime() {
    if [ -f "$CRED_FILE" ]; then
        stat -c %Y "$CRED_FILE" 2>/dev/null || echo "0"
    else
        echo "0"
    fi
}

push_to_secrets_manager() {
    local content
    content=$(cat "$CRED_FILE" 2>/dev/null) || return 1
    [ -z "$content" ] && return 1

    if aws secretsmanager put-secret-value \
        --secret-id "$SECRET_ID" \
        --secret-string "$content" \
        --region "$REGION" >/dev/null 2>&1; then
        log "Pushed credentials to Secrets Manager"
        return 0
    else
        log "ERROR: Failed to push credentials to Secrets Manager"
        return 1
    fi
}

pull_from_secrets_manager() {
    local remote_content
    remote_content=$(aws secretsmanager get-secret-value \
        --secret-id "$SECRET_ID" \
        --query SecretString \
        --output text \
        --region "$REGION" 2>/dev/null) || return 1

    [ -z "$remote_content" ] && return 1

    local remote_hash
    remote_hash=$(echo -n "$remote_content" | sha256sum | cut -d' ' -f1)
    local local_hash
    local_hash=$(get_file_hash)

    if [ "$remote_hash" != "$local_hash" ]; then
        mkdir -p "$(dirname "$CRED_FILE")"
        echo "$remote_content" > "$CRED_FILE"
        chmod 600 "$CRED_FILE"
        log "Pulled updated credentials from Secrets Manager"
        # Update tracked mtime so we don't re-push what we just pulled
        LAST_MTIME=$(get_file_mtime)
        LAST_HASH="$remote_hash"
        return 0
    fi

    return 1
}

# Initialize tracking
LAST_MTIME=$(get_file_mtime)
LAST_HASH=$(get_file_hash)
SECONDS_SINCE_PULL=0

while true; do
    sleep "$LOCAL_CHECK"
    SECONDS_SINCE_PULL=$((SECONDS_SINCE_PULL + LOCAL_CHECK))

    # Check for local changes (user ran /login)
    CURRENT_MTIME=$(get_file_mtime)
    if [ "$CURRENT_MTIME" != "$LAST_MTIME" ]; then
        CURRENT_HASH=$(get_file_hash)
        if [ "$CURRENT_HASH" != "$LAST_HASH" ]; then
            log "Local credential change detected"
            push_to_secrets_manager
            LAST_HASH="$CURRENT_HASH"
        fi
        LAST_MTIME="$CURRENT_MTIME"
    fi

    # Periodically pull from Secrets Manager
    if [ "$SECONDS_SINCE_PULL" -ge "$PULL_INTERVAL" ]; then
        SECONDS_SINCE_PULL=0
        pull_from_secrets_manager || true
    fi
done
