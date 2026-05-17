# Snowflake federated trust — setup

`setup.sql` provisions everything Snowflake-side for the federated-trust
demo: warehouse, demo database, low-privilege role, service user mapped to
the Maverics-issued `sub` claim, the `EXTERNAL_OAUTH` security integration
that trusts Maverics' JWKS, and a managed MCP server with a SQL-execution
tool.

## Free-tier reminder

Snowflake's 30-day trial supports `EXTERNAL_OAUTH_INTEGRATION TYPE = CUSTOM` —
no Enterprise edition required. Pick the smallest available cloud region;
the TPC-H sample data this demo queries is replicated everywhere.

## JWKS reachability (local-lab vs. production)

The setup script writes `EXTERNAL_OAUTH_JWS_KEYS_URL` into the security
integration; Snowflake fetches that URL during JWT validation. In this lab
that's a Cloudflare Tunnel pointed at Maverics' `/oauth2/jwks` — convenient
because it lets `docker-compose` be reachable from Snowflake's network
without any infrastructure. **Don't do this in production.** Publish JWKS
from your normal hardened public origin (CDN-fronted static `jwks.json`,
managed cloud function, API gateway in front of Maverics, etc.) and point
`BEDROCK_AUTH_HOSTNAME` at that host instead. Maverics never moves; only
the URL Snowflake fetches from changes.

## What the demo expects

After `make snowflake-setup` runs `setup.sql`:

| Object | Identifier | Purpose |
|---|---|---|
| Warehouse | `MAVERICS_WH` | XSMALL, auto-suspends after 60s |
| Database | `MAVERICS_DEMO` | Empty placeholder; sample data lives in SNOWFLAKE_SAMPLE_DATA |
| Role | `MAVERICS_DEMO_ROLE` | Granted `USAGE` on the warehouse, database, and TPC-H sample |
| Service user | `MAVERICS_AGENT` | `LOGIN_NAME = 'mcp-client-cli-snowflake'` matches the Maverics-issued JWT `sub` on `client_credentials` grants |
| Security integration | `MAVERICS_EXTERNAL_OAUTH` | Trusts the Maverics issuer + JWKS URL |
| Managed MCP server | `MAVERICS_DEMO.MCP.MAVERICS_AGENT_MCP` | One `SYSTEM_EXECUTE_SQL` tool exposed at `/api/v2/databases/MAVERICS_DEMO/schemas/MCP/mcp-servers/MAVERICS_AGENT_MCP` |

## Running it

`scripts/snowflake-setup.sh` substitutes the env-driven placeholders in
`setup.sql` before piping into `snowsql`. Required env vars (in `.env`):

```
SNOWFLAKE_ACCOUNT_URL=https://<account>.snowflakecomputing.com
SNOWFLAKE_ADMIN_USER=<account-admin>
SNOWSQL_PWD=<account-admin-password>          # ephemeral, only for setup
```

Then:

```bash
make snowflake-setup
```

The script is idempotent — every `CREATE` uses `IF NOT EXISTS` or
`CREATE OR REPLACE`, so it's safe to re-run.

## Two grant types, two user-mapping paths

`mcp-client-cli-snowflake` (the Maverics OIDC client in this tutorial)
supports two grant types. They issue different identity claims, and
`MAVERICS_EXTERNAL_OAUTH` is configured to map either claim to `LOGIN_NAME`
via `EXTERNAL_OAUTH_TOKEN_USER_MAPPING_CLAIM = ('email','sub')` — Snowflake
tries `email` first, falls back to `sub`. This lets the same security
integration serve both service-account and human-workforce flows.

- **`client_credentials`** — driven by `scripts/snowflake-demo.sh`. The JWT
  has `sub = mcp-client-cli-snowflake` and no `email` claim. Snowflake's
  `EXTERNAL_OAUTH` falls through to `sub` and maps it to **`MAVERICS_AGENT`**
  (whose `LOGIN_NAME` is `mcp-client-cli-snowflake`). Use this for
  non-interactive tests, CI, and the demo script.

- **`authorization_code` + PKCE** — driven by Claude Desktop, Cursor, or any
  other PKCE-capable MCP client. The user authenticates against Keycloak;
  the exchanged JWT carries `email = john.mcclane@orchestrator.lab` (or
  whichever Keycloak user logged in) and `sub` = the Keycloak UUID.
  `setup.sql` pre-provisions two test users that match the test Keycloak
  identities by email — **`JOHN_MCCLANE`** (`LOGIN_NAME =
  'john.mcclane@orchestrator.lab'`) and **`SARAH_CONNOR`** (`LOGIN_NAME =
  'sarah.connor@orchestrator.lab'`). To onboard your own workforce, replace
  those two with `CREATE USER` statements whose `LOGIN_NAME` matches each
  human's stable IdP email (or the claim of your choice) and grant
  `MAVERICS_DEMO_ROLE`.

Either way the federation mechanics are identical — Maverics signs, Snowflake
validates against the JWKS, the role activates from the `scope` claim, and
the agent claims from the Service Extension ride along into the session.

## Verifying

After setup, sanity-check from the Snowflake UI:

```sql
USE ROLE ACCOUNTADMIN;
SHOW SECURITY INTEGRATIONS LIKE 'MAVERICS_EXTERNAL_OAUTH';
DESCRIBE SECURITY INTEGRATION MAVERICS_EXTERNAL_OAUTH;
SHOW MCP SERVERS IN ACCOUNT;
```

Then run `make snowflake-demo` from the repo root to drive the full
mint → validate → query chain.

## Tearing down

For a clean slate (e.g. before changing the issuer hostname):

```sql
USE ROLE ACCOUNTADMIN;
DROP MCP SERVER IF EXISTS MAVERICS_DEMO.MCP.MAVERICS_AGENT_MCP;
DROP SCHEMA IF EXISTS MAVERICS_DEMO.MCP;
DROP SECURITY INTEGRATION IF EXISTS MAVERICS_EXTERNAL_OAUTH;
DROP USER IF EXISTS MAVERICS_AGENT;
DROP ROLE IF EXISTS MAVERICS_DEMO_ROLE;
DROP DATABASE IF EXISTS MAVERICS_DEMO;
DROP WAREHOUSE IF EXISTS MAVERICS_WH;
```

The trial credit balance survives — only the demo objects are removed.
