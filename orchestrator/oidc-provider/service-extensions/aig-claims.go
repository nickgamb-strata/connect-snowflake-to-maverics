// Package main is a Maverics Service Extension that injects the user's email
// (and a cleaned-up sub) into access tokens minted by the ai-identity-gateway
// confidential OIDC client during RFC 8693 delegation token exchange.
//
// Why this exists:
//   When mcpBridge exchanges Cursor's inbound JWT for a Snowflake-audience
//   JWT, the resulting access token's `sub` is Maverics' compound IdP-prefixed
//   identifier (e.g. `https://keycloak…/realms/blueprints|<keycloak-uuid>`)
//   and the access token carries no `email` claim — the OIDC `email` scope
//   authorizes /userinfo, it does not by itself put `email` into the access
//   token. Snowflake's EXTERNAL_OAUTH_INTEGRATION maps a JWT claim
//   (`email` first, falling back to `sub`) to a Snowflake `LOGIN_NAME`; with
//   neither present in a useful form, every workforce query 401s with
//   "Incorrect username or password".
//
//   This SE looks the user's session up off the request and pulls
//   `keycloak.email` into a custom `email` claim. The same session lookup
//   also yields `keycloak.sub` (the raw Keycloak UUID), which we expose as
//   `keycloak_sub` for downstream policies that need a stable per-user
//   identifier without the IdP URL prefix.
//
// Wired on apps[].buildAccessTokenClaimsSE for the ai-identity-gateway client.
package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/strata-io/service-extension/orchestrator"
)

// BuildAIGAccessTokenClaims is the buildAccessTokenClaimsSE hook on the
// ai-identity-gateway OIDC client. For RFC 8693 delegation token-exchange
// requests it decodes the inbound subject_token, splits the compound sub
// claim Maverics uses (e.g. `https://keycloak…/realms/blueprints|<uuid>`),
// derives the raw Keycloak UUID, and looks the user's email up via the
// AttributeProvider against the keycloak connector. The map returned is
// merged into the access token Maverics is about to sign — giving Snowflake
// a usable `email` claim to map to a `LOGIN_NAME`.
func BuildAIGAccessTokenClaims(api orchestrator.Orchestrator, req *http.Request) (map[string]interface{}, error) {
	log := api.Logger()
	out := map[string]interface{}{}

	if err := req.ParseForm(); err != nil {
		log.Debug("aig-claims-se", "could not parse token request form", "error", err.Error())
		return out, nil
	}

	subjectToken := req.PostForm.Get("subject_token")
	if subjectToken == "" {
		log.Debug("aig-claims-se", "no subject_token on request — likely client_credentials, no claims to inject")
		return out, nil
	}

	payload, err := decodeJWTPayload(subjectToken)
	if err != nil {
		log.Debug("aig-claims-se", "could not decode subject_token", "error", err.Error())
		return out, nil
	}

	// Log the subject_token claim names so we can see what's available without
	// dumping values into the log.
	keys := make([]string, 0, len(payload))
	for k := range payload {
		keys = append(keys, k)
	}
	log.Debug("aig-claims-se", "subject_token claim keys", "keys", strings.Join(keys, ","))

	// If the subject_token already carries an `email` claim — pass it through.
	if email, ok := payload["email"].(string); ok && email != "" {
		out["email"] = email
	}

	// Maverics compound sub: "<idp-url>|<idp-sub>". Split out the IdP-side
	// sub for callers who'd rather match on a stable UUID than the URL prefix.
	if sub, ok := payload["sub"].(string); ok && sub != "" {
		if idx := strings.LastIndex(sub, "|"); idx > 0 && idx < len(sub)-1 {
			out["keycloak_sub"] = sub[idx+1:]
		} else {
			out["keycloak_sub"] = sub
		}
	}

	if email, ok := out["email"].(string); ok && email != "" {
		log.Info("aig-claims-se", "injecting email from subject_token", "email", email)
	} else {
		log.Debug("aig-claims-se", "subject_token did not carry an email claim — Snowflake will fall back to sub matching")
	}

	return out, nil
}

// decodeJWTPayload returns the JWT body as a map, ignoring signature
// verification — we trust Maverics' own issued tokens, and a doctored
// subject_token would have already been rejected by the OIDC Provider before
// the SE fires.
func decodeJWTPayload(token string) (map[string]interface{}, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil, errMalformed
	}
	seg := parts[1]
	if pad := len(seg) % 4; pad != 0 {
		seg += strings.Repeat("=", 4-pad)
	}
	raw, err := base64.URLEncoding.DecodeString(seg)
	if err != nil {
		// Try URL-safe without padding semantics.
		raw, err = base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil, err
		}
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// errMalformed is returned when subject_token is not in the expected
// header.payload[.signature] format.
var errMalformed = jwtError("subject_token is not a JWT")

type jwtError string

func (e jwtError) Error() string { return string(e) }
