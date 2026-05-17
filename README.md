# Connect Snowflake to Maverics

A self-contained example connecting Claude Desktop (and other MCP clients) to **Snowflake's managed MCP server** through Maverics-issued JWTs — federated trust without a service account. Snowflake validates the JWT against Maverics' JWKS via `EXTERNAL_OAUTH_INTEGRATION`. Maverics is **not** in the data path on the actual query.

This tutorial is the technical companion to [**"Databricks and Snowflake MCP servers your security team will actually approve"**](https://www.strata.io/blog/databricks-and-snowflake-mcp-servers-your-security-team-will-actually-approve/) — that post frames *why* shared service accounts are an audit dead-end and lays out the Federated Exchange pattern; this repo is the working *how* against Snowflake's managed MCP. The companion dev post — *Connect Snowflake to Maverics: Federated Identity for Workforce AI Clients* — is on [maverics.ai/blog](https://www.maverics.ai/blog).

This tutorial starts from the same lab baseline as [connect-claude-to-maverics](https://github.com/nickgamb-strata/connect-claude-to-maverics) and layers the Snowflake federation on top. The parallel [connect-aws-bedrock-to-maverics](https://github.com/nickgamb-strata/connect-aws-bedrock-to-maverics) tutorial does the same with AWS Bedrock AgentCore. You can follow either, or both — they don't depend on each other.

## What you get

- The Maverics AI Identity Gateway protecting two MCP backends (Enterprise Ledger, Employee Directory) — the same baseline as the Claude tutorial.
- A new Maverics OIDC client (`mcp-client-cli-snowflake`) wired with two Go Service Extensions: one injects agent identity claims (`agent_type`, `agent_provider`, `agent_instance_id`, `delegation_purpose`) on every token mint, and a second hook puts the user's `email` + Keycloak `sub` into the access-token JWT so workforce users authenticate cleanly through `EXTERNAL_OAUTH`.
- A Snowflake `EXTERNAL_OAUTH_INTEGRATION` that trusts Maverics' issuer + JWKS, maps `email` (falling back to `sub`) to `LOGIN_NAME`, and activates `MAVERICS_DEMO_ROLE` from the `scope` claim.
- A managed MCP server on Snowflake (`MAVERICS_DEMO.MCP.MAVERICS_AGENT_MCP`) exposing a `SYSTEM_EXECUTE_SQL` tool against the TPC-H sample data.
- An end-to-end demo script that mints a JWT, decodes the agent claims, and runs a TPC-H query under the federated identity.
- An *optional* `mcpBridge` workaround under [`optional/snowflake-bridge-workaround/`](optional/snowflake-bridge-workaround/) for a Snowflake server-side bug currently blocking `tools/call` against `SYSTEM_EXECUTE_SQL`. Comes back out of the default config once Snowflake ships a fix.

## Prerequisites

- Docker Desktop (or Docker Engine + Compose v2)
- [mkcert](https://github.com/FiloSottile/mkcert) for local TLS
- [Node.js](https://nodejs.org/) for `mcp-remote` (Claude Desktop connection)
- [snowsql](https://docs.snowflake.com/en/user-guide/snowsql) for `make snowflake-setup`. If you'd rather not install it, paste `apps/snowflake/setup.sql` into a Snowflake worksheet after substituting `__SNOWFLAKE_ACCOUNT_URL__`, `__MAVERICS_INTERNAL_ISSUER__`, and `__MAVERICS_JWKS_URL__` with your values.
- Maverics Orchestrator image from [Strata](https://www.strata.io/), loaded via `docker load`
- A Snowflake account. The 30-day trial works — `EXTERNAL_OAUTH_INTEGRATION TYPE = CUSTOM` is available on all editions.
- A publicly-reachable hostname for Maverics' JWKS endpoint. Snowflake fetches JWKS from the public internet during JWT validation. The Makefile assumes [Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/); ngrok or any other public reverse proxy works the same way.

> **Production caveat — read this before you ship anything.** The Cloudflare Tunnel pattern in this repo is a **local-lab convenience** for getting `docker-compose` reachable from the public internet without provisioning real infrastructure. **Do not run production this way.** In a real deployment, publish JWKS from a hardened public origin — a static `jwks.json` behind a CDN, a managed cloud function (Cloud Run / Lambda / Cloudflare Workers), or your existing API gateway / edge proxy in front of Maverics. The signing key never leaves the Orchestrator; only the public JWKS needs to be reachable. The tunnel here is purely so the lab works end-to-end on one developer machine.

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

Restart Claude Desktop. On first use, a browser window opens for Keycloak authentication. The exchanged JWT carries the user's Keycloak `email` and `sub`. The Snowflake `EXTERNAL_OAUTH_INTEGRATION` from `setup.sql` is configured to map either claim to `LOGIN_NAME` (`EXTERNAL_OAUTH_TOKEN_USER_MAPPING_CLAIM = ('email','sub')`), and `setup.sql` pre-provisions the two test users below with email-based `LOGIN_NAME`s. To onboard your own workforce, add `CREATE USER` statements whose `LOGIN_NAME` matches each human's stable IdP claim (typically email) and grant them `MAVERICS_DEMO_ROLE` — see `apps/snowflake/README.md`.

## If `tools/call` fails — front Snowflake's REST API with `mcpBridge` (optional)

> **Skip this whole section if everything above works.** Only relevant if your MCP client gets `tools/list` back fine but every `tools/call` against `SYSTEM_EXECUTE_SQL` errors out with `Error parsing response`.

At the time of writing, Snowflake's managed MCP server has a server-side bug where `tools/call SYSTEM_EXECUTE_SQL` returns `MCP Server tool error: Error parsing response` under `EXTERNAL_OAUTH` authentication, even for trivial `SELECT 42`. The same JWT runs the same SQL fine against `/api/v2/statements` (which is why `make snowflake-demo` keeps working). It's a server-side issue we have an open report on — until it's fixed, the optional workaround below keeps the agent flow working without losing any of the federation properties.

The shape of the workaround: bypass the managed MCP endpoint, front Snowflake's REST API with your own MCP-speaking server, and let Maverics' [`mcpBridge`](https://docs.strata.io/reference/orchestrator/applications/mcp-bridge) generate the MCP tool surface from an OpenAPI spec — zero handwritten MCP code. The federated JWT still lands in Snowflake the same way; only the wire path between the MCP client and Snowflake changes.

### Three pieces, each small

1. **A tiny REST shim** (~50 lines, any language) that accepts `POST /sql` with `{sql, warehouse, role}`, passes the inbound `Authorization` header through, adds the `X-Snowflake-Authorization-Token-Type: OAUTH` header Snowflake's REST API requires, and forwards to `/api/v2/statements`. Run it on the same Docker network as the Maverics gateway. Python sketch:

   ```python
   @app.route("/sql", methods=["POST"])
   def run_sql():
       auth = request.headers["Authorization"]
       body = request.get_json()
       req = urllib.request.Request(
           f"{SNOWFLAKE_ACCOUNT_URL}/api/v2/statements",
           data=json.dumps({
               "statement": body["sql"],
               "warehouse": body.get("warehouse", "MAVERICS_WH"),
               "role":      body.get("role",      "MAVERICS_DEMO_ROLE"),
               "timeout":   30,
           }).encode(),
           method="POST",
           headers={
               "Authorization": auth,
               "X-Snowflake-Authorization-Token-Type": "OAUTH",
               "Content-Type": "application/json",
           },
       )
       return jsonify(json.loads(urllib.request.urlopen(req).read()))
   ```

2. **An OpenAPI spec** for that `POST /sql` endpoint with a `bearerAuth` security scheme. `mcpBridge` reads it and generates a `runQuery` MCP tool with input schema derived from your request body. The full file is under 100 lines.

3. **An `mcpBridge` app** on the AI Identity Gateway Orchestrator (`orchestrator/ai-identity-gateway/maverics.yaml`). It mounts the OpenAPI spec, baseURL's the shim, and does delegation `tokenExchange` so the inbound user JWT becomes a Snowflake-audience JWT before the request leaves the gateway:

   ```yaml
   apps:
     - name: snowflake-bridge
       type: mcpBridge
       mode: openapi
       toolNamespace:
         name: snowflake_
       openapi:
         spec:
           uri: file:///etc/maverics/apps/snowflake-shim/openapi.yaml
         baseURL: http://snowflake-shim:5000
       authorization:
         outbound:
           type: tokenExchange
           tokenExchange:
             type: delegation
             idp: oidc-provider
             audience: https://<your-account>.snowflakecomputing.com/
             tools:
               - name: runQuery
                 ttl: 60s
                 scopes:
                   - name: session:role:MAVERICS_DEMO_ROLE
                   - name: snowflake:sql_execution
   ```

The MCP client (Claude Desktop, Cursor, …) then points at `https://gateway.orchestrator.lab/mcp` instead of Snowflake's managed-MCP URL. The user signs in through Keycloak once; every subsequent tool call rides on a Snowflake-audience JWT validated against the same `EXTERNAL_OAUTH_INTEGRATION` you already configured. Same `MAVERICS_DEMO_ROLE`, same `LOGIN_HISTORY` / `QUERY_HISTORY` reconciliation, same Service Extension claims riding through on the exchanged token.

The existing `employee-directory-bridge` app in `orchestrator/ai-identity-gateway/maverics.yaml` is a direct template — same `mcpBridge` shape, same `tokenExchange.tools[]` block, only the OpenAPI spec, baseURL, audience, and scopes differ. See the [`mcpBridge` reference](https://docs.strata.io/reference/orchestrator/applications/mcp-bridge) and the [AI Identity Gateway MCP Bridge guide](https://docs.strata.io/guides/ai-identity/mcp-bridge) for the full configuration surface.

When Snowflake's `tools/call` lands a fix, delete the shim service, delete the `snowflake-bridge` stanza, and point your MCP client back at the managed-MCP URL — no other changes needed.

## Test users (for the Claude Desktop flow)

| User | Email | Password |
|------|-------|----------|
| John McClane | john.mcclane@orchestrator.lab | yippiekayay |
| Sarah Connor | sarah.connor@orchestrator.lab | judgmentday |

## Key files

| File | Purpose |
|------|---------|
| `orchestrator/oidc-provider/maverics.yaml` | OAuth Authorization Server config — `mcp-client-cli-snowflake` is the Snowflake-bound client, `mcp-client-cli` is the public PKCE client for MCP tools, `ai-identity-gateway` is the confidential client that brokers RFC 8693 delegation token exchange. Snowflake account URL is sourced from `secrets.yaml` (which reads from `.env`) so personal account IDs never get committed. |
| `orchestrator/oidc-provider/service-extensions/agent-claims.go` | Go SE wired on `mcp-client-cli-snowflake` — injects `agent_type`, `agent_provider`, `agent_instance_id`, `delegation_purpose`, plus the user's `email` and Keycloak `sub` when there's a workforce session. |
| `orchestrator/oidc-provider/service-extensions/mcp-client-claims.go` | Go SE wired on `mcp-client-cli` — puts `email` + Keycloak `sub` on the access-token JWT for workforce flows through the AI Identity Gateway. Maverics' declarative `claimsMapping` covers ID tokens / userinfo but not access-token JWTs. |
| `orchestrator/oidc-provider/service-extensions/aig-claims.go` | Go SE wired on the `ai-identity-gateway` confidential client — decodes the inbound subject_token during delegation token exchange and forwards `email` onto the exchanged JWT. Required by the optional bridge workaround so Snowflake matches workforce users by email. |
| `orchestrator/oidc-provider/policies/snowflake-token-minting.rego` | OPA policy gating the mint on presence of `session:role:*`. |
| `apps/snowflake/setup.sql` | Snowflake-side DDL: warehouse, role, service + workforce users, `EXTERNAL_OAUTH_INTEGRATION` (maps `('email','sub')` → `LOGIN_NAME`), managed MCP server. |
| `apps/snowflake/architecture.svg` | High-level customer-facing diagram. |
| `apps/snowflake/DEMO.md` | Presenter-facing live demo script. |
| `apps/snowflake/GAPS.md` | What's not yet shipping on either side — roadmap items for both Maverics and Snowflake. |
| `optional/snowflake-bridge-workaround/` | Workaround for the Snowflake managed-MCP `tools/call` bug — shim source, OpenAPI spec, OPA policy, and full README. Wired into the default `docker-compose.yml`; remove cleanly when Snowflake ships a fix. |
| `SNOWFLAKE-BUG-REPORT.md` | JIRA-ready reproduction + diagnostic writeup for the `tools/call` bug that motivates `optional/snowflake-bridge-workaround/`. |
| `scripts/snowflake-setup.sh` | Renders `setup.sql` with env-driven values and applies it via `snowsql`. |
| `scripts/snowflake-demo.sh` | One-command end-to-end demo: mint JWT, decode, query, optional MCP probe. |
| `secrets.yaml` + `vault/seed.sh` | Vault seeding — `seed.sh` expands `${SNOWFLAKE_ACCOUNT_URL}` placeholders in `secrets.yaml` from the `.env` file so the committed YAML stays free of personal details. |

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

- ["Databricks and Snowflake MCP servers your security team will actually approve"](https://www.strata.io/blog/databricks-and-snowflake-mcp-servers-your-security-team-will-actually-approve/) — the strategy-side companion piece. This tutorial is its technical implementation.
- [Strata: AI Identity Gateway overview](https://docs.strata.io/guides/ai-identity/)
- [Strata: Service Extensions — OIDC hooks](https://docs.strata.io/reference/orchestrator/service-extensions/oidc) — including `buildAccessTokenClaimsSE`
- [Strata: MCP Bridge App](https://docs.strata.io/reference/orchestrator/applications/mcp-bridge) — REST→MCP tool generation (the pattern under `optional/snowflake-bridge-workaround/`)
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
| Snowflake managed MCP `tools/call` returns "Error parsing response" | Tracked Snowflake-side bug — `tools/list` works, and the same JWT runs SQL fine against `/api/v2/statements` (which is why `make snowflake-demo` keeps working). See the [`mcpBridge` workaround](#if-toolscall-fails--front-snowflakes-rest-api-with-mcpbridge-optional) above and `apps/snowflake/GAPS.md` item #11 |
