package orchestrator

# Default allow — most Enterprise Ledger tools are permitted for authenticated users.
# Sensitive tools (getCustomerPII, getAuditLog) require elevated scopes.
default result["allowed"] := true

# Helper: decode the JWT to inspect scopes
jwt_payload := payload if {
	auth_header := input.request.http.headers.Authorization
	startswith(auth_header, "Bearer ")
	token := substring(auth_header, 7, -1)
	[_, payload, _] := io.jwt.decode(token)
}

# ── getCustomerPII: requires pii:read scope ──────────────────────
result["allowed"] := false if {
	input.request.mcp.tool.params.name == "getCustomerPII"
	not contains(jwt_payload.scope, "pii:read")
}

result["external_message"] := "Access denied: PII access requires pii:read scope. Re-authenticate with elevated privileges." if {
	input.request.mcp.tool.params.name == "getCustomerPII"
	not contains(jwt_payload.scope, "pii:read")
}

# ── getAuditLog: requires audit:read scope ────────────────────────
result["allowed"] := false if {
	input.request.mcp.tool.params.name == "getAuditLog"
	not contains(jwt_payload.scope, "audit:read")
}

result["external_message"] := "Insufficient privileges: audit log access requires audit:read scope." if {
	input.request.mcp.tool.params.name == "getAuditLog"
	not contains(jwt_payload.scope, "audit:read")
}
