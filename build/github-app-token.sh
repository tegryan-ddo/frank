#!/bin/bash
# github-app-token.sh - Generate GitHub App installation access token
#
# GitHub Apps use a two-step process:
# 1. Generate a JWT signed with the app's private key
# 2. Exchange the JWT for an installation access token
#
# Required environment variables:
#   GITHUB_APP_ID           - The GitHub App ID
#   GITHUB_APP_PRIVATE_KEY  - The PEM-encoded private key (can include newlines)
#   GITHUB_APP_INSTALLATION_ID - The installation ID for the org/repos
#
# Output: Prints the installation access token to stdout
# Exit codes:
#   0 - Success
#   1 - Missing required environment variables
#   2 - JWT generation failed
#   3 - Token exchange failed

set -e

# Check required environment variables
if [ -z "$GITHUB_APP_ID" ]; then
    echo "ERROR: GITHUB_APP_ID not set" >&2
    exit 1
fi

if [ -z "$GITHUB_APP_PRIVATE_KEY" ]; then
    echo "ERROR: GITHUB_APP_PRIVATE_KEY not set" >&2
    exit 1
fi

if [ -z "$GITHUB_APP_INSTALLATION_ID" ]; then
    echo "ERROR: GITHUB_APP_INSTALLATION_ID not set" >&2
    exit 1
fi

# Write private key to temp file (handle escaped newlines)
KEY_FILE=$(mktemp)
trap "rm -f $KEY_FILE" EXIT

# Handle both actual newlines and escaped \n in the key
echo "$GITHUB_APP_PRIVATE_KEY" | sed 's/\\n/\n/g' > "$KEY_FILE"

# Verify the key is valid (suppress all output, only care about exit code)
if ! openssl rsa -in "$KEY_FILE" -check -noout >/dev/null 2>&1; then
    echo "ERROR: Invalid private key" >&2
    exit 2
fi

# Generate JWT
# Header: {"alg":"RS256","typ":"JWT"}
# Payload: {"iat": <now>, "exp": <now+10min>, "iss": <app_id>}

NOW=$(date +%s)
IAT=$((NOW - 60))  # 1 minute in the past to handle clock drift
EXP=$((NOW + 600)) # 10 minutes from now (max allowed)

# Base64url encode (no padding, + -> -, / -> _)
b64url() {
    openssl base64 -A | tr '+/' '-_' | tr -d '='
}

# Create header
HEADER=$(echo -n '{"alg":"RS256","typ":"JWT"}' | b64url)

# Create payload
PAYLOAD=$(echo -n "{\"iat\":${IAT},\"exp\":${EXP},\"iss\":\"${GITHUB_APP_ID}\"}" | b64url)

# Create signature
SIGNATURE=$(echo -n "${HEADER}.${PAYLOAD}" | openssl dgst -sha256 -sign "$KEY_FILE" | b64url)

# Combine into JWT
JWT="${HEADER}.${PAYLOAD}.${SIGNATURE}"

# Exchange JWT for installation access token
RESPONSE=$(curl -s -X POST \
    -H "Authorization: Bearer $JWT" \
    -H "Accept: application/vnd.github+json" \
    -H "X-GitHub-Api-Version: 2022-11-28" \
    "https://api.github.com/app/installations/${GITHUB_APP_INSTALLATION_ID}/access_tokens")

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
