# Snowflake federated trust — live demo script

A presenter-facing walkthrough. Three beats, ≈15–20 minutes total. Each
beat has presenter notes, the exact commands to run, and what success looks
like.

> **Pre-flight check** (do this 5 min before the meeting starts):
> ```bash
> # Load env vars — every command below uses them.
> set -a && . ./.env && set +a
>
> docker compose ps                                       # all services should be Up
> curl -sk https://auth.orchestrator.lab/.well-known/openid-configuration \
>   | grep -q issuer && echo "OIDC OK" || echo "OIDC FAIL"
> ```
>
> **Important:** every terminal you open during the demo needs `set -a && . ./.env && set +a` first. Without it `$MCP_CLIENT_CLI_SNOWFLAKE_SECRET` is empty and the JWT mints fail with a confusing `KeyError: 'access_token'`.

---

## Beat 1 — End-to-end federated trust (≈8 min)

**Setting:** "We extended the Claude-to-Maverics lab with a Snowflake managed-MCP target. Same Maverics OIDC Provider, one new OAuth client, one Service Extension, one `EXTERNAL_OAUTH` integration on Snowflake's side. Maverics is **not** in the data path on the actual query — it issues the JWT and Snowflake validates it directly."

### 1a — Open three windows side-by-side

- **Window A** (terminal): commands you'll run live
- **Window B** (terminal): `docker compose logs -f oidc-provider 2>&1 | grep -E "agent-claims-se|mcp-client-cli-snowflake"` — shows the SE firing per token mint
- **Window C** (browser): Snowflake worksheet at `https://app.snowflake.com/<your-account-path>`. Run once and leave open — real-time, no `ACCOUNT_USAGE` latency:
  ```sql
  USE ROLE ACCOUNTADMIN;
  SELECT
    EVENT_TIMESTAMP,
    CLIENT_IP,
    FIRST_AUTHENTICATION_FACTOR,
    IS_SUCCESS,
    ERROR_MESSAGE
  FROM TABLE(INFORMATION_SCHEMA.LOGIN_HISTORY_BY_USER(
    USER_NAME => 'MAVERICS_AGENT',
    TIME_RANGE_START => DATEADD('hour', -1, CURRENT_TIMESTAMP())
  ))
  ORDER BY EVENT_TIMESTAMP DESC;
  ```

### 1b — Run the demo

> "One command exercises the whole chain — mint a JWT, decode the agent claims, run a TPC-H query under the federated identity."

```bash
make snowflake-demo
```

Expected output sections:

1. **Minting JWT** — confirms the OAuth call to Maverics succeeded.
2. **JWT payload** — `iss: https://auth.orchestrator.lab`, `sub: mcp-client-cli-snowflake`, plus `agent_type`, `agent_provider`, `agent_instance_id`, `delegation_purpose` injected by the Service Extension.
3. **Snowflake SQL API call** — `Columns: ['C_NAME', 'C_NATIONKEY', 'C_ACCTBAL']` and five customer rows from TPC-H.
4. **Snowflake managed MCP tools/list** (optional, if `SNOWFLAKE_MCP_PATH` is set) — shows `run_snowflake_query`.

### 1c — Show the SE firing in real time

> "Window B caught the Service Extension log line — the Go hook fires on every mint and writes a structured audit entry. That's the act-claim chain."

The log line will look like:

```
agent-claims-se "injected agent claims" client_id=mcp-client-cli-snowflake
  agent_type=mcp-client agent_provider=anthropic
  agent_instance_id=snowflake-demo-1777... delegation_purpose=snowflake-read
```

### 1d — Show the Snowflake-side audit

> "Switch to the Snowflake worksheet (Window C). Refresh the login-history query."

Each `make snowflake-demo` adds a row with `FIRST_AUTHENTICATION_FACTOR =
'OAUTH_ACCESS_TOKEN'` and `IS_SUCCESS = 'YES'`. That's Snowflake confirming
it accepted a Maverics-issued JWT.

### 1e — Pair the two audit trails

> "Two systems, two audit trails for the same handshake. Maverics signs it,
> Snowflake validates it, both record it."

For a fuller view that joins logins to the queries that ran under them:

```sql
USE ROLE ACCOUNTADMIN;
SELECT
  l.EVENT_TIMESTAMP            AS LOGIN_TIME,
  l.IS_SUCCESS                 AS LOGIN_OK,
  l.FIRST_AUTHENTICATION_FACTOR AS AUTH_VIA,
  q.QUERY_TEXT,
  q.EXECUTION_STATUS,
  q.ROWS_PRODUCED
FROM TABLE(INFORMATION_SCHEMA.LOGIN_HISTORY_BY_USER(
  USER_NAME => 'MAVERICS_AGENT',
  TIME_RANGE_START => DATEADD('hour', -1, CURRENT_TIMESTAMP())
)) l
LEFT JOIN TABLE(INFORMATION_SCHEMA.QUERY_HISTORY_BY_USER(
  USER_NAME => 'MAVERICS_AGENT',
  END_TIME_RANGE_START => DATEADD('hour', -1, CURRENT_TIMESTAMP())
)) q
  ON q.SESSION_ID IS NOT NULL
ORDER BY l.EVENT_TIMESTAMP DESC, q.START_TIME DESC
LIMIT 20;
```

---

## Beat 2 — Show the wiring (≈5 min)

**Setting:** "Three files make this work. Let me show them."

### 2a — Snowflake side: `apps/snowflake/setup.sql`

```bash
sed -n '/CREATE OR REPLACE SECURITY INTEGRATION/,/COMMENT/p' apps/snowflake/setup.sql
```

> "One `CREATE SECURITY INTEGRATION` — Maverics' issuer URL, JWKS URL, the role I'm willing to let federated callers activate, and the scope-claim handling so Snowflake parses the role name out of the JWT. One-time setup."

### 2b — Maverics OIDC Provider: `orchestrator/oidc-provider/maverics.yaml`

```bash
sed -n '/mcp-client-cli-snowflake/,/^  - name: ai-identity-gateway/p' orchestrator/oidc-provider/maverics.yaml
```

> "New OIDC client. Two interesting blocks:
>
> - `tokenMinting` with an OPA policy — gates which scope is allowed to mint a Snowflake-bound token.
> - `buildAccessTokenClaimsSE` — the Go Service Extension that injects `agent_type`, `agent_provider`, `agent_instance_id`, `delegation_purpose` into the JWT.
>
> Everything else is standard OIDC configuration — same shape as the existing `mcp-client-cli` client from the Claude tutorial."

### 2c — The Service Extension Go file

```bash
cat orchestrator/oidc-provider/service-extensions/agent-claims.go
```

> "Forty lines of Go. Reads the OAuth client_id and the `X-Agent-Instance` header, derives the four agent claims, returns them. Maverics merges them into the access token before signing.
>
> Today the SE is how those claims get in. A declarative YAML block for the same effect is a Maverics product gap — see `apps/snowflake/GAPS.md`."

---

## Beat 3 — Gap analysis walkthrough (≈5 min)

**Setting:** "What we just demoed works on shipping product on both sides. Here's where we'd evolve to get fully to the compounded-JWT vision."

Open `apps/snowflake/GAPS.md` on screen and walk through it column by
column. The headline items:

- **Maverics gaps**: declarative custom claims (vs. the Go SE), per-app default audience override, `tokenBrokering` graduation from experimental, per-mapping JWT TTL, inbound-to-brokered agent-identity propagation.
- **Snowflake gaps**: native Stage-4 compounded-JWT mint, Tier-1 partner trust UX (vs. raw `CREATE SECURITY INTEGRATION` SQL), RFC 9728 Protected Resource Metadata on managed MCP, per-tool scope vocabulary, custom-claim availability to row-access / masking policies, the `SYSTEM_EXECUTE_SQL` `tools/call` parsing bug (workaround documented in the [tutorial README](../../README.md#if-toolscall-fails--front-snowflakes-rest-api-with-mcpbridge-optional) — front Snowflake's REST API with Maverics `mcpBridge`).

### Closing line

> "The federation pattern is producable today, on shipping product on both sides, with one Go file and one `ALTER` on Snowflake's side. The items in GAPS.md are the next quarter's worth of work to get from here to a frictionless compounded-JWT picture without losing the architectural integrity."

---

## Connecting Claude Desktop (optional advanced section)

The demo above uses `client_credentials` for unattended testing. For the
production Claude Desktop flow, register `mcp-client-cli-snowflake` in
Claude Desktop's MCP config via `mcp-remote`:

```jsonc
{
  "mcpServers": {
    "snowflake": {
      "command": "npx",
      "args": [
        "-y",
        "mcp-remote",
        "https://<your-account>.snowflakecomputing.com/api/v2/databases/MAVERICS_DEMO/schemas/MCP/mcp-servers/MAVERICS_AGENT_MCP",
        "3335",
        "--host", "127.0.0.1",
        "--transport", "http-only",
        "--static-oauth-client-info", "{\"client_id\":\"mcp-client-cli-snowflake\"}",
        "--static-oauth-metadata", "{\"issuer\":\"https://auth.orchestrator.lab\",\"authorization_endpoint\":\"https://auth.orchestrator.lab/oauth2/auth\",\"token_endpoint\":\"https://auth.orchestrator.lab/oauth2/token\"}"
      ]
    }
  }
}
```

Claude Desktop opens the browser for OAuth, the user authenticates via
Keycloak, Maverics issues a JWT with `sub` = the user's Keycloak `sub`, and
Claude presents it to Snowflake's managed MCP. The federation mechanics are
the same; only the `sub` mapping changes (your Snowflake user's
`LOGIN_NAME` must match the human's Keycloak `sub`).
