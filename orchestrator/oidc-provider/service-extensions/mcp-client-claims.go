// Package main is a Maverics Service Extension that injects the user's email
// into access tokens issued by the mcp-client-cli (public PKCE) OIDC client.
//
// Why this exists:
//   The OIDC `email` scope authorizes /userinfo to expose the user's email
//   but does NOT by itself put `email` into the access-token JWT. Maverics'
//   declarative `claimsMapping` block populates ID tokens and userinfo, also
//   not the access token. So Cursor/Claude end up with an access token whose
//   only identity claim is the compound `sub: <idp-url>|<keycloak-uuid>` —
//   not useful for resource servers like Snowflake that match on email.
//
//   The downstream `mcpBridge` does RFC 8693 delegation token exchange
//   against `ai-identity-gateway`. Whatever claims the inbound access token
//   carries become the `subject_token` claims the exchange SE can mirror
//   into the outbound JWT. So putting email here closes the loop:
//
//     Cursor → mcp-client-cli access token (with email) → bridge exchange
//       → ai-identity-gateway exchanged token (with email forwarded by SE)
//       → Snowflake EXTERNAL_OAUTH matches email → LOGIN_NAME → role activates
//
// Wired on apps[].buildAccessTokenClaimsSE for the mcp-client-cli app.
package main

import (
	"net/http"

	"github.com/strata-io/service-extension/orchestrator"
	"github.com/strata-io/service-extension/session"
)

// BuildMCPClientAccessTokenClaims is the buildAccessTokenClaimsSE hook on the
// mcp-client-cli OIDC client. It pulls the user's email + Keycloak sub from
// the session that the authorization_code flow populates and exposes both as
// custom access-token claims.
func BuildMCPClientAccessTokenClaims(api orchestrator.Orchestrator, req *http.Request) (map[string]interface{}, error) {
	log := api.Logger()
	out := map[string]interface{}{}

	sess, err := api.Session(session.WithRequest(req))
	if err != nil {
		log.Debug("mcp-client-claims-se", "no session on request — skipping", "error", err.Error())
		return out, nil
	}

	if email, err := sess.GetString("keycloak.email"); err == nil && email != "" {
		out["email"] = email
	}
	if sub, err := sess.GetString("keycloak.sub"); err == nil && sub != "" {
		out["keycloak_sub"] = sub
	}

	if len(out) == 0 {
		log.Debug("mcp-client-claims-se", "session present but no keycloak attributes")
		return out, nil
	}

	log.Info("mcp-client-claims-se", "injecting workforce identity into access token",
		"email", out["email"],
		"keycloak_sub", out["keycloak_sub"],
	)
	return out, nil
}
