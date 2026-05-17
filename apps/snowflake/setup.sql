-- Snowflake-side bootstrap for the Maverics-federated demo.
--
-- What this creates:
--   * A small XSMALL warehouse, demo database, and a low-privilege role.
--   * A service user MAVERICS_AGENT whose LOGIN_NAME matches the `sub`
--     claim Maverics will issue in the federated JWT (ai-identity-gateway).
--   * Read-only access to the SNOWFLAKE_SAMPLE_DATA.TPCH_SF1 schema so the
--     demo agent can run Cortex Analyst / SQL queries against real data.
--   * MAVERICS_EXTERNAL_OAUTH security integration that trusts Maverics'
--     issuer and JWKS. Snowflake's managed MCP and SQL endpoints validate
--     incoming Maverics-issued JWTs against this integration.
--
-- Environment substitutions performed by scripts/snowflake-setup.sh before
-- snowsql executes this file:
--   __SNOWFLAKE_ACCOUNT_URL__     e.g. https://abc12345.snowflakecomputing.com
--   __MAVERICS_INTERNAL_ISSUER__  the issuer URL Maverics signs JWTs with (internal lab hostname)
--   __MAVERICS_JWKS_URL__         publicly-reachable JWKS endpoint Snowflake fetches keys from
--
-- Re-runnable: every CREATE uses IF NOT EXISTS or CREATE OR REPLACE so this
-- file can be applied repeatedly without errors.

USE ROLE ACCOUNTADMIN;

-- ── Warehouse & database ─────────────────────────────────────────────────
CREATE WAREHOUSE IF NOT EXISTS MAVERICS_WH
  WAREHOUSE_SIZE = 'XSMALL'
  AUTO_SUSPEND = 60
  AUTO_RESUME = TRUE
  INITIALLY_SUSPENDED = TRUE;

CREATE DATABASE IF NOT EXISTS MAVERICS_DEMO;

-- ── Demo role + grants ───────────────────────────────────────────────────
CREATE ROLE IF NOT EXISTS MAVERICS_DEMO_ROLE;
GRANT USAGE ON WAREHOUSE MAVERICS_WH TO ROLE MAVERICS_DEMO_ROLE;
GRANT USAGE ON DATABASE MAVERICS_DEMO TO ROLE MAVERICS_DEMO_ROLE;

-- TPC-H sample data is what the demo queries via Cortex Analyst.
GRANT IMPORTED PRIVILEGES ON DATABASE SNOWFLAKE_SAMPLE_DATA TO ROLE MAVERICS_DEMO_ROLE;

-- ── Federated users — service + workforce ────────────────────────────────
-- Snowflake looks up a Snowflake user from one of the JWT's identity claims
-- (configured below as ('email','sub') — Snowflake tries email first, falls
-- back to sub if no match). The two flows in this tutorial issue different
-- claims:
--
--   1. client_credentials grants (scripts/snowflake-demo.sh) — `sub` is the
--      OAuth client_id, "mcp-client-cli-snowflake". No `email` claim.
--      Maps to MAVERICS_AGENT below.
--
--   2. authorization_code grants (Claude Desktop / Cursor via Keycloak) —
--      `email` is the user's Keycloak email; `sub` is their Keycloak UUID.
--      Maps to JOHN_MCCLANE / SARAH_CONNOR below by email.
--
-- For your own workforce, replace the two test users with `CREATE USER`
-- statements whose LOGIN_NAME matches each human's IdP email — or whatever
-- claim your IdP guarantees stable per user. TYPE = SERVICE keeps the demo
-- off Snowflake's seat count; for production workforce identities use
-- TYPE = PERSON so MFA/SCIM/lifecycle policies apply.

CREATE USER IF NOT EXISTS MAVERICS_AGENT
  TYPE = SERVICE
  LOGIN_NAME = 'mcp-client-cli-snowflake'
  DEFAULT_ROLE = MAVERICS_DEMO_ROLE
  DEFAULT_WAREHOUSE = MAVERICS_WH
  COMMENT = 'Service identity for client_credentials demo flow. JWT sub maps here.';
GRANT ROLE MAVERICS_DEMO_ROLE TO USER MAVERICS_AGENT;

CREATE USER IF NOT EXISTS JOHN_MCCLANE
  TYPE = SERVICE
  LOGIN_NAME = 'john.mcclane@orchestrator.lab'
  DEFAULT_ROLE = MAVERICS_DEMO_ROLE
  DEFAULT_WAREHOUSE = MAVERICS_WH
  COMMENT = 'Test workforce user (matches Keycloak email).';
GRANT ROLE MAVERICS_DEMO_ROLE TO USER JOHN_MCCLANE;

CREATE USER IF NOT EXISTS SARAH_CONNOR
  TYPE = SERVICE
  LOGIN_NAME = 'sarah.connor@orchestrator.lab'
  DEFAULT_ROLE = MAVERICS_DEMO_ROLE
  DEFAULT_WAREHOUSE = MAVERICS_WH
  COMMENT = 'Test workforce user (matches Keycloak email).';
GRANT ROLE MAVERICS_DEMO_ROLE TO USER SARAH_CONNOR;

-- ── EXTERNAL_OAUTH integration trusting Maverics ────────────────────────
-- Snowflake validates incoming JWTs against the JWS keys at
-- __MAVERICS_JWKS_URL__. The audience list must match `aud` in the JWT.
--
-- Issuer / audience asymmetry. Maverics' OIDC Provider is configured with an
-- INTERNAL issuer URL (https://auth.orchestrator.lab) but exposes its JWKS
-- through a PUBLIC Cloudflare Tunnel hostname so Snowflake can fetch keys.
-- Tokens are signed with `iss` and `aud` set to the internal hostname, so
-- both EXTERNAL_OAUTH_ISSUER and one entry in EXTERNAL_OAUTH_AUDIENCE_LIST
-- use the internal value. Only EXTERNAL_OAUTH_JWS_KEYS_URL is fetched
-- across the network and so uses the public hostname.
--
-- Scope claim handling. Maverics emits the scope list in a `scope` claim
-- with space-delimited tokens; Snowflake's defaults are `scp` claim with
-- comma delimiter, so we override both. The value Snowflake parses is
-- `session:role:MAVERICS_DEMO_ROLE` — Snowflake strips `session:role:` and
-- looks the resulting role name up in EXTERNAL_OAUTH_ALLOWED_ROLES_LIST.
--
-- ANY_ROLE_MODE = DISABLE keeps the activation surface tight — only roles
-- explicitly listed in EXTERNAL_OAUTH_ALLOWED_ROLES_LIST may be activated.
CREATE OR REPLACE SECURITY INTEGRATION MAVERICS_EXTERNAL_OAUTH
  TYPE = EXTERNAL_OAUTH
  ENABLED = TRUE
  EXTERNAL_OAUTH_TYPE = CUSTOM
  EXTERNAL_OAUTH_ISSUER = '__MAVERICS_INTERNAL_ISSUER__'
  EXTERNAL_OAUTH_JWS_KEYS_URL = '__MAVERICS_JWKS_URL__'
  EXTERNAL_OAUTH_AUDIENCE_LIST = ('__MAVERICS_INTERNAL_ISSUER__', '__SNOWFLAKE_ACCOUNT_URL__/')
  -- Map JWTs by `email` first (matches the workforce users above), fall back
  -- to `sub` (which on client_credentials grants is the OAuth client_id, e.g.
  -- "mcp-client-cli-snowflake" → MAVERICS_AGENT). Listing both lets the same
  -- integration serve service-account and human-workforce flows.
  EXTERNAL_OAUTH_TOKEN_USER_MAPPING_CLAIM = ('email', 'sub')
  EXTERNAL_OAUTH_SNOWFLAKE_USER_MAPPING_ATTRIBUTE = 'LOGIN_NAME'
  EXTERNAL_OAUTH_ANY_ROLE_MODE = 'DISABLE'
  EXTERNAL_OAUTH_ALLOWED_ROLES_LIST = ('MAVERICS_DEMO_ROLE')
  EXTERNAL_OAUTH_SCOPE_MAPPING_ATTRIBUTE = 'scope'
  EXTERNAL_OAUTH_SCOPE_DELIMITER = ' '
  COMMENT = 'Trusts Maverics-issued JWTs. Source of truth for agent identity claims.';

-- ── Snowflake managed MCP server ────────────────────────────────────────
-- Snowflake's managed MCP servers (GA Nov 2025) are SQL-defined objects.
-- Each MCP server has a tool spec and is exposed at a stable URL pattern:
--   /api/v2/databases/<DB>/schemas/<SCHEMA>/mcp-servers/<MCP_SERVER_NAME>
--
-- We create one server with a single SYSTEM_EXECUTE_SQL tool. AgentCore
-- (or any other MCP client) authenticates with a Maverics-issued JWT, which
-- the EXTERNAL_OAUTH integration above validates.
USE DATABASE MAVERICS_DEMO;
CREATE SCHEMA IF NOT EXISTS MAVERICS_DEMO.MCP;
USE SCHEMA MAVERICS_DEMO.MCP;

CREATE OR REPLACE MCP SERVER MAVERICS_DEMO.MCP.MAVERICS_AGENT_MCP
  FROM SPECIFICATION $$
tools:
  - name: "run_snowflake_query"
    type: "SYSTEM_EXECUTE_SQL"
    title: "Run Snowflake SQL"
    description: "Execute a read-only SQL query against the Snowflake account. Use for analytics, schema exploration, and Cortex/data inspection."
$$;

-- The demo role needs USAGE on the schema and on the MCP server itself.
GRANT USAGE ON SCHEMA MAVERICS_DEMO.MCP TO ROLE MAVERICS_DEMO_ROLE;
GRANT USAGE ON MCP SERVER MAVERICS_DEMO.MCP.MAVERICS_AGENT_MCP TO ROLE MAVERICS_DEMO_ROLE;

-- ── Sanity probes (informational, safe to skip) ──────────────────────────
SHOW SECURITY INTEGRATIONS LIKE 'MAVERICS_EXTERNAL_OAUTH';
SHOW USERS LIKE 'MAVERICS_AGENT';
SHOW USERS LIKE 'JOHN_MCCLANE';
SHOW USERS LIKE 'SARAH_CONNOR';
SHOW MCP SERVERS LIKE 'MAVERICS_AGENT_MCP' IN SCHEMA MAVERICS_DEMO.MCP;
