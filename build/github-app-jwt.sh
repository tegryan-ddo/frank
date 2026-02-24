#!/bin/bash
# github-app-jwt.sh - Generate a GitHub App JWT
#
# Generates a JWT signed with the app's private key. The JWT can then be
# used to exchange for one or more installation access tokens (via
# github-app-token.sh).  Extracting JWT generation lets us build the JWT
# once and reuse it across N installation-token exchanges for multi-org
# setups.
#
# Required environment variables:
#   GITHUB_APP_ID          - The GitHub App ID
#   GITHUB_APP_PRIVATE_KEY - The PEM-encoded private key (can include newlines)
#
# Output: Prints the JWT to stdout
# Exit codes:
#   0 - Success
#   1 - Missing required environment variables
#   2 - Key validation / JWT generation failed

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

# Write private key to temp file (handle escaped newlines)
KEY_FILE=$(mktemp)
trap "rm -f $KEY_FILE" EXIT

# Handle both actual newlines and escaped \n in the key
echo "$GITHUB_APP_PRIVATE_KEY" | sed 's/\\n/\n/g' > "$KEY_FILE"

# Verify the key is valid
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

HEADER=$(echo -n '{"alg":"RS256","typ":"JWT"}' | b64url)
PAYLOAD=$(echo -n "{\"iat\":${IAT},\"exp\":${EXP},\"iss\":\"${GITHUB_APP_ID}\"}" | b64url)
SIGNATURE=$(echo -n "${HEADER}.${PAYLOAD}" | openssl dgst -sha256 -sign "$KEY_FILE" | b64url)

echo "${HEADER}.${PAYLOAD}.${SIGNATURE}"
