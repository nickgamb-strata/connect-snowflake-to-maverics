# Connect Snowflake to Maverics

A self-contained example connecting Claude Desktop (and other MCP clients) to **Snowflake's managed MCP server** through Maverics-issued JWTs — federated trust without a service account. Snowflake validates the JWT against Maverics' JWKS via `EXTERNAL_OAUTH_INTEGRATION`. Maverics is **not** in the data path on the actual query.

Companion to the blog post: *Connect Snowflake to Maverics: Federated Identity for Workforce AI Clients* (`connect-snowflake-to-maverics` on [maverics.ai/blog](https://www.maverics.ai/blog)).

This tutorial starts from the same lab baseline as [connect-claude-to-maverics](https://github.com/nickgamb-strata/connect-claude-to-maverics) and layers the Snowflake federation on top. The parallel [connect-aws-bedrock-to-maverics](https://github.com/nickgamb-strata/connect-aws-bedrock-to-maverics) tutorial does the same with AWS Bedrock AgentCore. You can follow either, or both — they don't depend on each other.

## What you get

- The Maverics AI Identity Gateway protecting two MCP backends (Enterprise Ledger, Employee Directory) — the same baseline as the Claude tutorial.
- A new Maverics OIDC client (`mcp-client-cli-snowflake`) wired with a Go Service Extension that injects agent identity claims (`agent_type`, `agent_provider`, `agent_instance_id`, `delegation_purpose`) on every token mint.
- A Snowflake `EXTERNAL_OAUTH_INTEGRATION` that trusts Maverics' issuer + JWKS and activates a least-privilege role from the JWT scope claim.
- A managed MCP server on Snowflake (`MAVERICS_DEMO.MCP.MAVERICS_AGENT_MCP`) exposing a `SYSTEM_EXECUTE_SQL` tool against the TPC-H sample data.
- An end-to-end demo script that mints a JWT, decodes the agent claims, and runs a TPC-H query under the federated identity — no AWS, no AgentCore, no service account.

## Prerequisites

- Docker Desktop (or Docker Engine + Compose v2)
- [mkcert](https://github.com/FiloSottile/mkcert) for local TLS
- [Node.js](https://nodejs.org/) for `mcp-remote` (Claude Desktop connection)
- [snowsql](https://docs.snowflake.com/en/user-guide/snowsql) (for `make snowflake-setup`) — or use the bundled Python helper if `snowsql` is not installed
- Maverics Orchestrator image from [Strata](https://www.strata.io/), loaded via `docker load`
- A Snowflake account. The 30-day trial works — `EXTERNAL_OAUTH_INTEGRATION TYPE = CUSTOM` is available on all editions.

## Quick start

```bash
# 1. Generate TLS certs, OIDC signing keys, configure local DNS
make init

# 2. Copy and fill in .env
cp .env.example .env
# Edit .env — set MAVERICS_IMAGE, SNOWFLAKE_ACCOUNT_URL, SNOWFLAKE_ADMIN_USER, SNOWSQL_PWD

# 3. Start all containers
make up

# 4. Verify everything is running
make smoke-test

# 5. Apply the Snowflake-side DDL (one-time, idempotent)
make snowflake-setup

# 6. Run the federation demo end-to-end
make snowflake-demo
```

The demo mints a JWT from Maverics, decodes it (you'll see all four agent claims), and runs a TPC-H query against Snowflake using that JWT. No service account anywhere — the JWT is valid because Snowflake's `EXTERNAL_OAUTH_INTEGRATION` trusts Maverics' JWKS, and the role activates from the `scope` claim.

## What success looks like

```
==> 1. Minting JWT from Maverics OIDC Provider (mcp-client-cli-snowflake)

==> 2. JWT payload (agent claims injected by the Service Extension)
  iss: https://auth.orchestrator.lab
  sub: mcp-client-cli-snowflake
  aud: https://auth.orchestrator.lab
  scope: session:role:MAVERICS_DEMO_ROLE snowflake:cortex_analyst snowflake:cortex_search snowflake:sql_execution
  agent_type: mcp-client
  agent_provider: anthropic
  agent_instance_id: snowflake-demo-1777...
  delegation_purpose: snowflake-read

==> 3. Snowflake SQL API call with the Maverics JWT
    sql: SELECT C_NAME, C_NATIONKEY, C_ACCTBAL FROM SNOWFLAKE_SAMPLE_DATA.TPCH_SF1.CUSTOMER ORDER BY C_ACCTBAL DESC LIMIT 5
  Columns: ['C_NAME', 'C_NATIONKEY', 'C_ACCTBAL']
  Row: ['Customer#000061453', 15, 9999.99]
  Row: ['Customer#000069321', 15, 9999.96]
  ...
```

## Architecture

```
┌────────────────────┐  1. POST /oauth2/token (PKCE or client_credentials)
│   Claude Desktop   │ ─────────────────────────────────────────────────┐
│   / MCP client     │                                                  │
│   (via mcp-remote) │ ◄────────────────  Maverics-signed JWT  ─────────│
└─────────┬──────────┘     (agent claims injected by Service Extension) │
          │                                                             ▼
          │                                                  ┌────────────────────┐
          │                                                  │ Maverics OIDC      │
          │                                                  │ Provider           │
          │                                                  │  • mcp-client-cli- │
          │                                                  │    snowflake app   │
          │                                                  │  • token-minting   │
          │                                                  │    OPA policy      │
          │                                                  │  • buildAccess…SE  │
          │                                                  └─────────┬──────────┘
          │                                                            │ JWKS published
          │                                                            │ at /oauth2/jwks
          │ 2. Call Snowflake managed MCP with the JWT                 │
          ▼                                                            │
┌──────────────────────────────────────────────┐    validates against  │
│ Snowflake                                    │ ◄─────────────────────┘
│  • EXTERNAL_OAUTH_INTEGRATION                │
│    (trusts Maverics issuer + JWKS)           │
│  • Managed MCP server                        │
│  • RBAC / row-access / masking policies      │
│    can read the agent_* claims               │
└──────────────────────────────────────────────┘
```

See `apps/snowflake/architecture.svg` for the customer-facing version.

## Connect Claude Desktop (production user flow)

The `make snowflake-demo` script uses `client_credentials` for an unattended end-to-end test. For Claude Desktop, register the Snowflake-bound MCP server with `mcp-remote`. Open the Claude Desktop config:

- **macOS:** `~/Library/Application Support/Claude/claude_desktop_config.json`
- **Windows:** `%APPDATA%\Claude\claude_desktop_config.json`

Add:

```json
{
  "mcpServers": {
    "snowflake": {
      "command": "/opt/homebrew/bin/npx",
      "args": [
        "-y",
        "mcp-remote",
        "https://<your-account>.snowflakecomputing.com/api/v2/databases/MAVERICS_DEMO/schemas/MCP/mcp-servers/MAVERICS_AGENT_MCP",
        "3335",
        "--host", "127.0.0.1",
        "--transport", "http-only",
        "--static-oauth-client-info", "{\"client_id\":\"mcp-client-cli-snowflake\"}",
        "--static-oauth-metadata", "{\"issuer\":\"https://auth.orchestrator.lab\",\"authorization_endpoint\":\"https://auth.orchestrator.lab/oauth2/auth\",\"token_endpoint\":\"https://auth.orchestrator.lab/oauth2/token\"}"
      ],
      "env": {
        "NODE_EXTRA_CA_CERTS": "/path/to/connect-snowflake-to-maverics/certs/rootCA.pem"
      }
    }
  }
}
```

Restart Claude Desktop. On first use, a browser window opens for Keycloak authentication. The Maverics-issued JWT will have `sub` = the user's Keycloak `sub`. To support this path on the Snowflake side, create a Snowflake user with `LOGIN_NAME` matching that Keycloak `sub` and grant it `MAVERICS_DEMO_ROLE` — see `apps/snowflake/README.md`.

## Test users (for the Claude Desktop flow)

| User | Email | Password |
|------|-------|----------|
| John McClane | john.mcclane@orchestrator.lab | yippiekayay |
| Sarah Connor | sarah.connor@orchestrator.lab | judgmentday |

## Key files

| File | Purpose |
|------|---------|
| `orchestrator/oidc-provider/maverics.yaml` | OAuth Authorization Server config; the `mcp-client-cli-snowflake` app is the Snowflake-bound client |
| `orchestrator/oidc-provider/service-extensions/agent-claims.go` | Go SE that injects `agent_type`, `agent_provider`, `agent_instance_id`, `delegation_purpose` |
| `orchestrator/oidc-provider/policies/snowflake-token-minting.rego` | OPA policy gating the mint on the presence of `session:role:*` |
| `apps/snowflake/setup.sql` | Snowflake-side DDL: warehouse, role, user, EXTERNAL_OAUTH integration, managed MCP server |
| `apps/snowflake/architecture.svg` | High-level customer-facing diagram |
| `apps/snowflake/DEMO.md` | Presenter-facing live demo script |
| `apps/snowflake/GAPS.md` | What's not yet shipping on either side — roadmap items for both Maverics and Snowflake |
| `scripts/snowflake-setup.sh` | Renders `setup.sql` with env-driven values and applies it via `snowsql` |
| `scripts/snowflake-demo.sh` | One-command end-to-end demo: mint JWT, decode, query, optional MCP probe |

## Makefile targets

| Target | Description |
|--------|-------------|
| `make init` | Generate TLS certs, OIDC keys, configure DNS |
| `make up` | Start all containers |
| `make down` | Stop and remove all containers + volumes |
| `make logs` | Tail container logs |
| `make smoke-test` | Verify services are healthy |
| `make snowflake-setup` | Apply `apps/snowflake/setup.sql` against your Snowflake account |
| `make snowflake-demo` | Mint a JWT, decode the agent claims, run a TPC-H query end-to-end |

## Reference docs

- [Strata: AI Identity Gateway overview](https://docs.strata.io/guides/ai-identity/)
- [Strata: Service Extensions — OIDC hooks](https://docs.strata.io/reference/orchestrator/service-extensions/oidc)
- [Strata: Token Brokering (experimental)](https://docs.strata.io/reference/orchestrator/experimental/token-brokering)
- [Snowflake: External OAuth Custom](https://docs.snowflake.com/en/user-guide/oauth-custom)
- [Snowflake: Managed MCP servers](https://docs.snowflake.com/en/release-notes/2025/other/2025-11-04-cortex-agents-mcp)
- [RFC 7519: JSON Web Tokens](https://datatracker.ietf.org/doc/html/rfc7519)
- [RFC 8693: OAuth 2.0 Token Exchange](https://datatracker.ietf.org/doc/html/rfc8693)
- [RFC 9728: OAuth 2.0 Protected Resource Metadata](https://datatracker.ietf.org/doc/html/rfc9728)

## Troubleshooting

| Issue | Fix |
|-------|-----|
| `mkcert: command not found` | Install: `brew install mkcert` (macOS) or see [mkcert docs](https://github.com/FiloSottile/mkcert#installation) |
| DNS not resolving | Run `make dns-setup` and restart your browser |
| `KeyError: 'access_token'` in `snowflake-demo` | Env not loaded: run `set -a && . ./.env && set +a` first, then re-run |
| Snowflake says "Invalid OAuth access token" | The `iss` claim on the JWT must match `EXTERNAL_OAUTH_ISSUER` exactly. Maverics signs with `https://auth.orchestrator.lab` — make sure that's what the integration trusts |
| Snowflake says "role not listed in the Access Token" | The `scope` claim must include `session:role:<role>`. Snowflake parses that and looks up the role name in `EXTERNAL_OAUTH_ALLOWED_ROLES_LIST` |
| Snowflake managed MCP `tools/call` returns "Error parsing response" | Tracked Snowflake-side bug — see `apps/snowflake/GAPS.md` item #11. `tools/list` works; the same JWT runs SQL fine against `/api/v2/statements` |
