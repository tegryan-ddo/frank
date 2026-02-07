#!/bin/bash
# credential-sync.sh - Syncs Claude credentials across containers via Secrets Manager
# and automatically refreshes expired OAuth tokens.
# Runs as a background process; best-effort, failures don't affect container operation.

set -o pipefail

CRED_FILE="$HOME/.claude/.credentials.json"
SECRET_ID="/frank/claude-credentials"
LOG_FILE="/tmp/credential-sync.log"
PULL_INTERVAL=60   # seconds between Secrets Manager pulls
LOCAL_CHECK=5       # seconds between local file mtime checks
REFRESH_BUFFER=1800 # refresh tokens 30 minutes before expiry
REGION="${AWS_REGION:-us-east-1}"

# Claude Code OAuth configuration (extracted from CLI binary)
TOKEN_URL="https://platform.claude.com/v1/oauth/token"
CLIENT_ID="9d1c250a-e61b-44d9-88ed-5944d1962f5e"
OAUTH_SCOPES="user:profile user:inference user:sessions:claude_code user:mcp_servers"

log() {
    echo "[$(date -u '+%Y-%m-%d %H:%M:%S')] $*" >> "$LOG_FILE"
}

# Only run in ECS
if [ -z "$ECS_CONTAINER_METADATA_URI_V4" ]; then
    echo "Not running in ECS - credential sync disabled" >> "$LOG_FILE"
    exit 0
fi

log "Credential sync started (pull every ${PULL_INTERVAL}s, local check every ${LOCAL_CHECK}s, refresh buffer ${REFRESH_BUFFER}s)"

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

# Refresh OAuth token if expired or expiring soon.
# Claude Code stores tokens in .credentials.json under claudeAiOauth.
# The CLI itself does NOT refresh tokens on headless machines (known bug),
# so we do it here using the same OAuth endpoint and client_id.
refresh_oauth_token() {
    [ -f "$CRED_FILE" ] || return 1

    local refresh_token expires_at now_ms threshold_ms
    refresh_token=$(jq -r '.claudeAiOauth.refreshToken // .refreshToken // empty' "$CRED_FILE" 2>/dev/null)
    expires_at=$(jq -r '.claudeAiOauth.expiresAt // .expiresAt // empty' "$CRED_FILE" 2>/dev/null)

    # Nothing to refresh if no refresh token
    [ -z "$refresh_token" ] && return 1

    # If no expiresAt, try to refresh anyway (token state unknown)
    now_ms=$(($(date +%s) * 1000))
    threshold_ms=$(( ($(date +%s) + REFRESH_BUFFER) * 1000 ))

    if [ -n "$expires_at" ] && [ "$expires_at" -gt "$threshold_ms" ] 2>/dev/null; then
        # Token still valid and not within refresh buffer
        return 1
    fi

    log "Token expired or expiring soon (expiresAt=${expires_at:-unknown}, now=${now_ms}), attempting refresh..."

    local response http_code body
    response=$(curl -s -w "\n%{http_code}" -X POST "$TOKEN_URL" \
        -H "Content-Type: application/json" \
        -d "{
            \"grant_type\": \"refresh_token\",
            \"refresh_token\": \"${refresh_token}\",
            \"client_id\": \"${CLIENT_ID}\",
            \"scope\": \"${OAUTH_SCOPES}\"
        }" 2>/dev/null)

    http_code=$(echo "$response" | tail -1)
    body=$(echo "$response" | sed '$d')

    if [ "$http_code" != "200" ]; then
        log "ERROR: Token refresh failed (HTTP ${http_code}): $(echo "$body" | jq -r '.error // .message // "unknown error"' 2>/dev/null)"
        return 1
    fi

    local new_access_token new_refresh_token expires_in new_expires_at
    new_access_token=$(echo "$body" | jq -r '.access_token // empty' 2>/dev/null)
    new_refresh_token=$(echo "$body" | jq -r '.refresh_token // empty' 2>/dev/null)
    expires_in=$(echo "$body" | jq -r '.expires_in // empty' 2>/dev/null)

    if [ -z "$new_access_token" ]; then
        log "ERROR: Token refresh response missing access_token"
        return 1
    fi

    # Fall back to existing refresh token if server didn't return a new one
    [ -z "$new_refresh_token" ] && new_refresh_token="$refresh_token"

    # Calculate new expiresAt in milliseconds
    new_expires_at=$(( ($(date +%s) + ${expires_in:-86400}) * 1000 ))

    # Update the credentials file, preserving existing structure
    local updated
    updated=$(jq \
        --arg at "$new_access_token" \
        --arg rt "$new_refresh_token" \
        --argjson ea "$new_expires_at" \
        '
        if .claudeAiOauth then
            .claudeAiOauth.accessToken = $at |
            .claudeAiOauth.refreshToken = $rt |
            .claudeAiOauth.expiresAt = $ea
        else
            .accessToken = $at |
            .refreshToken = $rt |
            .expiresAt = $ea
        end
        ' "$CRED_FILE" 2>/dev/null)

    if [ -z "$updated" ]; then
        log "ERROR: Failed to update credentials file with refreshed token"
        return 1
    fi

    echo "$updated" > "$CRED_FILE"
    chmod 600 "$CRED_FILE"
    log "Token refreshed successfully (expires in ${expires_in:-86400}s)"

    # Push refreshed token to Secrets Manager for other containers
    push_to_secrets_manager

    # Update tracking so we don't re-push from the local change check
    LAST_MTIME=$(get_file_mtime)
    LAST_HASH=$(get_file_hash)

    return 0
}

# Initialize tracking
LAST_MTIME=$(get_file_mtime)
LAST_HASH=$(get_file_hash)
SECONDS_SINCE_PULL=0

# Attempt an immediate refresh on startup in case stored credentials are already expired
refresh_oauth_token || true

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

    # Periodically pull from Secrets Manager and check token expiry
    if [ "$SECONDS_SINCE_PULL" -ge "$PULL_INTERVAL" ]; then
        SECONDS_SINCE_PULL=0
        pull_from_secrets_manager || true
        refresh_oauth_token || true
    fi
done
