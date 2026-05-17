# AI Identity Gateway — Blog Example

A self-contained example of the Maverics AI Identity Gateway protecting MCP servers with OAuth 2.0 identity governance. One command to start. Connect Claude in 60 seconds.

Companion to: [Your MCP Server Is a Resource Server Now. Act Like It.](../blog.md)

## Prerequisites

- Docker Desktop (or Docker Engine + Compose v2)
- [mkcert](https://github.com/FiloSottile/mkcert) — local TLS certificate generation
- [Node.js](https://nodejs.org/) — required for `mcp-remote` (Claude Desktop connection)
  ```bash
  brew install node    # macOS
  ```
- Maverics Orchestrator image from [Strata](https://www.strata.io/) (loaded via `docker load`)

## Quick Start

```bash
# 1. Generate TLS certificates, OIDC signing keys, and configure local DNS
make init

# 2. Copy .env.example to .env and set the Maverics image tag
cp .env.example .env
# Edit .env → set MAVERICS_IMAGE to match your loaded image tag

# 3. Start all containers
make up

# 4. Verify everything is running
make smoke-test
```

## Connect Claude Code

If you use Claude Code, add the example MCP using the following command:

```bash
claude mcp add --transport http \
  --client-id mcp-client-cli \
  --callback-port 19876 \
  ai-identity-gateway \
  https://gateway.orchestrator.lab/mcp
```

If you use Claude Desktop, add the MCP gateway using `mcp-remote`. First, open your config file:

**macOS:** `~/Library/Application Support/Claude/claude_desktop_config.json`
**Windows:** `%APPDATA%\Claude\claude_desktop_config.json`

Then add the following to the `mcpServers` object:

```json
{
  "mcpServers": {
    "ai-identity-gateway": {
      "command": "/opt/homebrew/bin/npx",
      "args": [
        "mcp-remote",
        "https://gateway.orchestrator.lab/mcp",
        "3334",
        "--transport", "http-only",
        "--static-oauth-client-info",
        "{\"client_id\":\"mcp-client-cli\"}"
      ],
      "env": {
        "NODE_EXTRA_CA_CERTS": "/path/to/connect-claude-to-maverics/certs/rootCA.pem"
      }
    }
  }
}
```

> **Note:** Update the `NODE_EXTRA_CA_CERTS` path to point to the `certs/rootCA.pem` in your clone of this repository. This is required because the lab uses locally-generated TLS certificates.

Restart Claude Desktop after saving the file.

When prompted, authenticate as one of the test users below.

For additional connection methods (Claude Desktop, claude.ai web connectors), see:
→ [Strata Docs: Connect Claude](https://docs.strata.io/guides/ai-identity/connect/claude)

## Test Users

| User | Email | Password |
|------|-------|----------|
| John McClane | john.mcclane@orchestrator.lab | yippiekayay |
| Sarah Connor | sarah.connor@orchestrator.lab | judgmentday |

## Architecture

```
Claude Code ──→ Envoy (TLS) ──→ AI Identity Gateway (Maverics)
                                   ├── MCP Proxy → Enterprise Ledger (Go MCP server)
                                   └── MCP Bridge → Employee Directory (Go REST API)
                                         ↕
                              OIDC Provider (Maverics) ←→ Keycloak (IdP)
                                         ↕
                                   Redis (cache) + Vault (secrets)
```

## MCP Tools Available

| Namespace | Tool | Scope | Access |
|-----------|------|-------|--------|
| enterprise_ledger_ | listAccounts | ledger:ListAccounts | All authenticated users |
| enterprise_ledger_ | getAccount | ledger:GetAccount | All authenticated users |
| enterprise_ledger_ | getTransactions | ledger:ListTransactions | All authenticated users |
| enterprise_ledger_ | updateAccountStatus | ledger:UpdateAccount | All authenticated users |
| enterprise_ledger_ | getCustomerPII | ledger:ReadPII | Requires `pii:read` scope |
| enterprise_ledger_ | getAuditLog | ledger:ReadAudit | Requires `audit:read` scope |
| employee_directory_ | listEmployees | employee:List | All authenticated users |
| employee_directory_ | getEmployee | employee:Get | All authenticated users |
| employee_directory_ | createEmployee | employee:Create | All authenticated users |
| employee_directory_ | updateEmployee | employee:Update | All authenticated users |
| employee_directory_ | deactivateEmployee | employee:Deactivate | All authenticated users |
| employee_directory_ | getDirectReports | employee:List | All authenticated users |
| employee_directory_ | listDepartments | department:List | All authenticated users |

## Key Files

| File | Purpose |
|------|---------|
| `orchestrator/oidc-provider/maverics.yaml` | OAuth authorization server config |
| `orchestrator/ai-identity-gateway/maverics.yaml` | MCP gateway config (proxy + bridge) |
| `orchestrator/ai-identity-gateway/policies/*.rego` | OPA authorization policies |
| `keycloak/blueprints-realm.json` | Keycloak realm with test users and clients |
| `apps/enterprise-ledger/` | Go MCP server with tiered data access |
| `apps/employee-directory/` | Go REST API with OpenAPI spec |

## Makefile Targets

| Target | Description |
|--------|-------------|
| `make init` | Generate TLS certs, OIDC keys, configure DNS |
| `make up` | Start all containers |
| `make down` | Stop and remove all containers + volumes |
| `make logs` | Tail container logs |
| `make smoke-test` | Verify services are healthy |

## Building From Scratch

Want to build this example from scratch instead of cloning it? Follow the Strata setup docs:
- [AI Identity Gateway Overview](https://docs.strata.io/guides/ai-identity/)
- [MCP Bridge](https://docs.strata.io/maverics/apps/mcp-bridge/)
- [MCP Proxy](https://docs.strata.io/maverics/apps/mcp-proxy/)
- [OIDC Provider](https://docs.strata.io/maverics/oidc-provider/)

## Troubleshooting

| Issue | Fix |
|-------|-----|
| `mkcert: command not found` | Install: `brew install mkcert` (macOS) or see [mkcert docs](https://github.com/FiloSottile/mkcert#installation) |
| DNS not resolving | Run `make dns-setup` and restart your browser |
| OAuth redirect mismatch | Ensure `--callback-port 19876` matches the `redirectURLs` in the OIDC Provider config |
| TLS certificate errors | Run `make init` again to regenerate certs |
| Containers won't start | Check `MAVERICS_IMAGE` in `.env` matches your loaded image tag |
| OAuth callback fails in Claude Desktop | Run `./reset-demo.sh` and restart Claude Desktop (see below) |

### Resetting OAuth State (Claude Desktop)

Claude Desktop can occasionally spawn multiple `mcp-remote` instances during startup, which creates parallel OAuth flows that race against each other. This can cause PKCE verification failures at the Keycloak callback or session timeouts.

If the OAuth login page opens but the callback redirect fails, run the reset script:

```bash
./reset-demo.sh
```

This kills any stale `mcp-remote` processes and clears the cached OAuth tokens. After running it, restart Claude Desktop — a fresh browser window will open for authentication.
