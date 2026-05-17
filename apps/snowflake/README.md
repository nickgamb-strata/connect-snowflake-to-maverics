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

## Two grant types, two `sub` values

This tutorial registers a single Maverics OIDC client
(`mcp-client-cli-snowflake`) that supports two grant types. The `sub` claim
in the issued JWT depends on which one fires:

- **`client_credentials`** — driven by `scripts/snowflake-demo.sh`. `sub` =
  `mcp-client-cli-snowflake`. Snowflake's `EXTERNAL_OAUTH` maps that to
  `MAVERICS_AGENT` via `LOGIN_NAME`. Use this for non-interactive tests, CI,
  and the demo script.

- **`authorization_code` + PKCE** — driven by Claude Desktop (or any other
  PKCE-capable MCP client). The user authenticates against Keycloak; `sub` =
  the user's Keycloak `sub`. To support this path, create a second Snowflake
  user with `LOGIN_NAME` set to that Keycloak `sub` and grant it
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
