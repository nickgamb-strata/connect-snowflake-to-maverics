#!/usr/bin/env sh
#
# Seed HashiCorp Vault with secrets for development
# Runs as an init container after Vault dev server starts
#

set -eu

VAULT_ADDR="${VAULT_ADDR:-http://vault:8200}"
VAULT_TOKEN="${VAULT_TOKEN:-blueprints}"
SECRETS_FILE="${SECRETS_FILE:-/vault-init/secrets.yaml}"
LOCAL_SECRETS_FILE="${LOCAL_SECRETS_FILE:-/vault-init/local-secrets.yaml}"
REPO_ROOT="${REPO_ROOT:-/repo}"

export VAULT_ADDR

echo "==> Waiting for Vault to be ready..."
until wget -q -O /dev/null "${VAULT_ADDR}/v1/sys/health" 2>&1; do
    echo "    Vault not ready, waiting..."
    sleep 2
done

echo "==> Seeding secrets from $SECRETS_FILE..."

# Merge local secrets overlay if present
if [ -f "$LOCAL_SECRETS_FILE" ]; then
    echo "==> Merging local secrets overlay..."
    MERGED_SECRETS=$(mktemp)
    yq eval-all 'select(fileIndex == 0) * select(fileIndex == 1)' \
        "$SECRETS_FILE" "$LOCAL_SECRETS_FILE" > "$MERGED_SECRETS"
    SECRETS_FILE="$MERGED_SECRETS"
fi

if [ ! -f "$SECRETS_FILE" ]; then
    echo "    No secrets file found, skipping seed"
    exit 0
fi

# Get list of secret keys
SECRET_KEYS=$(yq '.secrets | keys | .[]' "$SECRETS_FILE" 2>/dev/null || echo "")

if [ -z "$SECRET_KEYS" ]; then
    echo "    No secrets found in $SECRETS_FILE"
    exit 0
fi

# Build JSON payload by processing each secret
PAYLOAD='{'
FIRST=true

for KEY in $SECRET_KEYS; do
    # Check for value or file
    VALUE=$(yq ".secrets[\"$KEY\"].value // \"\"" "$SECRETS_FILE")
    FILE=$(yq ".secrets[\"$KEY\"].file // \"\"" "$SECRETS_FILE")

    if [ -n "$VALUE" ] && [ "$VALUE" != "null" ]; then
        SECRET_VALUE="$VALUE"
        echo "    Loading secret: $KEY"
    elif [ -n "$FILE" ] && [ "$FILE" != "null" ]; then
        FILE_PATH="${REPO_ROOT}/${FILE#./}"
        if [ ! -f "$FILE_PATH" ]; then
            echo "    ERROR: File not found: $FILE_PATH (for secret $KEY)"
            exit 1
        fi
        SECRET_VALUE=$(cat "$FILE_PATH")
        echo "    Loading secret: $KEY (from file)"
    else
        echo "    ERROR: Secret '$KEY' must have either 'value' or 'file'"
        exit 1
    fi

    # Escape the value for JSON
    ESCAPED_VALUE=$(printf '%s' "$SECRET_VALUE" | sed 's/\\/\\\\/g; s/"/\\"/g; s/	/\\t/g' | awk '{printf "%s\\n", $0}' | sed '$ s/\\n$//')

    if [ "$FIRST" = true ]; then
        FIRST=false
    else
        PAYLOAD="${PAYLOAD},"
    fi
    PAYLOAD="${PAYLOAD}\"${KEY}\":\"${ESCAPED_VALUE}\""
done

PAYLOAD="${PAYLOAD}}"

wget -q -O /dev/null --post-data="{\"data\": $PAYLOAD}" \
    --header="X-Vault-Token: $VAULT_TOKEN" \
    --header='Content-Type: application/json' \
    "${VAULT_ADDR}/v1/secret/data/maverics"

SECRET_COUNT=$(echo "$SECRET_KEYS" | wc -w | tr -d ' ')
echo "    Stored $SECRET_COUNT secrets at secret/maverics"

echo ""
echo "==> Vault setup complete!"
echo "    Vault UI: ${VAULT_ADDR}/ui"
echo "    Token: ${VAULT_TOKEN}"
echo ""
