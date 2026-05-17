#!/usr/bin/env bash
# Exercise the federated-trust chain end to end without an interactive client:
#   1. Mint a JWT from Maverics' OIDC Provider via client_credentials.
#   2. Decode the JWT and show the agent_* claims the SE injected.
#   3. Authenticate to Snowflake's SQL API with that JWT, run a TPC-H query.
#   4. (Optional) Hit Snowflake's managed MCP endpoint with the same JWT to
#      prove tools/list resolves under the EXTERNAL_OAUTH integration.
#
# Production note: in a real workforce deployment the JWT comes from Claude
# Desktop driving authorization_code + PKCE, with `sub` = the human's
# Keycloak `sub`. The client_credentials path here is for unattended testing
# and CI — same federation mechanics, no browser required.
set -euo pipefail

: "${SNOWFLAKE_ACCOUNT_URL:?SNOWFLAKE_ACCOUNT_URL must be set in .env}"
: "${MCP_CLIENT_CLI_SNOWFLAKE_SECRET:?MCP_CLIENT_CLI_SNOWFLAKE_SECRET must be set in .env (matches secrets.yaml)}"
: "${SNOWFLAKE_ROLE:=MAVERICS_DEMO_ROLE}"
: "${SNOWFLAKE_WAREHOUSE:=MAVERICS_WH}"
: "${DEMO_SQL:=SELECT C_NAME, C_NATIONKEY, C_ACCTBAL FROM SNOWFLAKE_SAMPLE_DATA.TPCH_SF1.CUSTOMER ORDER BY C_ACCTBAL DESC LIMIT 5}"

ACCOUNT_URL="${SNOWFLAKE_ACCOUNT_URL%/}"

# ── 1. Mint a Maverics JWT ──────────────────────────────────────────────
echo "==> 1. Minting JWT from Maverics OIDC Provider (mcp-client-cli-snowflake)"
RESP=$(curl -sk -X POST "https://auth.orchestrator.lab/oauth2/token" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -H "X-Agent-Instance: snowflake-demo-$(date +%s)" \
  --data-urlencode "grant_type=client_credentials" \
  --data-urlencode "client_id=mcp-client-cli-snowflake" \
  --data-urlencode "client_secret=${MCP_CLIENT_CLI_SNOWFLAKE_SECRET}" \
  --data-urlencode "scope=session:role:MAVERICS_DEMO_ROLE snowflake:cortex_analyst snowflake:cortex_search snowflake:sql_execution")

JWT=$(echo "${RESP}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
if 'access_token' not in d:
    sys.stderr.write('Mint failed: ' + json.dumps(d) + '\n'); sys.exit(1)
print(d['access_token'])
")

# ── 2. Decode the JWT to show the agent claims ─────────────────────────
echo ""
echo "==> 2. JWT payload (agent claims injected by the Service Extension)"
echo "${JWT}" | python3 -c "
import json, sys, base64
parts = sys.stdin.read().strip().split('.')
def d(p): p += '=' * (-len(p)%4); return json.loads(base64.urlsafe_b64decode(p).decode())
p = d(parts[1])
for k in ['iss','sub','aud','scope','exp','iat','agent_type','agent_provider','agent_instance_id','delegation_purpose']:
    if k in p:
        print(f'  {k}: {p[k]}')
"

# ── 3. Snowflake SQL API call with the Maverics JWT ─────────────────────
echo ""
echo "==> 3. Snowflake SQL API call with the Maverics JWT"
echo "    sql: ${DEMO_SQL}"
REQ_BODY=$(DEMO_SQL="${DEMO_SQL}" \
  SNOWFLAKE_WAREHOUSE="${SNOWFLAKE_WAREHOUSE}" \
  SNOWFLAKE_ROLE="${SNOWFLAKE_ROLE}" \
  python3 -c 'import json, os; print(json.dumps({"statement": os.environ["DEMO_SQL"], "warehouse": os.environ["SNOWFLAKE_WAREHOUSE"], "role": os.environ["SNOWFLAKE_ROLE"], "timeout": 30}))')
curl -sS -X POST "${ACCOUNT_URL}/api/v2/statements" \
  -H "Authorization: Bearer ${JWT}" \
  -H "X-Snowflake-Authorization-Token-Type: OAUTH" \
  -H "Content-Type: application/json" \
  -d "${REQ_BODY}" \
  --max-time 30 \
  | python3 -c "
import json, sys
d = json.load(sys.stdin)
if 'data' in d:
    cols = [c['name'] for c in d.get('resultSetMetaData', {}).get('rowType', [])]
    print('  Columns:', cols)
    for r in d['data']:
        print('  Row:', r)
else:
    print('  ERROR:', d.get('message','?').split(chr(10))[0][:200])
    print('  Code :', d.get('code'))
"

# ── 4. Snowflake managed MCP tools/list (optional) ──────────────────────
#
# Note: at the time of writing, Snowflake's managed MCP returns "Error
# parsing response" on every tools/call against SYSTEM_EXECUTE_SQL under
# EXTERNAL_OAUTH, while tools/list works fine through the same JWT. We
# probe tools/list here as part of the demo for that reason — it proves
# the federation reaches the MCP endpoint. If you need tools/call to
# work today, see the "If tools/call fails" section in the README for
# the mcpBridge-based workaround.
if [ -n "${SNOWFLAKE_MCP_PATH:-}" ]; then
  echo ""
  echo "==> 4. Snowflake managed MCP tools/list"
  curl -sk -X POST "${ACCOUNT_URL}${SNOWFLAKE_MCP_PATH}" \
    -H "Authorization: Bearer ${JWT}" \
    -H "X-Snowflake-Authorization-Token-Type: OAUTH" \
    -H "Content-Type: application/json" \
    -H "Accept: application/json, text/event-stream" \
    -d '{"jsonrpc":"2.0","method":"tools/list","id":1}' \
    --max-time 20 | python3 -c "
import json, sys, re
text = sys.stdin.read()
m = re.search(r'\{.*\}', text, re.S)
if not m:
    print('  No JSON body returned'); sys.exit(0)
d = json.loads(m.group(0))
tools = d.get('result', {}).get('tools', [])
if not tools:
    print('  ', d.get('error') or 'no tools')
for t in tools:
    print(f'  - {t[\"name\"]}: {t.get(\"description\",\"\")[:60]}')
"
fi
