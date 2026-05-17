package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/urfave/cli/v2"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Account represents a bank account
type Account struct {
	ID            string  `json:"id"`
	AccountNumber string  `json:"account_number"`
	HolderName    string  `json:"holder_name"`
	HolderEmail   string  `json:"holder_email"`
	Type          string  `json:"type"`
	Balance       float64 `json:"balance"`
	Status        string  `json:"status"`
	CreatedAt     string  `json:"created_at"`
}

// Transaction represents a financial transaction
type Transaction struct {
	ID           string  `json:"id"`
	AccountID    string  `json:"account_id"`
	Type         string  `json:"type"`
	Amount       float64 `json:"amount"`
	Description  string  `json:"description"`
	Counterparty string  `json:"counterparty"`
	Timestamp    string  `json:"timestamp"`
}

// CustomerPII represents sensitive personally identifiable information
type CustomerPII struct {
	AccountID   string `json:"account_id"`
	SSN         string `json:"ssn"`
	DateOfBirth string `json:"date_of_birth"`
	Phone       string `json:"phone"`
	Address     string `json:"address"`
}

// AuditEntry represents an internal audit log entry
type AuditEntry struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Action    string `json:"action"`
	User      string `json:"user"`
	Resource  string `json:"resource"`
	Details   string `json:"details"`
}

// In-memory store
type Store struct {
	mu           sync.RWMutex
	accounts     map[string]Account
	transactions map[string][]Transaction
	customerPII  map[string]CustomerPII
	auditLog     []AuditEntry
}

var store = &Store{
	accounts:     make(map[string]Account),
	transactions: make(map[string][]Transaction),
	customerPII:  make(map[string]CustomerPII),
	auditLog:     make([]AuditEntry, 0),
}

// JWKS cache
type JWKSCache struct {
	mu        sync.RWMutex
	jwks      *jose.JSONWebKeySet
	endpoint  string
	lastFetch time.Time
}

var jwksCache = &JWKSCache{}

// Global logger
var logger *zap.Logger

// JWT configuration
var jwtConfig struct {
	issuer   string
	audience string
}

// Whether auth is disabled (for unprotected mode)
var noAuth bool

// Actor represents an actor in the token exchange chain (RFC 8693)
type Actor struct {
	Sub      string `json:"sub,omitempty"`
	ClientID string `json:"client_id,omitempty"`
	Act      *Actor `json:"act,omitempty"`
}

// CustomClaims extends standard JWT claims with token exchange fields
type CustomClaims struct {
	jwt.Claims
	Email    string `json:"email,omitempty"`
	Name     string `json:"name,omitempty"`
	ClientID string `json:"client_id,omitempty"`
	Scope    string `json:"scope,omitempty"`
	Act      *Actor `json:"act,omitempty"`
}

// GetScopes returns the scopes as a slice
func (c *CustomClaims) GetScopes() []string {
	if c.Scope == "" {
		return nil
	}
	return strings.Split(c.Scope, " ")
}

// HasScope checks if the token has the required scope
func (c *CustomClaims) HasScope(required string) bool {
	for _, s := range c.GetScopes() {
		if s == required {
			return true
		}
	}
	return false
}

func main() {
	app := &cli.App{
		Name:  "enterprise-ledger",
		Usage: "MCP server for enterprise banking data",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "port",
				Value:   "8080",
				Usage:   "Port to listen on",
				EnvVars: []string{"PORT"},
			},
			&cli.StringFlag{
				Name:    "tls-cert",
				Usage:   "Path to TLS certificate file",
				EnvVars: []string{"TLS_CERT_FILE"},
			},
			&cli.StringFlag{
				Name:    "tls-key",
				Usage:   "Path to TLS key file",
				EnvVars: []string{"TLS_KEY_FILE"},
			},
			&cli.StringFlag{
				Name:    "jwt-issuer",
				Value:   "https://auth.orchestrator.lab",
				Usage:   "Expected JWT issuer",
				EnvVars: []string{"JWT_ISSUER"},
			},
			&cli.StringFlag{
				Name:    "jwt-audience",
				Value:   "https://enterprise-ledger.orchestrator.lab/",
				Usage:   "Expected JWT audience",
				EnvVars: []string{"JWT_AUDIENCE"},
			},
			&cli.StringFlag{
				Name:    "jwks-endpoint",
				Usage:   "JWKS endpoint URL (defaults to {jwt-issuer}/oauth2/jwks)",
				EnvVars: []string{"JWKS_ENDPOINT"},
			},
			&cli.StringFlag{
				Name:    "seed-file",
				Usage:   "Path to JSON file containing seed data",
				EnvVars: []string{"SEED_FILE"},
			},
			&cli.BoolFlag{
				Name:    "no-auth",
				Usage:   "Disable JWT authentication (unprotected mode)",
				EnvVars: []string{"NO_AUTH"},
			},
		},
		Action: runServer,
	}

	if err := app.Run(os.Args); err != nil {
		if logger != nil {
			logger.Fatal("application error", zap.Error(err))
		} else {
			fmt.Fprintf(os.Stderr, "Fatal: %v\n", err)
			os.Exit(1)
		}
	}
}

func runServer(c *cli.Context) error {
	port := c.String("port")
	tlsCert := c.String("tls-cert")
	tlsKey := c.String("tls-key")
	jwtConfig.issuer = c.String("jwt-issuer")
	jwtConfig.audience = c.String("jwt-audience")
	jwksEndpoint := c.String("jwks-endpoint")
	seedFile := c.String("seed-file")
	noAuth = c.Bool("no-auth")

	// Initialize zap logger
	config := zap.NewProductionConfig()
	config.EncoderConfig.TimeKey = "time"
	config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	config.EncoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder

	var err error
	logger, err = config.Build()
	if err != nil {
		return fmt.Errorf("failed to initialize logger: %w", err)
	}
	defer logger.Sync()

	if jwksEndpoint == "" {
		jwksEndpoint = jwtConfig.issuer + "/oauth2/jwks"
	}
	jwksCache.endpoint = jwksEndpoint

	// Load seed data if provided, otherwise use defaults
	if seedFile != "" {
		if err := loadSeedFile(seedFile); err != nil {
			return fmt.Errorf("failed to load seed file: %w", err)
		}
	} else {
		loadDefaultData()
	}

	// Create MCP server
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "enterprise-ledger",
		Version: "1.0.0",
	}, nil)

	// Register tools
	registerTools(server)

	// Create the streamable HTTP handler
	handler := mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
		return server
	}, nil)

	if noAuth {
		logger.Warn("AUTHENTICATION DISABLED - running in unprotected mode")
		logger.Info("starting Enterprise Ledger MCP server (unprotected)",
			zap.String("port", port),
		)
	} else {
		logger.Info("starting Enterprise Ledger MCP server",
			zap.String("port", port),
			zap.String("jwt_issuer", jwtConfig.issuer),
			zap.String("jwt_audience", jwtConfig.audience),
			zap.String("jwks_endpoint", jwksEndpoint),
		)
	}

	// Wrap with selective auth middleware (unless no-auth mode)
	var wrappedHandler http.Handler
	if noAuth {
		wrappedHandler = handler
	} else {
		wrappedHandler = selectiveAuthMiddleware(handler)
	}

	addr := ":" + port
	if tlsCert != "" && tlsKey != "" {
		logger.Info("TLS enabled", zap.String("cert", tlsCert))
		return http.ListenAndServeTLS(addr, tlsCert, tlsKey, wrappedHandler)
	}

	logger.Info("TLS disabled, running in HTTP mode")
	return http.ListenAndServe(addr, wrappedHandler)
}

// jwtTokenVerifier implements auth.TokenVerifier for JWT validation
func jwtTokenVerifier(ctx context.Context, tokenString string, req *http.Request) (*auth.TokenInfo, error) {
	// Parse the token
	parsedJWT, err := jwt.ParseSigned(tokenString, []jose.SignatureAlgorithm{jose.RS256, jose.ES256})
	if err != nil {
		logger.Warn("invalid token format", zap.Error(err))
		return nil, fmt.Errorf("%w: invalid token format", auth.ErrInvalidToken)
	}

	// Fetch JWKS
	jwks, err := fetchJWKS(ctx)
	if err != nil {
		logger.Error("failed to fetch JWKS", zap.Error(err))
		return nil, fmt.Errorf("failed to fetch JWKS: %w", err)
	}

	// Find the signing key
	var key *jose.JSONWebKey
	if len(parsedJWT.Headers) > 0 && parsedJWT.Headers[0].KeyID != "" {
		keys := jwks.Key(parsedJWT.Headers[0].KeyID)
		if len(keys) > 0 {
			key = &keys[0]
		}
	}

	if key == nil {
		logger.Warn("token signing key not found")
		return nil, fmt.Errorf("%w: signing key not found", auth.ErrInvalidToken)
	}

	// Validate the token
	var claims CustomClaims
	if err := parsedJWT.Claims(key.Key, &claims); err != nil {
		logger.Warn("invalid token signature", zap.Error(err))
		return nil, fmt.Errorf("%w: invalid signature", auth.ErrInvalidToken)
	}

	// Validate standard claims
	expected := jwt.Expected{
		Issuer:      jwtConfig.issuer,
		AnyAudience: []string{jwtConfig.audience},
		Time:        time.Now(),
	}

	if err := claims.Claims.Validate(expected); err != nil {
		logger.Warn("token validation failed", zap.Error(err))
		return nil, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
	}

	// Determine subject for logging
	subject := claims.Subject
	if claims.Email != "" {
		subject = claims.Email
	} else if claims.Name != "" {
		subject = claims.Name
	}

	// Build actor chain
	actors := buildActorSlice(claims.Act)

	// Get scopes
	scopes := claims.GetScopes()

	// Calculate TTL from issued at and expiry
	var ttlSeconds int64
	var issuedAtStr string
	if claims.IssuedAt != nil {
		issuedAt := claims.IssuedAt.Time()
		issuedAtStr = issuedAt.Format(time.RFC3339)
		if claims.Expiry != nil {
			ttlSeconds = int64(claims.Expiry.Time().Sub(issuedAt).Seconds())
		}
	}

	// Log authenticated request with full token metadata
	fields := []zap.Field{
		zap.String("method", req.Method),
		zap.String("path", req.URL.Path),
		zap.String("subject", subject),
	}
	if len(actors) > 0 {
		fields = append(fields, zap.Strings("actors", actors))
	}
	if len(scopes) > 0 {
		fields = append(fields, zap.Strings("scopes", scopes))
	}
	if claims.Issuer != "" {
		fields = append(fields, zap.String("issuer", claims.Issuer))
	}
	if issuedAtStr != "" {
		fields = append(fields, zap.String("issued_at", issuedAtStr))
	}
	if len(claims.Audience) > 0 {
		fields = append(fields, zap.Strings("audience", claims.Audience))
	}
	if ttlSeconds > 0 {
		fields = append(fields, zap.Int64("ttl_seconds", ttlSeconds))
	}
	logger.Info("authenticated request", fields...)

	// Build extra info for TokenInfo
	extra := map[string]any{
		"subject": subject,
		"actors":  actors,
		"scopes":  scopes,
	}
	if claims.Email != "" {
		extra["email"] = claims.Email
	}
	if claims.Name != "" {
		extra["name"] = claims.Name
	}
	if claims.Issuer != "" {
		extra["issuer"] = claims.Issuer
	}
	if issuedAtStr != "" {
		extra["issued_at"] = issuedAtStr
	}
	if len(claims.Audience) > 0 {
		extra["audience"] = claims.Audience
	}
	if ttlSeconds > 0 {
		extra["ttl_seconds"] = ttlSeconds
	}

	return &auth.TokenInfo{
		Expiration: claims.Expiry.Time(),
		Extra:      extra,
	}, nil
}

// buildActorSlice recursively builds a slice of actor IDs from the actor chain
func buildActorSlice(act *Actor) []string {
	if act == nil {
		return nil
	}

	actorID := act.Sub
	if actorID == "" {
		actorID = act.ClientID
	}
	if actorID == "" {
		actorID = "(unknown)"
	}

	actors := []string{actorID}
	if act.Act != nil {
		actors = append(actors, buildActorSlice(act.Act)...)
	}

	return actors
}

func fetchJWKS(ctx context.Context) (*jose.JSONWebKeySet, error) {
	jwksCache.mu.RLock()
	if jwksCache.jwks != nil && time.Since(jwksCache.lastFetch) < 5*time.Minute {
		defer jwksCache.mu.RUnlock()
		return jwksCache.jwks, nil
	}
	jwksCache.mu.RUnlock()

	jwksCache.mu.Lock()
	defer jwksCache.mu.Unlock()

	if jwksCache.jwks != nil && time.Since(jwksCache.lastFetch) < 5*time.Minute {
		return jwksCache.jwks, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksCache.endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create JWKS request: %w", err)
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: false,
			},
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch JWKS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("JWKS endpoint returned status %d", resp.StatusCode)
	}

	var jwks jose.JSONWebKeySet
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return nil, fmt.Errorf("failed to decode JWKS: %w", err)
	}

	jwksCache.jwks = &jwks
	jwksCache.lastFetch = time.Now()

	logger.Info("fetched JWKS", zap.Int("key_count", len(jwks.Keys)))
	return &jwks, nil
}

// selectiveAuthMiddleware wraps a handler and only requires authentication for tool calls.
// The initialize and tools/list methods are allowed without authentication to support
// MCP Proxy tool discovery, while actual tool invocations require valid JWT tokens.
func selectiveAuthMiddleware(next http.Handler) http.Handler {
	authHandler := auth.RequireBearerToken(jwtTokenVerifier, &auth.RequireBearerTokenOptions{})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read the body to inspect the method
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}
		// Restore the body for the next handler
		r.Body = io.NopCloser(bytes.NewReader(body))

		// Parse the JSON-RPC request to get the method
		var rpcReq struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal(body, &rpcReq); err == nil {
			// Allow these methods without authentication for tool discovery
			switch rpcReq.Method {
			case "initialize", "tools/list", "notifications/initialized":
				logger.Debug("allowing unauthenticated request for method", zap.String("method", rpcReq.Method))
				next.ServeHTTP(w, r)
				return
			}
		}

		// All other requests require authentication
		authHandler(next).ServeHTTP(w, r)
	})
}

func registerTools(server *mcp.Server) {
	// Normal sensitivity tools
	mcp.AddTool(server, &mcp.Tool{
		Name:        "listAccounts",
		Description: "List bank accounts with optional filtering by holder name, account type, or status",
	}, withScope("ledger:ListAccounts", listAccounts))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "getAccount",
		Description: "Get detailed information about a specific bank account by ID",
	}, withScope("ledger:GetAccount", getAccount))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "getTransactions",
		Description: "Get transaction history for a specific bank account",
	}, withScope("ledger:ListTransactions", getTransactions))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "updateAccountStatus",
		Description: "Update the status of a bank account (active, frozen, closed, under-review)",
	}, withScope("ledger:UpdateAccount", updateAccountStatus))

	// Sensitive tools
	mcp.AddTool(server, &mcp.Tool{
		Name:        "getCustomerPII",
		Description: "Get sensitive personally identifiable information for a customer including SSN, date of birth, phone number, and address",
	}, withScope("ledger:ReadPII", getCustomerPII))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "getAuditLog",
		Description: "Get internal audit log entries showing all administrative actions performed on accounts",
	}, withScope("ledger:ReadAudit", getAuditLog))

	logger.Info("registered MCP tools", zap.Int("count", 6))
}

// Tool parameter types

type ListAccountsParams struct {
	HolderName string `json:"holder_name,omitempty" jsonschema:"Filter by account holder name (partial match)"`
	Type       string `json:"type,omitempty" jsonschema:"Filter by account type (checking, savings, investment)"`
	Status     string `json:"status,omitempty" jsonschema:"Filter by account status (active, frozen, closed, under-review)"`
}

type GetAccountParams struct {
	ID string `json:"id" jsonschema:"Account ID,required"`
}

type GetTransactionsParams struct {
	AccountID string `json:"account_id" jsonschema:"Account ID to get transactions for,required"`
	Type      string `json:"type,omitempty" jsonschema:"Filter by transaction type (debit, credit, transfer)"`
}

type UpdateAccountStatusParams struct {
	ID     string `json:"id" jsonschema:"Account ID,required"`
	Status string `json:"status" jsonschema:"New status (active, frozen, closed, under-review),required"`
}

type GetCustomerPIIParams struct {
	AccountID string `json:"account_id" jsonschema:"Account ID to get PII for,required"`
}

type GetAuditLogParams struct {
	Resource string `json:"resource,omitempty" jsonschema:"Filter by resource (account number)"`
	Action   string `json:"action,omitempty" jsonschema:"Filter by action type"`
}

// Tool handlers

func listAccounts(ctx context.Context, req *mcp.CallToolRequest, params *ListAccountsParams) (*mcp.CallToolResult, any, error) {
	logToolCall(req, "listAccounts")

	store.mu.RLock()
	defer store.mu.RUnlock()

	var accounts []Account
	for _, a := range store.accounts {
		if params.HolderName != "" && !strings.Contains(strings.ToLower(a.HolderName), strings.ToLower(params.HolderName)) {
			continue
		}
		if params.Type != "" && a.Type != params.Type {
			continue
		}
		if params.Status != "" && a.Status != params.Status {
			continue
		}
		accounts = append(accounts, a)
	}

	result := map[string]any{
		"accounts": accounts,
		"total":    len(accounts),
	}

	data, _ := json.MarshalIndent(result, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(data)},
		},
	}, result, nil
}

func getAccount(ctx context.Context, req *mcp.CallToolRequest, params *GetAccountParams) (*mcp.CallToolResult, any, error) {
	logToolCall(req, "getAccount")

	if params.ID == "" {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "Account ID is required"}},
			IsError: true,
		}, nil, nil
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	account, exists := store.accounts[params.ID]
	if !exists {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Account not found: %s", params.ID)}},
			IsError: true,
		}, nil, nil
	}

	data, _ := json.MarshalIndent(account, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}, account, nil
}

func getTransactions(ctx context.Context, req *mcp.CallToolRequest, params *GetTransactionsParams) (*mcp.CallToolResult, any, error) {
	logToolCall(req, "getTransactions")

	if params.AccountID == "" {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "Account ID is required"}},
			IsError: true,
		}, nil, nil
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	// Verify account exists
	if _, exists := store.accounts[params.AccountID]; !exists {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Account not found: %s", params.AccountID)}},
			IsError: true,
		}, nil, nil
	}

	txns := store.transactions[params.AccountID]
	var filtered []Transaction
	for _, t := range txns {
		if params.Type != "" && t.Type != params.Type {
			continue
		}
		filtered = append(filtered, t)
	}

	result := map[string]any{
		"account_id":   params.AccountID,
		"transactions": filtered,
		"total":        len(filtered),
	}

	data, _ := json.MarshalIndent(result, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}, result, nil
}

func updateAccountStatus(ctx context.Context, req *mcp.CallToolRequest, params *UpdateAccountStatusParams) (*mcp.CallToolResult, any, error) {
	logToolCall(req, "updateAccountStatus")

	if params.ID == "" || params.Status == "" {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "Account ID and status are required"}},
			IsError: true,
		}, nil, nil
	}

	validStatuses := map[string]bool{"active": true, "frozen": true, "closed": true, "under-review": true}
	if !validStatuses[params.Status] {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "Invalid status. Must be: active, frozen, closed, or under-review"}},
			IsError: true,
		}, nil, nil
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	account, exists := store.accounts[params.ID]
	if !exists {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Account not found: %s", params.ID)}},
			IsError: true,
		}, nil, nil
	}

	oldStatus := account.Status
	account.Status = params.Status
	store.accounts[params.ID] = account

	// Add audit log entry
	store.auditLog = append(store.auditLog, AuditEntry{
		ID:        uuid.New().String(),
		Timestamp: time.Now().Format(time.RFC3339),
		Action:    "account.status_change",
		User:      getSubjectFromRequest(req),
		Resource:  account.AccountNumber,
		Details:   fmt.Sprintf("Status changed from %s to %s", oldStatus, params.Status),
	})

	logger.Info("account status updated",
		zap.String("id", account.ID),
		zap.String("account_number", account.AccountNumber),
		zap.String("old_status", oldStatus),
		zap.String("new_status", params.Status),
	)

	data, _ := json.MarshalIndent(map[string]any{
		"account":    account,
		"old_status": oldStatus,
		"new_status": params.Status,
	}, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}, account, nil
}

func getCustomerPII(ctx context.Context, req *mcp.CallToolRequest, params *GetCustomerPIIParams) (*mcp.CallToolResult, any, error) {
	logToolCall(req, "getCustomerPII")

	if params.AccountID == "" {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "Account ID is required"}},
			IsError: true,
		}, nil, nil
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	pii, exists := store.customerPII[params.AccountID]
	if !exists {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("No PII records found for account: %s", params.AccountID)}},
			IsError: true,
		}, nil, nil
	}

	logger.Warn("SENSITIVE DATA ACCESSED: Customer PII retrieved",
		zap.String("account_id", params.AccountID),
	)

	data, _ := json.MarshalIndent(pii, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}, pii, nil
}

func getAuditLog(ctx context.Context, req *mcp.CallToolRequest, params *GetAuditLogParams) (*mcp.CallToolResult, any, error) {
	logToolCall(req, "getAuditLog")

	store.mu.RLock()
	defer store.mu.RUnlock()

	var entries []AuditEntry
	for _, e := range store.auditLog {
		if params.Resource != "" && e.Resource != params.Resource {
			continue
		}
		if params.Action != "" && e.Action != params.Action {
			continue
		}
		entries = append(entries, e)
	}

	result := map[string]any{
		"audit_entries": entries,
		"total":         len(entries),
	}

	logger.Warn("SENSITIVE DATA ACCESSED: Audit log retrieved",
		zap.Int("entries_returned", len(entries)),
	)

	data, _ := json.MarshalIndent(result, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}, result, nil
}

// Helper functions

func getSubjectFromRequest(req *mcp.CallToolRequest) string {
	if req == nil || req.Extra == nil || req.Extra.TokenInfo == nil {
		return "anonymous"
	}
	if subject, ok := req.Extra.TokenInfo.Extra["subject"].(string); ok {
		return subject
	}
	return "unknown"
}

func logToolCall(req *mcp.CallToolRequest, toolName string) {
	if noAuth {
		logger.Info("tool called (UNPROTECTED)",
			zap.String("tool", toolName),
			zap.String("auth_mode", "none"),
		)
		return
	}

	if req == nil || req.Extra == nil || req.Extra.TokenInfo == nil {
		logger.Info("tool called", zap.String("tool", toolName))
		return
	}

	tokenInfo := req.Extra.TokenInfo
	fields := []zap.Field{zap.String("tool", toolName)}
	if subject, ok := tokenInfo.Extra["subject"].(string); ok {
		fields = append(fields, zap.String("subject", subject))
	}
	if actors, ok := tokenInfo.Extra["actors"].([]string); ok && len(actors) > 0 {
		fields = append(fields, zap.Strings("actors", actors))
	}
	if scopes, ok := tokenInfo.Extra["scopes"].([]string); ok && len(scopes) > 0 {
		fields = append(fields, zap.Strings("scopes", scopes))
	}
	if issuer, ok := tokenInfo.Extra["issuer"].(string); ok {
		fields = append(fields, zap.String("issuer", issuer))
	}
	if issuedAt, ok := tokenInfo.Extra["issued_at"].(string); ok {
		fields = append(fields, zap.String("issued_at", issuedAt))
	}
	if audience, ok := tokenInfo.Extra["audience"].([]string); ok && len(audience) > 0 {
		fields = append(fields, zap.Strings("audience", audience))
	}
	if ttl, ok := tokenInfo.Extra["ttl_seconds"].(int64); ok {
		fields = append(fields, zap.Int64("ttl_seconds", ttl))
	}
	logger.Info("tool called", fields...)
}

// checkScopeFromRequest verifies that the token has the required scope
func checkScopeFromRequest(req *mcp.CallToolRequest, requiredScope string) error {
	// In no-auth mode, all scopes are allowed
	if noAuth {
		return nil
	}

	if req == nil || req.Extra == nil || req.Extra.TokenInfo == nil {
		logger.Warn("checkScope: no token info in request")
		return fmt.Errorf("no token info in request")
	}

	tokenInfo := req.Extra.TokenInfo

	scopes, ok := tokenInfo.Extra["scopes"].([]string)
	if !ok {
		// Try []any (common when JSON unmarshaling)
		if scopesAny, ok := tokenInfo.Extra["scopes"].([]any); ok {
			scopes = make([]string, 0, len(scopesAny))
			for _, s := range scopesAny {
				if str, ok := s.(string); ok {
					scopes = append(scopes, str)
				}
			}
		} else {
			scopes = nil
		}
	}

	for _, s := range scopes {
		if s == requiredScope {
			return nil
		}
	}

	logger.Warn("insufficient scope",
		zap.String("required_scope", requiredScope),
		zap.Strings("token_scopes", scopes),
	)
	return fmt.Errorf("insufficient scope: requires '%s'", requiredScope)
}

// scopeError returns a CallToolResult for scope errors
func scopeError(scope string) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Forbidden: insufficient scope, requires '%s'", scope)}},
		IsError: true,
	}, nil, nil
}

// withScope wraps a tool handler with scope enforcement
func withScope[T any](scope string, handler func(context.Context, *mcp.CallToolRequest, *T) (*mcp.CallToolResult, any, error)) func(context.Context, *mcp.CallToolRequest, *T) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, params *T) (*mcp.CallToolResult, any, error) {
		if err := checkScopeFromRequest(req, scope); err != nil {
			return scopeError(scope)
		}
		return handler(ctx, req, params)
	}
}

// Seed data loading

type SeedData struct {
	Accounts    []SeedAccount     `json:"accounts"`
	Transactions []SeedTransaction `json:"transactions"`
	CustomerPII []SeedCustomerPII `json:"customer_pii"`
	AuditLog    []SeedAuditEntry  `json:"audit_log"`
}

type SeedAccount struct {
	AccountNumber string  `json:"account_number"`
	HolderName    string  `json:"holder_name"`
	HolderEmail   string  `json:"holder_email"`
	Type          string  `json:"type"`
	Balance       float64 `json:"balance"`
	Status        string  `json:"status"`
}

type SeedTransaction struct {
	AccountNumber string  `json:"account_number"`
	Type          string  `json:"type"`
	Amount        float64 `json:"amount"`
	Description   string  `json:"description"`
	Counterparty  string  `json:"counterparty"`
}

type SeedCustomerPII struct {
	AccountNumber string `json:"account_number"`
	SSN           string `json:"ssn"`
	DateOfBirth   string `json:"date_of_birth"`
	Phone         string `json:"phone"`
	Address       string `json:"address"`
}

type SeedAuditEntry struct {
	Action   string `json:"action"`
	User     string `json:"user"`
	Resource string `json:"resource"`
	Details  string `json:"details"`
}

func loadSeedFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read seed file: %w", err)
	}

	var seedData SeedData
	if err := json.Unmarshal(data, &seedData); err != nil {
		return fmt.Errorf("failed to parse seed file: %w", err)
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	now := time.Now()

	// Build account number to ID mapping
	accountNumberToID := make(map[string]string)

	// Load accounts
	for _, sa := range seedData.Accounts {
		id := uuid.New().String()
		accountNumberToID[sa.AccountNumber] = id
		store.accounts[id] = Account{
			ID:            id,
			AccountNumber: sa.AccountNumber,
			HolderName:    sa.HolderName,
			HolderEmail:   sa.HolderEmail,
			Type:          sa.Type,
			Balance:       sa.Balance,
			Status:        sa.Status,
			CreatedAt:     now.Add(-30 * 24 * time.Hour).Format(time.RFC3339),
		}
	}

	// Load transactions (linked to account IDs)
	for _, st := range seedData.Transactions {
		accountID, ok := accountNumberToID[st.AccountNumber]
		if !ok {
			logger.Warn("transaction references unknown account", zap.String("account_number", st.AccountNumber))
			continue
		}
		txn := Transaction{
			ID:           uuid.New().String(),
			AccountID:    accountID,
			Type:         st.Type,
			Amount:       st.Amount,
			Description:  st.Description,
			Counterparty: st.Counterparty,
			Timestamp:    now.Add(-time.Duration(len(store.transactions[accountID])+1) * 24 * time.Hour).Format(time.RFC3339),
		}
		store.transactions[accountID] = append(store.transactions[accountID], txn)
	}

	// Load customer PII (linked to account IDs)
	for _, sp := range seedData.CustomerPII {
		accountID, ok := accountNumberToID[sp.AccountNumber]
		if !ok {
			logger.Warn("PII references unknown account", zap.String("account_number", sp.AccountNumber))
			continue
		}
		store.customerPII[accountID] = CustomerPII{
			AccountID:   accountID,
			SSN:         sp.SSN,
			DateOfBirth: sp.DateOfBirth,
			Phone:       sp.Phone,
			Address:     sp.Address,
		}
	}

	// Load audit log entries
	for _, se := range seedData.AuditLog {
		store.auditLog = append(store.auditLog, AuditEntry{
			ID:        uuid.New().String(),
			Timestamp: now.Add(-time.Duration(len(store.auditLog)+1) * time.Hour).Format(time.RFC3339),
			Action:    se.Action,
			User:      se.User,
			Resource:  se.Resource,
			Details:   se.Details,
		})
	}

	logger.Info("loaded seed data",
		zap.Int("accounts", len(seedData.Accounts)),
		zap.Int("transactions", len(seedData.Transactions)),
		zap.Int("customer_pii", len(seedData.CustomerPII)),
		zap.Int("audit_entries", len(seedData.AuditLog)),
	)
	return nil
}

func loadDefaultData() {
	store.mu.Lock()
	defer store.mu.Unlock()

	now := time.Now()

	// Default account
	id := uuid.New().String()
	store.accounts[id] = Account{
		ID:            id,
		AccountNumber: "CHK-000001",
		HolderName:    "Demo User",
		HolderEmail:   "demo@orchestrator.lab",
		Type:          "checking",
		Balance:       10000.00,
		Status:        "active",
		CreatedAt:     now.Format(time.RFC3339),
	}

	store.transactions[id] = []Transaction{
		{
			ID:           uuid.New().String(),
			AccountID:    id,
			Type:         "credit",
			Amount:       5000.00,
			Description:  "Initial deposit",
			Counterparty: "Cash",
			Timestamp:    now.Format(time.RFC3339),
		},
	}

	logger.Info("loaded default data", zap.Int("accounts", 1))
}
