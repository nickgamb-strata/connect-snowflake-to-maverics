// Package main is a Maverics Service Extension that injects compounded agent
// identity claims — and, for workforce flows, the user's email and Keycloak
// sub — into access tokens issued by the OIDC Provider.
//
// Wired on apps[].buildAccessTokenClaimsSE for the mcp-client-cli-snowflake
// client. The OIDC Provider invokes BuildAccessTokenClaims during token
// minting (every grant type, including authorization_code, client_credentials,
// and token-exchange); the map this function returns is merged into the
// access token before the token is signed and returned to the caller.
//
// Why this injects email:
//   The OIDC `email` scope authorizes /userinfo to expose the user's email
//   but does NOT by itself put `email` into the access-token JWT. Maverics'
//   declarative `claimsMapping` block populates ID tokens / userinfo, also
//   not the access token. Snowflake's EXTERNAL_OAUTH_INTEGRATION matches
//   identity from the access-token JWT, so without this SE no resource
//   server sees the workforce user's email — only the compound
//   `sub: <idp-url>|<keycloak-uuid>` Maverics builds for delegated sessions.
//
// In the lab flow:
//   MCP client (Claude Desktop, CLI, …) --POST /oauth2/token--> Maverics OIDC
//                                                                |
//                                                                v
//                                          BuildAccessTokenClaims fires here
//                                                                |
//                                                                v
//                                            JWT issued with agent_type,
//                                            agent_provider, agent_instance_id,
//                                            delegation_purpose attached
//
// Snowflake's EXTERNAL_OAUTH_INTEGRATION validates the JWT against Maverics'
// JWKS. The agent claims survive into the Snowflake session and are visible
// to row-access policies, masking policies, and query history.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/strata-io/service-extension/orchestrator"
	"github.com/strata-io/service-extension/session"
)

// BuildAccessTokenClaims is the hook function called by the Orchestrator's
// embedded Go runtime. The signature must match the buildAccessTokenClaimsSE
// contract documented at:
//   https://docs.strata.io/reference/orchestrator/service-extensions/oidc#buildaccesstokenclaimsse
func BuildAccessTokenClaims(api orchestrator.Orchestrator, req *http.Request) (map[string]interface{}, error) {
	log := api.Logger()

	if err := req.ParseForm(); err != nil {
		log.Error("agent-claims-se", "failed to parse token request form", "error", err.Error())
		return nil, err
	}

	clientID := req.PostForm.Get("client_id")
	if clientID == "" {
		// client_secret_basic — the client_id is in the Basic auth header instead.
		if u, _, ok := req.BasicAuth(); ok {
			clientID = u
		}
	}

	instanceID := req.Header.Get("X-Agent-Instance")
	if instanceID == "" {
		instanceID = newRequestID()
	}

	purpose := req.PostForm.Get("delegation_purpose")
	if purpose == "" {
		purpose = "snowflake-read"
	}

	claims := map[string]interface{}{
		"agent_type":         deriveAgentType(clientID),
		"agent_provider":     deriveAgentProvider(clientID),
		"agent_instance_id":  instanceID,
		"delegation_purpose": purpose,
	}

	// On authorization_code (workforce) grants there is a user session bound
	// to the request with attributes populated by the keycloak OIDC connector
	// during the auth code exchange. Pull email + keycloak.sub out of it so
	// Snowflake's EXTERNAL_OAUTH integration can match on email — the
	// declarative claimsMapping in maverics.yaml populates ID tokens / userinfo
	// but not the access-token JWT, so this is the canonical hook for putting
	// workforce identity claims into the access token.
	//
	// On client_credentials grants there is no user session; sess.GetString
	// returns an error and we leave the claims off. The downstream resource
	// server falls back to the `sub` claim (= the OAuth client_id), which
	// maps to the corresponding Snowflake service user.
	if sess, err := api.Session(session.WithRequest(req)); err == nil {
		if email, e := sess.GetString("keycloak.email"); e == nil && email != "" {
			claims["email"] = email
		}
		if ksub, e := sess.GetString("keycloak.sub"); e == nil && ksub != "" {
			claims["keycloak_sub"] = ksub
		}
	}

	log.Info("agent-claims-se", "injected access-token claims",
		"client_id", clientID,
		"agent_type", claims["agent_type"],
		"agent_provider", claims["agent_provider"],
		"agent_instance_id", claims["agent_instance_id"],
		"delegation_purpose", claims["delegation_purpose"],
		"email", claims["email"],
		"keycloak_sub", claims["keycloak_sub"],
	)

	return claims, nil
}

// deriveAgentType maps an OAuth client_id to the agent platform it represents.
// New agent platforms federated through Maverics get a new case here.
func deriveAgentType(clientID string) string {
	switch {
	case strings.HasPrefix(clientID, "mcp-client-cli"):
		// Public MCP clients — Claude Desktop, Claude Code, Cursor, etc. all
		// register under this client today in the connect-claude tutorial.
		return "mcp-client"
	case strings.HasPrefix(clientID, "claude-"):
		return "claude"
	case strings.HasPrefix(clientID, "openai-"):
		return "openai"
	case strings.HasPrefix(clientID, "cursor-"):
		return "cursor"
	case strings.HasPrefix(clientID, "bedrock-agentcore"):
		return "bedrock-agentcore"
	default:
		return clientID
	}
}

// deriveAgentProvider returns the vendor that operates the agent platform.
func deriveAgentProvider(clientID string) string {
	switch {
	case strings.HasPrefix(clientID, "mcp-client-cli"):
		return "anthropic"
	case strings.HasPrefix(clientID, "claude-"):
		return "anthropic"
	case strings.HasPrefix(clientID, "openai-"):
		return "openai"
	case strings.HasPrefix(clientID, "cursor-"):
		return "anysphere"
	case strings.HasPrefix(clientID, "bedrock-agentcore"):
		return "aws"
	default:
		return "unknown"
	}
}

// newRequestID generates a 16-character hex identifier when the agent does not
// supply X-Agent-Instance. Not a security primitive — just a way to keep the
// agent_instance_id claim populated for traceability when no upstream value
// exists.
func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
