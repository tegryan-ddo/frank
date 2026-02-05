#!/bin/bash
# pnyx-credential-sync.sh - Syncs Pnyx API keys across containers via Secrets Manager
# Supports per-agent keys: /frank/pnyx-api-key/{agent-name}
# Runs as a background process; best-effort, failures don't affect container operation.

set -o pipefail

CRED_FILE="$HOME/.config/pnyx/credentials.json"
LOG_FILE="/tmp/pnyx-credential-sync.log"
PULL_INTERVAL=60   # seconds between Secrets Manager pulls
LOCAL_CHECK=5      # seconds between local file mtime checks
REGION="${AWS_REGION:-us-east-1}"
API_URL="https://pnyx.digitaldevops.io"

# Agent name from container name (e.g., "work", "enkai")
AGENT_NAME="${CONTAINER_NAME:-}"
AGENT_SECRET_ID=""

if [ -n "$AGENT_NAME" ]; then
    AGENT_SECRET_ID="/frank/pnyx-api-key/$AGENT_NAME"
fi

log() {
    echo "[$(date -u '+%Y-%m-%d %H:%M:%S')] $*" >> "$LOG_FILE"
}

# Only run in ECS
if [ -z "$ECS_CONTAINER_METADATA_URI_V4" ]; then
    echo "Not running in ECS - Pnyx credential sync disabled" >> "$LOG_FILE"
    exit 0
fi

log "Pnyx credential sync started"
log "  Agent: ${AGENT_NAME:-<none>}"
log "  Agent secret: ${AGENT_SECRET_ID:-<none>}"
log "  Pull interval: ${PULL_INTERVAL}s, local check: ${LOCAL_CHECK}s"

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

# Extract API key from credentials file
get_local_api_key() {
    if [ -f "$CRED_FILE" ]; then
        jq -r '.api_key // empty' "$CRED_FILE" 2>/dev/null
    fi
}

# Write credentials file with API key
write_credentials() {
    local api_key="$1"
    [ -z "$api_key" ] && return 1

    mkdir -p "$(dirname "$CRED_FILE")"
    cat > "$CRED_FILE" <<EOF
{
  "api_key": "$api_key",
  "api_url": "$API_URL"
}
EOF
    chmod 600 "$CRED_FILE"
    return 0
}

# Try to get secret value, returns empty on failure
get_secret() {
    local secret_id="$1"
    aws secretsmanager get-secret-value \
        --secret-id "$secret_id" \
        --query SecretString \
        --output text \
        --region "$REGION" 2>/dev/null
}

# Check if secret exists
secret_exists() {
    local secret_id="$1"
    aws secretsmanager describe-secret \
        --secret-id "$secret_id" \
        --region "$REGION" >/dev/null 2>&1
}

# Create a new secret
create_secret() {
    local secret_id="$1"
    local value="$2"

    aws secretsmanager create-secret \
        --name "$secret_id" \
        --secret-string "$value" \
        --description "Pnyx API key for agent: $AGENT_NAME" \
        --region "$REGION" >/dev/null 2>&1
}

# Update existing secret
update_secret() {
    local secret_id="$1"
    local value="$2"

    aws secretsmanager put-secret-value \
        --secret-id "$secret_id" \
        --secret-string "$value" \
        --region "$REGION" >/dev/null 2>&1
}

# Push local API key to agent-specific secret
push_to_secrets_manager() {
    local api_key
    api_key=$(get_local_api_key)
    [ -z "$api_key" ] && return 1
    [ -z "$AGENT_SECRET_ID" ] && return 1

    if secret_exists "$AGENT_SECRET_ID"; then
        if update_secret "$AGENT_SECRET_ID" "$api_key"; then
            log "Pushed API key to agent secret: $AGENT_SECRET_ID"
            return 0
        fi
    else
        if create_secret "$AGENT_SECRET_ID" "$api_key"; then
            log "Created agent secret: $AGENT_SECRET_ID"
            return 0
        fi
    fi

    log "ERROR: Failed to push API key to Secrets Manager"
    return 1
}

# Pull API key from Secrets Manager (agent-specific only)
pull_from_secrets_manager() {
    local remote_key=""
    local source=""

    # Try agent-specific secret first
    if [ -n "$AGENT_SECRET_ID" ]; then
        remote_key=$(get_secret "$AGENT_SECRET_ID")
        if [ -n "$remote_key" ]; then
            source="agent:$AGENT_SECRET_ID"
        fi
    fi

    # No remote key found
    if [ -z "$remote_key" ]; then
        return 1
    fi

    # Compare with local
    local local_key
    local_key=$(get_local_api_key)

    if [ "$remote_key" != "$local_key" ]; then
        write_credentials "$remote_key"
        log "Pulled API key from $source"
        # Update tracked state so we don't re-push what we just pulled
        LAST_MTIME=$(get_file_mtime)
        LAST_HASH=$(get_file_hash)
        return 0
    fi

    return 1
}

# Initial setup: try to get credentials from Secrets Manager or env var
initial_setup() {
    log "Running initial credential setup..."

    # If we already have a local file, use it as baseline
    if [ -f "$CRED_FILE" ]; then
        local existing_key
        existing_key=$(get_local_api_key)
        if [ -n "$existing_key" ]; then
            log "Found existing local credentials"
            return 0
        fi
    fi

    # Try to pull from Secrets Manager
    if pull_from_secrets_manager; then
        log "Initial credentials loaded from Secrets Manager"
        return 0
    fi

    # Fall back to PNYX_API_KEY env var (for backwards compatibility)
    if [ -n "$PNYX_API_KEY" ]; then
        write_credentials "$PNYX_API_KEY"
        log "Initial credentials set from PNYX_API_KEY env var"
        return 0
    fi

    log "No initial credentials found - agent can use /pnyx engage to register"
    return 1
}

# Run initial setup
initial_setup

# Initialize tracking
LAST_MTIME=$(get_file_mtime)
LAST_HASH=$(get_file_hash)
SECONDS_SINCE_PULL=0

while true; do
    sleep "$LOCAL_CHECK"
    SECONDS_SINCE_PULL=$((SECONDS_SINCE_PULL + LOCAL_CHECK))

    # Check for local changes (agent updated credentials via /pnyx engage)
    CURRENT_MTIME=$(get_file_mtime)
    if [ "$CURRENT_MTIME" != "$LAST_MTIME" ]; then
        CURRENT_HASH=$(get_file_hash)
        if [ "$CURRENT_HASH" != "$LAST_HASH" ]; then
            local_key=$(get_local_api_key)
            if [ -n "$local_key" ]; then
                log "Local credential change detected"
                push_to_secrets_manager
                LAST_HASH="$CURRENT_HASH"
            fi
        fi
        LAST_MTIME="$CURRENT_MTIME"
    fi

    # Periodically pull from Secrets Manager
    if [ "$SECONDS_SINCE_PULL" -ge "$PULL_INTERVAL" ]; then
        SECONDS_SINCE_PULL=0
        pull_from_secrets_manager || true
    fi
done
