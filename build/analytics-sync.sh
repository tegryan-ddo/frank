#!/bin/bash
# analytics-sync.sh - Syncs local analytics JSON files to S3
# Runs as a background daemon; best-effort, failures don't affect container operation.

set -o pipefail

ANALYTICS_DIR="$HOME/.frank/analytics"
SYNCED_DIR="$HOME/.frank/analytics/.synced"
LOG_FILE="/tmp/analytics-sync.log"
SYNC_INTERVAL=300  # 5 minutes
REGION="${AWS_REGION:-us-east-1}"
BUCKET="${ANALYTICS_BUCKET:-}"
PROFILE="${CONTAINER_NAME:-unknown}"

log() {
    echo "[$(date -u '+%Y-%m-%d %H:%M:%S')] $*" >> "$LOG_FILE"
}

# Only run in ECS with a configured bucket
if [ -z "$ECS_CONTAINER_METADATA_URI_V4" ]; then
    log "Not running in ECS - analytics sync disabled"
    exit 0
fi

if [ -z "$BUCKET" ]; then
    log "ANALYTICS_BUCKET not set - analytics sync disabled"
    exit 0
fi

log "Analytics sync started (every ${SYNC_INTERVAL}s)"
log "  Source: $ANALYTICS_DIR"
log "  Destination: s3://$BUCKET/prompts/$PROFILE/"

mkdir -p "$SYNCED_DIR"

sync_files() {
    if [ ! -d "$ANALYTICS_DIR" ]; then
        return
    fi

    local count=0

    # Find .json files (excluding the .synced directory)
    while IFS= read -r -d '' file; do
        # Get path relative to analytics dir
        local rel_path="${file#$ANALYTICS_DIR/}"
        local s3_key="prompts/${PROFILE}/${rel_path}"
        local synced_marker="$SYNCED_DIR/$rel_path"

        # Skip if already synced
        if [ -f "$synced_marker" ]; then
            continue
        fi

        # Upload to S3
        if aws s3 cp "$file" "s3://$BUCKET/$s3_key" \
            --region "$REGION" \
            --content-type "application/json" \
            --quiet 2>/dev/null; then
            # Mark as synced
            mkdir -p "$(dirname "$synced_marker")"
            touch "$synced_marker"
            count=$((count + 1))
        else
            log "Failed to upload: $rel_path"
        fi
    done < <(find "$ANALYTICS_DIR" -name '*.json' -not -path '*/.synced/*' -print0 2>/dev/null)

    if [ "$count" -gt 0 ]; then
        log "Synced $count file(s) to s3://$BUCKET/prompts/$PROFILE/"
    fi
}

while true; do
    sync_files
    sleep "$SYNC_INTERVAL"
done
