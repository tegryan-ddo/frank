#!/bin/bash
# codex-credential-sync.sh - Syncs Codex credentials across containers via Secrets Manager
# Runs as a background process; best-effort, failures don't affect container operation.
#
# Codex stores credentials in ~/.codex/ after device auth login.
# This script syncs them to Secrets Manager so they persist across container restarts.

set -o pipefail

CODEX_DIR="$HOME/.codex"
SECRET_ID="/frank/codex-credentials"
LOG_FILE="/tmp/codex-credential-sync.log"
PULL_INTERVAL=60   # seconds between Secrets Manager pulls
LOCAL_CHECK=5      # seconds between local file mtime checks
REGION="${AWS_REGION:-us-east-1}"

log() {
    echo "[$(date -u '+%Y-%m-%d %H:%M:%S')] $*" >> "$LOG_FILE"
}

# Only run in ECS
if [ -z "$ECS_CONTAINER_METADATA_URI_V4" ]; then
    echo "Not running in ECS - Codex credential sync disabled" >> "$LOG_FILE"
    exit 0
fi

log "Codex credential sync started (pull every ${PULL_INTERVAL}s, local check every ${LOCAL_CHECK}s)"

# Get hash of all credential-related files in ~/.codex/
get_codex_hash() {
    if [ -d "$CODEX_DIR" ]; then
        # Hash all json files and any auth-related files
        find "$CODEX_DIR" -maxdepth 1 -type f \( -name "*.json" -o -name "*.toml" -o -name "auth*" -o -name "cred*" \) -exec cat {} \; 2>/dev/null | sha256sum | cut -d' ' -f1
    else
        echo ""
    fi
}

get_codex_mtime() {
    if [ -d "$CODEX_DIR" ]; then
        find "$CODEX_DIR" -maxdepth 1 -type f \( -name "*.json" -o -name "*.toml" -o -name "auth*" -o -name "cred*" \) -exec stat -c %Y {} \; 2>/dev/null | sort -rn | head -1
    else
        echo "0"
    fi
}

# Pack all credential files into a JSON blob for storage
pack_credentials() {
    local packed="{}"

    for file in "$CODEX_DIR"/*.json "$CODEX_DIR"/*.toml "$CODEX_DIR"/auth* "$CODEX_DIR"/cred*; do
        if [ -f "$file" ]; then
            local basename=$(basename "$file")
            local content=$(base64 -w0 "$file" 2>/dev/null)
            packed=$(echo "$packed" | jq --arg name "$basename" --arg content "$content" '. + {($name): $content}')
        fi
    done 2>/dev/null

    echo "$packed"
}

# Unpack credentials from JSON blob
unpack_credentials() {
    local packed="$1"

    mkdir -p "$CODEX_DIR"
    chmod 700 "$CODEX_DIR"

    for key in $(echo "$packed" | jq -r 'keys[]'); do
        local content=$(echo "$packed" | jq -r --arg k "$key" '.[$k]')
        echo "$content" | base64 -d > "$CODEX_DIR/$key"
        chmod 600 "$CODEX_DIR/$key"
    done
}

push_to_secrets_manager() {
    local packed
    packed=$(pack_credentials)

    # Don't push empty credentials
    if [ "$packed" = "{}" ]; then
        log "No Codex credentials to push"
        return 1
    fi

    if aws secretsmanager put-secret-value \
        --secret-id "$SECRET_ID" \
        --secret-string "$packed" \
        --region "$REGION" >/dev/null 2>&1; then
        log "Pushed Codex credentials to Secrets Manager"
        return 0
    else
        # Secret might not exist yet, try to create it
        if aws secretsmanager create-secret \
            --name "$SECRET_ID" \
            --description "Codex CLI credentials (device auth)" \
            --secret-string "$packed" \
            --region "$REGION" >/dev/null 2>&1; then
            log "Created and pushed Codex credentials to Secrets Manager"
            return 0
        fi
        log "ERROR: Failed to push Codex credentials to Secrets Manager"
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
    [ "$remote_content" = "{}" ] && return 1

    local remote_hash
    remote_hash=$(echo -n "$remote_content" | sha256sum | cut -d' ' -f1)
    local local_hash
    local_hash=$(get_codex_hash)

    if [ "$remote_hash" != "$local_hash" ]; then
        unpack_credentials "$remote_content"
        log "Pulled updated Codex credentials from Secrets Manager"
        # Update tracked values
        LAST_MTIME=$(get_codex_mtime)
        LAST_HASH="$remote_hash"
        return 0
    fi

    return 1
}

# Initial pull on startup
log "Attempting initial pull of Codex credentials..."
pull_from_secrets_manager && log "Restored Codex credentials from Secrets Manager" || log "No existing Codex credentials in Secrets Manager"

# Initialize tracking
LAST_MTIME=$(get_codex_mtime)
LAST_HASH=$(get_codex_hash)
SECONDS_SINCE_PULL=0

while true; do
    sleep "$LOCAL_CHECK"
    SECONDS_SINCE_PULL=$((SECONDS_SINCE_PULL + LOCAL_CHECK))

    # Check for local changes (user ran codex login --device-auth)
    CURRENT_MTIME=$(get_codex_mtime)
    if [ "$CURRENT_MTIME" != "$LAST_MTIME" ] && [ "$CURRENT_MTIME" != "0" ]; then
        CURRENT_HASH=$(get_codex_hash)
        if [ "$CURRENT_HASH" != "$LAST_HASH" ] && [ -n "$CURRENT_HASH" ]; then
            log "Local Codex credential change detected"
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
