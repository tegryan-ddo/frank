#!/bin/bash
# github-app-token.sh - Generate GitHub App installation access token
#
# GitHub Apps use a two-step process:
# 1. Generate a JWT signed with the app's private key
# 2. Exchange the JWT for an installation access token
#
# Usage: github-app-token.sh [installation_id] [jwt]
#
# Arguments (optional, for multi-org use):
#   installation_id - Override $GITHUB_APP_INSTALLATION_ID
#   jwt             - Pre-built JWT (skip generation, reuse across orgs)
#
# Required environment variables:
#   GITHUB_APP_ID           - The GitHub App ID       (only if jwt not provided)
#   GITHUB_APP_PRIVATE_KEY  - The PEM-encoded private key (only if jwt not provided)
#   GITHUB_APP_INSTALLATION_ID - The installation ID  (only if installation_id arg not provided)
#
# Output: Prints the installation access token to stdout
# Exit codes:
#   0 - Success
#   1 - Missing required environment variables
#   2 - JWT generation failed
#   3 - Token exchange failed

set -e

# Accept optional positional args
INSTALL_ID="${1:-$GITHUB_APP_INSTALLATION_ID}"
JWT="${2:-}"

if [ -z "$INSTALL_ID" ]; then
    echo "ERROR: No installation ID (pass as arg or set GITHUB_APP_INSTALLATION_ID)" >&2
    exit 1
fi

# Generate JWT if not provided
if [ -z "$JWT" ]; then
    JWT=$(/usr/local/bin/github-app-jwt.sh) || exit 2
fi

# Exchange JWT for installation access token
RESPONSE=$(curl -s -X POST \
    -H "Authorization: Bearer $JWT" \
    -H "Accept: application/vnd.github+json" \
    -H "X-GitHub-Api-Version: 2022-11-28" \
    "https://api.github.com/app/installations/${INSTALL_ID}/access_tokens")

# Extract token from response
TOKEN=$(echo "$RESPONSE" | jq -r '.token // empty')

if [ -z "$TOKEN" ]; then
    ERROR_MSG=$(echo "$RESPONSE" | jq -r '.message // "Unknown error"')
    echo "ERROR: Failed to get installation token: $ERROR_MSG" >&2
    echo "Response: $RESPONSE" >&2
    exit 3
fi

# Output the token
echo "$TOKEN"
