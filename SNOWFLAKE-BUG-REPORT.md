# Snowflake managed-MCP `SYSTEM_EXECUTE_SQL` tools/call fails under EXTERNAL_OAUTH

**File for:** Snowflake engineering (Cortex Agents / managed MCP)
**Filed by:** Nick Gamble (Strata Identity) via Maverics ↔ Snowflake federation demo
**Reproduction date:** 2026-05-14 — 2026-05-17, Standard edition trial account (ID redacted)
**Snowflake area:** managed MCP server (`MCP SERVER … FROM SPECIFICATION`) — released 2025-11-04
**Severity:** S2 — blocks tool-calling agents that authenticate via `EXTERNAL_OAUTH`

Supporting repo: [`connect-snowflake-to-maverics`](https://github.com/nickgamb-strata/connect-snowflake-to-maverics) — public federated-identity tutorial (Strata Maverics is the OAuth Authorization Server, Snowflake is the resource server via `EXTERNAL_OAUTH_INTEGRATION TYPE = CUSTOM`). The strategy companion piece is ["Databricks and Snowflake MCP servers your security team will actually approve"](https://www.strata.io/blog/databricks-and-snowflake-mcp-servers-your-security-team-will-actually-approve/).

---

## Summary

A managed MCP server exposing the built-in `SYSTEM_EXECUTE_SQL` tool returns `Error parsing response` on every `tools/call`, but only when the caller authenticates with a JWT issued by an external IdP through `EXTERNAL_OAUTH_INTEGRATION`. The **same JWT, against the same account, running the same SQL** succeeds against `/api/v2/statements`. `tools/list` against the MCP server also succeeds. Only `tools/call` of `SYSTEM_EXECUTE_SQL` fails.

This blocks the whole point of a managed MCP server for any deployment that uses federated identity instead of Snowflake-native PATs or key-pair auth — i.e., the dominant pattern for workforce-AI MCP clients (Claude Desktop, Cursor, etc.) connecting to multiple data sources through an enterprise OAuth gateway.

## Environment

| Component | Value |
| --- | --- |
| Snowflake account | trial, Standard edition (US-West) — account ID redacted, available on request |
| Account URL | `https://<your-account>.snowflakecomputing.com` |
| Managed MCP path | `/api/v2/databases/MAVERICS_DEMO/schemas/MCP/mcp-servers/MAVERICS_AGENT_MCP` |
| MCP tool | `SYSTEM_EXECUTE_SQL` (Snowflake built-in) |
| External IdP / AS | Strata Maverics Identity Orchestrator v2026.03.4 |
| Integration | `EXTERNAL_OAUTH_INTEGRATION` with `EXTERNAL_OAUTH_TYPE = CUSTOM` |
| Issuer | `https://auth.orchestrator.lab` |
| JWKS | `https://<public-jwks-host>/oauth2/jwks` (Cloudflare-tunneled Maverics) |
| User mapping claim | `('email', 'sub')` → `LOGIN_NAME` |
| Scope mapping | `SCOPE_MAPPING_ATTRIBUTE = 'scope'`, `SCOPE_DELIMITER = ' '`, scopes carry `session:role:MAVERICS_DEMO_ROLE` |

## Steps to reproduce

1. Create the integration, role, user, warehouse, and managed MCP server per `apps/snowflake/setup.sql` in the supporting repo (1-shot DDL — full file attached). The MCP server spec is:

    ```sql
    CREATE OR REPLACE MCP SERVER MAVERICS_DEMO.MCP.MAVERICS_AGENT_MCP
        FROM SPECIFICATION $$
            tools:
              - type: SYSTEM_EXECUTE_SQL
                identifier: "MAVERICS_AGENT_EXEC_SQL"
        $$;
    GRANT USAGE,  MODIFY ON MCP SERVER MAVERICS_DEMO.MCP.MAVERICS_AGENT_MCP TO ROLE MAVERICS_DEMO_ROLE;
    ```

2. Mint a JWT from the external IdP. Required claims:
   - `iss` = `https://auth.orchestrator.lab` (matches integration's `EXTERNAL_OAUTH_ISSUER`)
   - `aud` = `https://<your-account>.snowflakecomputing.com/` (in `AUDIENCE_LIST`)
   - `sub` = `mcp-client-cli-snowflake` (matches `LOGIN_NAME` of the `MAVERICS_AGENT` user) — or `email` = a workforce email matched to another `LOGIN_NAME`. The bug reproduces regardless of which mapping resolves.
   - `scope` includes `session:role:MAVERICS_DEMO_ROLE`
   - signed with a key the integration's JWKS URL publishes

3. **`/api/v2/statements` — works:**

    ```bash
    curl -X POST "https://<your-account>.snowflakecomputing.com/api/v2/statements" \
        -H "Authorization: Bearer $JWT" \
        -H "X-Snowflake-Authorization-Token-Type: OAUTH" \
        -H "Content-Type: application/json" \
        -d '{
            "statement": "SELECT C_NAME, C_NATIONKEY, C_ACCTBAL FROM SNOWFLAKE_SAMPLE_DATA.TPCH_SF1.CUSTOMER ORDER BY C_ACCTBAL DESC LIMIT 3",
            "warehouse": "MAVERICS_WH",
            "role": "MAVERICS_DEMO_ROLE"
        }'
    ```

    Returns rows. ✅

4. **Managed-MCP `tools/list` — works:**

    ```bash
    curl -X POST "https://<your-account>.snowflakecomputing.com/api/v2/databases/MAVERICS_DEMO/schemas/MCP/mcp-servers/MAVERICS_AGENT_MCP" \
        -H "Authorization: Bearer $JWT" \
        -H "X-Snowflake-Authorization-Token-Type: OAUTH" \
        -H "Content-Type: application/json" \
        -H "Accept: application/json, text/event-stream" \
        -d '{"jsonrpc":"2.0","method":"tools/list","id":1}'
    ```

    Returns the `MAVERICS_AGENT_EXEC_SQL` tool descriptor with input schema. ✅

5. **Managed-MCP `tools/call` of `SYSTEM_EXECUTE_SQL` — fails:**

    ```bash
    curl -X POST "https://<your-account>.snowflakecomputing.com/api/v2/databases/MAVERICS_DEMO/schemas/MCP/mcp-servers/MAVERICS_AGENT_MCP" \
        -H "Authorization: Bearer $JWT" \
        -H "X-Snowflake-Authorization-Token-Type: OAUTH" \
        -H "Content-Type: application/json" \
        -H "Accept: application/json, text/event-stream" \
        -d '{
            "jsonrpc":"2.0",
            "method":"tools/call",
            "id":2,
            "params":{
                "name":"MAVERICS_AGENT_EXEC_SQL",
                "arguments":{
                    "statement":"SELECT C_NAME FROM SNOWFLAKE_SAMPLE_DATA.TPCH_SF1.CUSTOMER LIMIT 1"
                }
            }
        }'
    ```

    Returns:

    ```json
    {
      "jsonrpc": "2.0",
      "id": 2,
      "error": {
        "code": -32603,
        "message": "Error parsing response"
      }
    }
    ```

    HTTP status is `200 OK`. ❌

## Observed vs expected

| | Expected | Observed |
| --- | --- | --- |
| HTTP status | `200 OK` with tool result content | `200 OK` with JSON-RPC `-32603` error |
| Error body | n/a — should be a `content` array of text blocks | `{"error":{"code":-32603,"message":"Error parsing response"}}` |
| Query history | An entry for the SELECT under `MAVERICS_AGENT` / role `MAVERICS_DEMO_ROLE` | **No entry** — the SQL never reaches the engine |

## What we tried

We were thorough before filing because if it's a config issue we'd rather fix it than report it. Each of the following did **not** change the outcome:

- Changing the SQL — `SELECT 1`, fully-qualified table names, `SHOW TABLES`, `EXECUTE STATEMENT 'SELECT 1'`. Same `Error parsing response` for every body.
- Granting `USAGE,MODIFY ON MCP SERVER` to `MAVERICS_DEMO_ROLE` — already in place; verified via `SHOW GRANTS`.
- Toggling `EXTERNAL_OAUTH_ANY_ROLE_MODE` between `DISABLE`, `ENABLE_FOR_PRIVILEGE`, `ENABLE`.
- Using `email` vs `sub` for user mapping — both succeed on `/api/v2/statements`, both fail on `tools/call`.
- Adding the `X-Snowflake-Authorization-Token-Type: OAUTH` header and removing it; setting `Accept: text/event-stream`, `application/json`, both; sending the MCP `initialize` handshake first and reusing the session ID; not sending an `Mcp-Session-Id`. All routes through to the same `-32603`.
- Decoding the JWT and verifying every claim manually. Timestamps are encoded as scientific-notation numbers (e.g., `1.779043744e+09`) — confirmed by Maverics to be RFC 7519 NumericDate compliant and accepted by Snowflake's `/api/v2/statements` path. Ruled out as a cause.
- Same flow with a `PAT` (Snowflake personal access token) on the same MCP server — works. The bug is specific to the `EXTERNAL_OAUTH` authentication path, not the user/role/grants.

## Working hypothesis

`SYSTEM_EXECUTE_SQL` under the managed-MCP code path doesn't carry the `EXTERNAL_OAUTH`-derived session/role context through to the SQL execution layer in the same way `/api/v2/statements` does. The MCP handler appears to start a SQL session in a mode that the OAuth-validated identity hasn't been resolved into yet, so the execution side fails before producing a parseable result — which is then surfaced to MCP as the generic "Error parsing response" message.

Two pieces of evidence that point at the session-init path rather than the request body:

1. `tools/list` works fine with the same JWT. That confirms auth, JWKS fetch, audience/scope mapping, role activation, and MCP transport are all correct.
2. The query never lands in `SNOWFLAKE.ACCOUNT_USAGE.QUERY_HISTORY` for `MAVERICS_AGENT`, even though `/api/v2/statements` calls from the same JWT do show up there. The SQL never starts.

## Impact

- Any managed-MCP deployment using `EXTERNAL_OAUTH_INTEGRATION` for federated workforce identity (Claude Desktop, Cursor, Bedrock AgentCore, ChatGPT Enterprise, …) cannot execute SQL through the managed MCP server. They get tool discovery but cannot call the only tool that matters.
- Customers fall back to either (a) hosting their own MCP server that wraps `/api/v2/statements` (defeats the point of Snowflake's managed MCP — that's what our workaround does today) or (b) switching to PATs (loses federation, every user needs a Snowflake key).
- The bug doesn't reproduce under PAT auth, so it's specific to the `EXTERNAL_OAUTH` validation/session-attach path inside the MCP handler.

## Current workaround (for visibility)

Until this is fixed, the supporting repo ships an opt-in workaround at
[`optional/snowflake-bridge-workaround/`](optional/snowflake-bridge-workaround/) that fronts Snowflake's `/api/v2/statements` with a tiny REST shim and exposes it as an MCP `runQuery` tool via Maverics' [`mcpBridge`](https://docs.strata.io/reference/orchestrator/applications/mcp-bridge). Same federated JWT, same `EXTERNAL_OAUTH` validation, same role activation, same `LOGIN_HISTORY`/`QUERY_HISTORY` entries — only the wire path between MCP client and Snowflake changes. We'd like to delete that code; this report is the request to make that possible.

## Suggested next steps

1. Add a server-side log line that captures the actual exception being squashed into `Error parsing response`. The generic message defeats client-side debugging.
2. Verify the MCP `tools/call` handler for `SYSTEM_EXECUTE_SQL` resolves the `EXTERNAL_OAUTH` JWT to a session in the same way `/api/v2/statements` does — specifically that role activation from the `scope` claim happens before the SQL engine is invoked.
3. Add an integration test that runs `tools/call SYSTEM_EXECUTE_SQL` under `EXTERNAL_OAUTH_INTEGRATION TYPE = CUSTOM` — the existing tests likely cover PAT/keypair auth only.

## Repro artifacts (attach to JIRA)

- `apps/snowflake/setup.sql` — full DDL (warehouse, role, user, integration, MCP server)
- `scripts/snowflake-demo.sh` — JWT mint + `/api/v2/statements` (working path)
- Sample JWT with all claims redacted (sub, account ID), available on request

## Contact

Nick Gamble, Strata Identity — `nick.gamble@strata.io` / Slack `@nickgamble`
Tagging Kelvin Chen as Snowflake-side counterpart on this thread.
