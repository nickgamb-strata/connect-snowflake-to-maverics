# Maverics × Snowflake — gap analysis

What this tutorial demonstrates today, on shipping product on both sides,
and what would need to ship next to make the pattern frictionless.

Working chain proven in this repo: MCP client → Maverics-issued JWT (with
`agent_type`, `agent_provider`, `agent_instance_id`, `delegation_purpose`
injected by a Service Extension) → Snowflake via `EXTERNAL_OAUTH_INTEGRATION`
trusting Maverics' JWKS. The `sub` claim maps to a Snowflake user; the
`scope` claim activates the role.

## Where this maps to a five-stage compounded-JWT lifecycle

| Lifecycle stage | This tutorial's implementation |
|---|---|
| **Stage 1** — Partner key registration | `EXTERNAL_OAUTH_INTEGRATION` registers Maverics' issuer + JWKS URL once per Snowflake account |
| **Stage 2** — Per-customer integration | Snowflake admin runs `setup.sql` once; creates the security integration, role, service user, managed-MCP server |
| **Stage 3** — Standard OAuth login | Claude Desktop (or any MCP client) drives `authorization_code` + PKCE; the demo script uses `client_credentials` for unattended testing |
| **Stage 4** — Compounded JWT mint | Maverics OIDC Provider mints the JWT with `agent_type`, `agent_provider`, `agent_instance_id`, `delegation_purpose` injected by `buildAccessTokenClaimsSE` |
| **Stage 5** — Policy enforcement | Snowflake's session activates `MAVERICS_DEMO_ROLE` from the `scope` claim; row-access and masking policies can read the agent claims via `CURRENT_OAUTH_ACCESS_TOKEN_INFO()` |

## Maverics — gaps to close

| # | Gap | Today's workaround | What it would unlock |
|---|---|---|---|
| 1 | Declarative custom claims on OIDC apps | `buildAccessTokenClaimsSE` Go service extension (`orchestrator/oidc-provider/service-extensions/agent-claims.go`) | A YAML `accessToken.customClaims` block would let admins keep agent-claim policy in the same surface as the rest of the OIDC config — no Go required for the standard case. |
| 2 | Per-app default audience override | Maverics defaults `aud` to the issuer URL on `client_credentials` when the caller doesn't pass `resource=` | Today Snowflake's `EXTERNAL_OAUTH_AUDIENCE_LIST` has to include the Maverics issuer URL. A per-app `accessToken.defaultAudience` would let JWTs carry the platform-specific audience without caller cooperation. |
| 3 | `tokenBrokering` graduates from experimental | Not used in this tutorial — federated trust uses plain OIDC + EXTERNAL_OAUTH | When data platforms ship a Stage-4 compounded-mint endpoint, the natural successor pattern is Token Brokering Federated Exchange. GA stability is the prerequisite security teams will check. |
| 4 | Per-mapping JWT TTL on `tokenBrokering` mappings | Token TTL is per-app via `accessToken.lifetimeSeconds` | Issuing short-lived tokens for Snowflake while keeping longer tokens for other audiences would let one Maverics deployment hold a tighter blast radius for sensitive backends. |
| 5 | Inbound→brokered agent-identity propagation | The SE reads `client_id` and `X-Agent-Instance` headers explicitly | When an upstream gateway has already extracted agent identity from an inbound token, automatic forwarding into the brokered JWT would remove the SE entirely for the standard case. |

## Snowflake — gaps to suggest

| # | Gap | What it would unlock |
|---|---|---|
| 6 | **Native Stage-4 compounded-JWT mint endpoint** | Today `EXTERNAL_OAUTH_INTEGRATION` only validates an external JWT and maps `sub` → user. A Stage-4 endpoint that takes a partner's signed assertion and returns a Snowflake-flavored JWT — preserving the agent claims alongside Snowflake's own RBAC binding — would let Stage-5 policies reference a Snowflake-issued token directly. |
| 7 | Tier-1 partner trust UX | Replace raw `CREATE SECURITY INTEGRATION` SQL with an admin UI to register a federation partner (Maverics, AgentCore, etc.). Lower friction for security and platform teams to onboard new federated issuers. |
| 8 | RFC 9728 Protected Resource Metadata on managed MCP | Publish `/.well-known/oauth-protected-resource` on managed-MCP endpoints so MCP clients can auto-discover the OAuth setup. Today MCP clients have to be told explicitly where the OAuth server is because Snowflake's managed MCP doesn't advertise it. |
| 9 | Per-tool scope vocabulary on managed MCP | First-class scopes like `cortex_analyst`, `cortex_search`, `sql_execution` rather than mapping every authorization decision to a Snowflake role. Lets agent platforms request the smallest envelope they need without proliferating role definitions. |
| 10 | Custom-claim availability to Snowflake policies | `CURRENT_OAUTH_ACCESS_TOKEN_INFO()` returns a JSON object that includes broker-injected claims, but the integration with row-access / masking / Cortex consent surfaces is undocumented. A first-class API for "Snowflake policies see the agent identity that called this query" would close the loop on Stage-5. |
| 11 | **`SYSTEM_EXECUTE_SQL` MCP tool returns "Error parsing response" on every `tools/call`** | The chain works through `tools/list` (Maverics-issued JWT accepted, role activates, tool list returns). Every `tools/call` on this tool type returns `{"isError": true, "content": [{"text": "MCP error calling tool ...: MCP Server tool error: Error parsing response"}]}`, including trivial `SELECT 42`. The same JWT executes the same SQL successfully against `/api/v2/statements`. Tracking it here because it's the single largest gap between the federation pattern working and a clean end-to-end MCP-tool-call demo. |

## Bottom line

The federation pattern is buildable today, on shipping product on both
sides, with one Go Service Extension and one `CREATE SECURITY INTEGRATION`.
The Maverics items above are roadmap polish (declarative claims, per-app
audience, Token Brokering GA). The Snowflake items above are the next steps
to a fully-frictionless compounded-JWT picture.
