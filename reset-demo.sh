#!/usr/bin/env bash
set -euo pipefail

# Reset mcp-remote auth state for the AI Identity Gateway demo.
# Run this if Claude Desktop gets stuck during the OAuth callback
# (e.g. PKCE mismatch, timeout, or stale session).

echo "Stopping mcp-remote processes..."
pkill -f mcp-remote 2>/dev/null && echo "  killed mcp-remote" || echo "  no mcp-remote running"

echo "Clearing mcp-remote auth cache..."
rm -rf ~/.mcp-auth/mcp-remote-*/
echo "  cleared ~/.mcp-auth/mcp-remote-*/"

echo ""
echo "Done. Restart Claude Desktop to re-authenticate."
