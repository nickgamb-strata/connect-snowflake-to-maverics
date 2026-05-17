// Package main is a Maverics Service Extension that injects compounded agent
// identity claims into access tokens issued by the OIDC Provider.
//
// Wired on apps[].buildAccessTokenClaimsSE for the mcp-client-cli-snowflake
// client. The OIDC Provider invokes BuildAccessTokenClaims during token
// minting (every grant type, including authorization_code, client_credentials,
// and token-exchange); the map this function returns is merged into the
// access token before the token is signed and returned to the caller.
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

	log.Info("agent-claims-se", "injected agent claims",
		"client_id", clientID,
		"agent_type", claims["agent_type"],
		"agent_provider", claims["agent_provider"],
		"agent_instance_id", claims["agent_instance_id"],
		"delegation_purpose", claims["delegation_purpose"],
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
