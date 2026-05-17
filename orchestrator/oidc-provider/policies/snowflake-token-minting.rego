package orchestrator

# Token-minting policy on the bedrock-agentcore-snowflake OIDC app. Fires
# before the SE injects agent claims and before Maverics signs the JWT, so
# this is the gate that decides whether a Snowflake-bound token gets issued.
#
# Snowflake's EXTERNAL_OAUTH integration parses the JWT's `scope` claim with
# EXTERNAL_OAUTH_SCOPE_DELIMITER, strips the `session:role:` prefix from each
# token, and looks the resulting role name up in
# EXTERNAL_OAUTH_ALLOWED_ROLES_LIST. So our scope claim must include a
# `session:role:<role>` token before Snowflake will activate any role.
#
# This policy enforces that minted tokens always carry that prefix. Anything
# else — bare role names, no scope at all, only per-tool scopes — is denied
# with a clear message the demo can show.

default result["allowed"] := false

result["allowed"] if {
	contains(input.request.oauth.scope, "session:role:")
}

result["external_message"] := "Token mint denied: Snowflake-bound tokens must carry a session:role:* scope so Snowflake's EXTERNAL_OAUTH activates a role (e.g. session:role:MAVERICS_DEMO_ROLE)." if {
	not contains(input.request.oauth.scope, "session:role:")
}
