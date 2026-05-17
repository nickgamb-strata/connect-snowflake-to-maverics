# Snowflake managed-MCP `tools/call` workaround — `mcpBridge` pattern

> **Skip this if you don't need it.** At the time of writing, Snowflake's
> managed-MCP server has a bug where `tools/call` against `SYSTEM_EXECUTE_SQL`
> returns `MCP Server tool error: Error parsing response` under
> `EXTERNAL_OAUTH_INTEGRATION`, even though `tools/list` works and the same
> JWT runs the same SQL fine against `/api/v2/statements`. The reproduction
> details are in [`../../SNOWFLAKE-BUG-REPORT.md`](../../SNOWFLAKE-BUG-REPORT.md).
> When Snowflake fixes the bug, delete the files here, remove the
> `snowflake-shim` service from `docker-compose.yml`, and remove the
> `snowflake-bridge` app from `orchestrator/ai-identity-gateway/maverics.yaml`.

## What it does

Bypass Snowflake's managed-MCP endpoint and front the bare `/api/v2/statements`
REST API with a tiny shim. Maverics' [`mcpBridge`](https://docs.strata.io/reference/orchestrator/applications/mcp-bridge)
reads an OpenAPI spec describing the shim and generates an MCP `runQuery`
tool. RFC 8693 delegation token exchange swaps the inbound user JWT for a
Snowflake-audience JWT with the user's `email` claim forwarded, so
Snowflake's `EXTERNAL_OAUTH` integration matches the workforce user and
`MAVERICS_DEMO_ROLE` activates from the `scope` claim — identical
federation semantics to the managed-MCP path, just with Maverics in the
data path for the tool call itself.

```
                                                ┌──────────────────────────────────┐
  MCP client (Cursor / Claude Desktop)          │ Maverics AI Identity Gateway     │
       │                                        │                                  │
       │ POST /mcp tools/call (Bearer JWT)      │  mcpBridge: snowflake-bridge     │
       │ ─────────────────────────────────────► │   ├─ delegation token-exchange   │
       │                                        │   │  → aud = Snowflake account   │
       │                                        │   │  → scope = session:role:…    │
       │                                        │   │  → email forwarded by SE     │
       │                                        │   ▼                              │
       │                                        │  POST /sql ──────────────┐       │
       │                                        └──────────────────────────│───────┘
       │                                                                   ▼
       │                                                ┌──────────────────────────┐
       │                                                │ snowflake-shim (Flask)   │
       │                                                │  + X-Snowflake-Auth-     │
       │                                                │    Token-Type: OAUTH     │
       │                                                │  → POST /api/v2/         │
       │                                                │    statements            │
       │                                                └──────────┬───────────────┘
       │                                                           ▼
       │                                                ┌──────────────────────────┐
       │                                                │ Snowflake                │
       │                                                │ EXTERNAL_OAUTH validates │
       │                                                │ → email → JOHN_MCCLANE   │
       │                                                │ → role activates         │
       │                                                │ → query runs             │
       │                                                └──────────────────────────┘
```

## What ships in this directory

| Path | Purpose |
| --- | --- |
| `snowflake-shim/app.py` | ~70 lines of Flask. Reads `POST /sql {sql, warehouse, role}`, forwards to Snowflake's `/api/v2/statements` with the inbound `Authorization` header plus `X-Snowflake-Authorization-Token-Type: OAUTH`. |
| `snowflake-shim/openapi.yaml` | OpenAPI 3.0 spec describing the shim. `mcpBridge` reads this file and auto-generates a `runQuery` MCP tool with input schema derived from the request body. |
| `snowflake-shim/Dockerfile` | Slim Python 3.12 image. Built from `docker-compose.yml`. |
| `snowflake-shim/requirements.txt` | Just `flask==3.1.1`. |
| `policies/snowflake-bridge-inbound-authz.rego` | Default-allow OPA policy — every authenticated caller can invoke the `runQuery` tool. Replace with real per-tool / per-user authorization when you wire this into anything that isn't the demo. |

## How it's wired into the lab

This workaround is enabled in the default `docker-compose.yml` because the
upstream bug currently blocks the alternative end-to-end MCP-tool-call path.
Specifically:

- `docker-compose.yml` declares the `snowflake-shim` service and mounts
  `openapi.yaml` + the OPA policy into the `ai-identity-gateway` container.
- `orchestrator/ai-identity-gateway/maverics.yaml` declares a `snowflake-bridge`
  mcpBridge app that reads the OpenAPI spec, exposes `runQuery` as an MCP tool,
  and does RFC 8693 delegation token exchange to mint Snowflake-audience JWTs.
- `orchestrator/oidc-provider/maverics.yaml` adds the Snowflake account URL
  to the `ai-identity-gateway` confidential client's `allowedAudiences` and
  carries `session:role:MAVERICS_DEMO_ROLE` + `snowflake:sql_execution`
  in `customScopes`. The `buildAccessTokenClaimsSE` hook on that client
  (`service-extensions/aig-claims.go`) decodes the inbound subject_token and
  forwards `email` onto the exchanged JWT. A parallel hook on `mcp-client-cli`
  (`service-extensions/mcp-client-claims.go`) puts `email` into the inbound
  JWT in the first place — Maverics' declarative `claimsMapping` populates
  ID tokens and userinfo by OIDC convention but not the access-token JWT.

If you want the workaround OFF (e.g. when Snowflake ships a fix), three deletes:

1. Remove the `snowflake-shim` service and the two workaround volume mounts
   in `docker-compose.yml`.
2. Remove the `snowflake-bridge` app block in
   `orchestrator/ai-identity-gateway/maverics.yaml`.
3. Optionally remove the `<snowflake.account_url>` audience entry,
   `session:role:MAVERICS_DEMO_ROLE` / `snowflake:sql_execution` scopes, and
   the `buildAccessTokenClaimsSE` hook on the `ai-identity-gateway` client in
   `orchestrator/oidc-provider/maverics.yaml` (harmless to leave; they have
   no effect when the bridge stanza is gone).

The two `buildAccessTokenClaimsSE` hooks are safe to leave wired even without
the workaround — they only mutate JWTs that resource servers downstream are
going to look at, and the additional `email` claim is well-formed regardless.

## How to copy this pattern to your own deployment

The pattern is generic. To front any Snowflake-audience REST API with
Maverics' `mcpBridge`:

1. Take `snowflake-shim/` as-is or adapt the SQL handler to your API surface
   (per-warehouse routing, async statement polling, etc).
2. Author an OpenAPI 3.0 spec describing the shim — declare a `bearerAuth`
   security scheme so `mcpBridge` knows to forward the exchanged JWT.
3. Drop an `mcpBridge` app into your AI Identity Gateway config with
   `tokenExchange.type: delegation`, `audience: <snowflake-account-url>`,
   and per-tool scopes including `session:role:<role>`.
4. Make sure your inbound OIDC client and your AIG confidential client carry
   the user's `email` claim through to the exchanged JWT — Maverics doesn't
   do that automatically. Two small Go Service Extensions, examples in
   `service-extensions/mcp-client-claims.go` and `service-extensions/aig-claims.go`.

The published [`mcpBridge` reference](https://docs.strata.io/reference/orchestrator/applications/mcp-bridge)
documents every config knob.

## File the bug

See [`SNOWFLAKE-BUG-REPORT.md`](../../SNOWFLAKE-BUG-REPORT.md) for a
JIRA-ready writeup with exact reproduction steps, the full list of variations
we tested, and the working hypothesis. Hand it to Snowflake support so this
workaround can come back out of the default config.
