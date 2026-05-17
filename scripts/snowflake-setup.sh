#!/usr/bin/env bash
# Apply apps/snowflake/setup.sql against a Snowflake free-tier account, with
# env-driven values substituted in. Idempotent — safe to re-run.
#
# Required env (loaded from .env by the Makefile target):
#   SNOWFLAKE_ACCOUNT_URL   https://<account>.snowflakecomputing.com
#   SNOWFLAKE_ADMIN_USER    Snowflake user with ACCOUNTADMIN
#   SNOWSQL_PWD             that user's password (set by Makefile, exported here)
#   BEDROCK_AUTH_HOSTNAME   Publicly-reachable hostname that exposes Maverics'
#                           /oauth2/jwks endpoint (e.g. auth.example.com). The
#                           script bakes "https://${BEDROCK_AUTH_HOSTNAME}/oauth2/jwks"
#                           into EXTERNAL_OAUTH_JWS_KEYS_URL on the Snowflake side.
#                           LOCAL-LAB ONLY uses a Cloudflare Tunnel for this;
#                           production should point at a hardened public origin
#                           (CDN, managed gateway, etc.).
#
# Prereqs:
#   - snowsql installed locally (https://docs.snowflake.com/en/user-guide/snowsql)
#   - The host at $BEDROCK_AUTH_HOSTNAME is reachable from Snowflake's network
#     and serves Maverics' JWKS at /oauth2/jwks (verify with `curl` from any
#     box outside your LAN).
set -euo pipefail

: "${SNOWFLAKE_ACCOUNT_URL:?SNOWFLAKE_ACCOUNT_URL must be set in .env}"
: "${SNOWFLAKE_ADMIN_USER:?SNOWFLAKE_ADMIN_USER must be set in .env}"
: "${SNOWSQL_PWD:?SNOWSQL_PWD must be set in the environment (Makefile loads it from .env)}"
: "${BEDROCK_AUTH_HOSTNAME:?BEDROCK_AUTH_HOSTNAME must be set in .env}"

# Strip a trailing slash from the account URL so it concatenates predictably.
ACCOUNT_URL="${SNOWFLAKE_ACCOUNT_URL%/}"
ACCOUNT_HOST="${ACCOUNT_URL#https://}"
ACCOUNT_HOST="${ACCOUNT_HOST%%/*}"
ACCOUNT_NAME="${ACCOUNT_HOST%%.*}"

# Maverics signs JWTs with `iss` set to the internal lab hostname; Snowflake
# fetches JWKS over the public Cloudflare hostname.
MAVERICS_INTERNAL_ISSUER="https://auth.orchestrator.lab"
MAVERICS_JWKS_URL="https://${BEDROCK_AUTH_HOSTNAME}/oauth2/jwks"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SQL_TEMPLATE="${REPO_ROOT}/apps/snowflake/setup.sql"
SQL_RENDERED="$(mktemp -t snowflake-setup.XXXXXX.sql)"

trap 'rm -f "${SQL_RENDERED}"' EXIT

echo "==> Rendering SQL with:"
echo "    Account URL:             ${ACCOUNT_URL}"
echo "    Maverics internal issuer: ${MAVERICS_INTERNAL_ISSUER}"
echo "    Maverics JWKS (public):   ${MAVERICS_JWKS_URL}"
echo ""

# Use a delimiter unlikely to appear in the URLs.
sed \
  -e "s|__SNOWFLAKE_ACCOUNT_URL__|${ACCOUNT_URL}|g" \
  -e "s|__MAVERICS_INTERNAL_ISSUER__|${MAVERICS_INTERNAL_ISSUER}|g" \
  -e "s|__MAVERICS_JWKS_URL__|${MAVERICS_JWKS_URL}|g" \
  "${SQL_TEMPLATE}" > "${SQL_RENDERED}"

echo "==> Applying setup.sql to Snowflake account ${ACCOUNT_NAME}…"
snowsql \
  --accountname "${ACCOUNT_NAME}" \
  --username "${SNOWFLAKE_ADMIN_USER}" \
  --filename "${SQL_RENDERED}" \
  --variable warehouse=MAVERICS_WH \
  --variable role=ACCOUNTADMIN \
  -o quiet=false \
  -o friendly=false \
  -o output_format=plain

echo ""
echo "==> Setup complete. Now run:"
echo "    make snowflake-up        # registers the AgentCore Snowflake target"
echo "    make snowflake-demo      # exercises the full chain"
