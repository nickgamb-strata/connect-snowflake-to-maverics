package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-contrib/zap"
	"github.com/gin-gonic/gin"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
	"github.com/urfave/cli/v2"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Employee represents an employee record
type Employee struct {
	ID         string    `json:"id"`
	EmployeeID string    `json:"employee_id,omitempty"`
	FirstName  string    `json:"first_name"`
	LastName   string    `json:"last_name"`
	Email      string    `json:"email"`
	Department string    `json:"department"`
	Title      string    `json:"title"`
	ManagerID  *string   `json:"manager_id,omitempty"`
	Location   string    `json:"location,omitempty"`
	StartDate  string    `json:"start_date,omitempty"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// EmployeeCreate represents the request body for creating an employee
type EmployeeCreate struct {
	EmployeeID string  `json:"employee_id"`
	FirstName  string  `json:"first_name" binding:"required"`
	LastName   string  `json:"last_name" binding:"required"`
	Email      string  `json:"email" binding:"required,email"`
	Department string  `json:"department" binding:"required"`
	Title      string  `json:"title" binding:"required"`
	ManagerID  *string `json:"manager_id"`
	Location   string  `json:"location"`
	StartDate  string  `json:"start_date"`
}

// EmployeeUpdate represents the request body for updating an employee
type EmployeeUpdate struct {
	FirstName  *string `json:"first_name"`
	LastName   *string `json:"last_name"`
	Email      *string `json:"email"`
	Department *string `json:"department"`
	Title      *string `json:"title"`
	ManagerID  *string `json:"manager_id"`
	Location   *string `json:"location"`
	Status     *string `json:"status"`
}

// EmployeeList represents a paginated list of employees
type EmployeeList struct {
	Employees []Employee `json:"employees"`
	Total     int        `json:"total"`
	Page      int        `json:"page"`
	PageSize  int        `json:"page_size"`
}

// ErrorResponse represents an API error
type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// In-memory store
type Store struct {
	mu        sync.RWMutex
	employees map[string]Employee
}

var store = &Store{
	employees: make(map[string]Employee),
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

func main() {
	app := &cli.App{
		Name:  "employee-directory",
		Usage: "Enterprise employee directory API",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "port",
				Value:   "8443",
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
				Value:   "https://employee-directory.orchestrator.lab/",
				Usage:   "Expected JWT audience",
				EnvVars: []string{"JWT_AUDIENCE"},
			},
			&cli.StringFlag{
				Name:    "jwks-endpoint",
				Usage:   "JWKS endpoint URL (defaults to {jwt-issuer}/jwks)",
				EnvVars: []string{"JWKS_ENDPOINT"},
			},
			&cli.StringFlag{
				Name:     "seed-file",
				Usage:    "Path to JSON file containing seed employee data",
				EnvVars:  []string{"SEED_FILE"},
				Required: true,
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
	issuer := c.String("jwt-issuer")
	audience := c.String("jwt-audience")
	jwksEndpoint := c.String("jwks-endpoint")
	seedFile := c.String("seed-file")

	// Initialize zap logger with production config but human-readable time
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
		jwksEndpoint = issuer + "/jwks"
	}

	jwksCache.endpoint = jwksEndpoint

	// Load seed data from file
	if err := loadSeedFile(seedFile); err != nil {
		return fmt.Errorf("failed to load seed file: %w", err)
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()

	// Use zap for gin logging
	r.Use(ginzap.Ginzap(logger, time.RFC3339, true))
	r.Use(ginzap.RecoveryWithZap(logger, true))

	// Health check (no auth required)
	r.GET("/health", func(ctx *gin.Context) {
		ctx.JSON(http.StatusOK, gin.H{"status": "healthy"})
	})

	// API routes with JWT auth and scope enforcement
	api := r.Group("/api/v1")
	api.Use(jwtAuthMiddleware(issuer, audience))
	{
		// Employee endpoints with AWS IAM-style scopes
		api.GET("/employees", requireScope("employee:List"), listEmployees)
		api.POST("/employees", requireScope("employee:Create"), createEmployee)
		api.GET("/employees/:id", requireScope("employee:Get"), getEmployee)
		api.PUT("/employees/:id", requireScope("employee:Update"), updateEmployee)
		api.DELETE("/employees/:id", requireScope("employee:Deactivate"), deactivateEmployee)
		api.GET("/employees/:id/reports", requireScope("employee:List"), getDirectReports)

		// Department endpoints
		api.GET("/departments", requireScope("department:List"), listDepartments)
	}

	logger.Info("starting Employee Directory API",
		zap.String("port", port),
		zap.String("jwt_issuer", issuer),
		zap.String("jwt_audience", audience),
		zap.String("jwks_endpoint", jwksEndpoint),
	)

	if tlsCert != "" && tlsKey != "" {
		logger.Info("TLS enabled", zap.String("cert", tlsCert))
		return r.RunTLS(":"+port, tlsCert, tlsKey)
	}

	logger.Info("TLS disabled, running in HTTP mode")
	return r.Run(":" + port)
}

// SeedData represents the structure of the seed JSON file
type SeedData struct {
	Employees []SeedEmployee `json:"employees"`
}

// SeedEmployee represents an employee in the seed file
type SeedEmployee struct {
	EmployeeID string  `json:"employee_id"`
	FirstName  string  `json:"first_name"`
	LastName   string  `json:"last_name"`
	Email      string  `json:"email"`
	Department string  `json:"department"`
	Title      string  `json:"title"`
	ManagerID  *string `json:"manager_id,omitempty"`
	Location   string  `json:"location,omitempty"`
	StartDate  string  `json:"start_date,omitempty"`
	Status     string  `json:"status"`
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

	// Build a map of employeeId -> UUID for resolving manager references
	employeeIDToUUID := make(map[string]string)
	employees := make([]Employee, 0, len(seedData.Employees))

	now := time.Now()

	// First pass: create employees with UUIDs
	for _, se := range seedData.Employees {
		id := uuid.New().String()
		employeeIDToUUID[se.EmployeeID] = id

		employees = append(employees, Employee{
			ID:         id,
			EmployeeID: se.EmployeeID,
			FirstName:  se.FirstName,
			LastName:   se.LastName,
			Email:      se.Email,
			Department: se.Department,
			Title:      se.Title,
			Location:   se.Location,
			StartDate:  se.StartDate,
			Status:     se.Status,
			CreatedAt:  now,
			UpdatedAt:  now,
		})
	}

	// Second pass: resolve manager references
	for i, se := range seedData.Employees {
		if se.ManagerID != nil {
			if managerUUID, ok := employeeIDToUUID[*se.ManagerID]; ok {
				employees[i].ManagerID = &managerUUID
			} else {
				logger.Warn("manager not found for employee",
					zap.String("manager_id", *se.ManagerID),
					zap.String("employee_id", se.EmployeeID),
				)
			}
		}
	}

	store.mu.Lock()
	for _, emp := range employees {
		store.employees[emp.ID] = emp
	}
	store.mu.Unlock()

	logger.Info("loaded employees from seed file",
		zap.Int("count", len(employees)),
		zap.String("path", path),
	)
	return nil
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

	// Double-check after acquiring write lock
	if jwksCache.jwks != nil && time.Since(jwksCache.lastFetch) < 5*time.Minute {
		return jwksCache.jwks, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksCache.endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create JWKS request: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
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

func jwtAuthMiddleware(issuer, audience string) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, ErrorResponse{
				Code:    "UNAUTHORIZED",
				Message: "Missing Authorization header",
			})
			c.Abort()
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			c.JSON(http.StatusUnauthorized, ErrorResponse{
				Code:    "UNAUTHORIZED",
				Message: "Invalid Authorization header format",
			})
			c.Abort()
			return
		}

		tokenString := parts[1]

		// Parse the token to get the header (we need the kid)
		parsedJWT, err := jwt.ParseSigned(tokenString, []jose.SignatureAlgorithm{jose.RS256, jose.ES256})
		if err != nil {
			c.JSON(http.StatusUnauthorized, ErrorResponse{
				Code:    "UNAUTHORIZED",
				Message: "Invalid token format",
			})
			c.Abort()
			return
		}

		// Fetch JWKS
		jwks, err := fetchJWKS(c.Request.Context())
		if err != nil {
			logger.Error("failed to fetch JWKS", zap.Error(err))
			c.JSON(http.StatusInternalServerError, ErrorResponse{
				Code:    "INTERNAL_ERROR",
				Message: "Failed to validate token",
			})
			c.Abort()
			return
		}

		// Find the key
		var key *jose.JSONWebKey
		if len(parsedJWT.Headers) > 0 && parsedJWT.Headers[0].KeyID != "" {
			keys := jwks.Key(parsedJWT.Headers[0].KeyID)
			if len(keys) > 0 {
				key = &keys[0]
			}
		}

		if key == nil {
			c.JSON(http.StatusUnauthorized, ErrorResponse{
				Code:    "UNAUTHORIZED",
				Message: "Token signing key not found",
			})
			c.Abort()
			return
		}

		// Validate the token - use custom claims struct to capture act claim
		var claims CustomClaims
		if err := parsedJWT.Claims(key.Key, &claims); err != nil {
			c.JSON(http.StatusUnauthorized, ErrorResponse{
				Code:    "UNAUTHORIZED",
				Message: "Invalid token signature",
			})
			c.Abort()
			return
		}

		// Validate standard claims
		expected := jwt.Expected{
			Issuer:      issuer,
			AnyAudience: []string{audience},
			Time:        time.Now(),
		}

		if err := claims.Claims.Validate(expected); err != nil {
			logger.Warn("token validation failed", zap.Error(err))
			c.JSON(http.StatusUnauthorized, ErrorResponse{
				Code:    "UNAUTHORIZED",
				Message: "Token validation failed: " + err.Error(),
			})
			c.Abort()
			return
		}

		// Log the subject and actor chain
		logTokenIdentity(c.Request.Method, c.Request.URL.Path, &claims)

		// Store claims in context for later use
		c.Set("claims", claims)
		c.Next()
	}
}

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

// logTokenIdentity logs the subject, scopes, full actor chain, and token metadata
func logTokenIdentity(method, path string, claims *CustomClaims) {
	// Determine subject (prefer email, then name, then sub claim)
	subject := claims.Subject
	if claims.Email != "" {
		subject = claims.Email
	} else if claims.Name != "" {
		subject = claims.Name
	}
	if subject == "" {
		subject = "(none)"
	}

	// Build actor chain as slice for structured logging
	actors := buildActorSlice(claims.Act)

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

	fields := []zap.Field{
		zap.String("method", method),
		zap.String("path", path),
		zap.String("subject", subject),
	}

	if len(actors) > 0 {
		fields = append(fields, zap.Strings("actors", actors))
	}

	// Log scopes if present
	scopes := claims.GetScopes()
	if len(scopes) > 0 {
		fields = append(fields, zap.Strings("scopes", scopes))
	}

	// Add token metadata
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

	logger.Info("API request", fields...)
}

// requireScope creates a middleware that enforces a specific scope
func requireScope(scope string) gin.HandlerFunc {
	return func(c *gin.Context) {
		claimsVal, exists := c.Get("claims")
		if !exists {
			c.JSON(http.StatusUnauthorized, ErrorResponse{
				Code:    "UNAUTHORIZED",
				Message: "No claims found in context",
			})
			c.Abort()
			return
		}

		claims, ok := claimsVal.(CustomClaims)
		if !ok {
			c.JSON(http.StatusInternalServerError, ErrorResponse{
				Code:    "INTERNAL_ERROR",
				Message: "Invalid claims type",
			})
			c.Abort()
			return
		}

		if !claims.HasScope(scope) {
			logger.Warn("insufficient scope",
				zap.String("required_scope", scope),
				zap.Strings("token_scopes", claims.GetScopes()),
				zap.String("subject", claims.Subject),
			)
			c.JSON(http.StatusForbidden, ErrorResponse{
				Code:    "FORBIDDEN",
				Message: fmt.Sprintf("Insufficient scope: requires '%s'", scope),
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

// buildActorSlice recursively builds a slice of actor IDs from the actor chain
func buildActorSlice(act *Actor) []string {
	if act == nil {
		return nil
	}

	// Identify this actor (prefer sub, fallback to client_id)
	actorID := act.Sub
	if actorID == "" {
		actorID = act.ClientID
	}
	if actorID == "" {
		actorID = "(unknown)"
	}

	actors := []string{actorID}

	// Recursively get nested actors
	if act.Act != nil {
		actors = append(actors, buildActorSlice(act.Act)...)
	}

	return actors
}

func listEmployees(c *gin.Context) {
	department := c.Query("department")
	status := c.Query("status")
	managerID := c.Query("manager_id")
	search := strings.ToLower(c.Query("search"))

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	var filtered []Employee
	for _, emp := range store.employees {
		if department != "" && emp.Department != department {
			continue
		}
		if status != "" && emp.Status != status {
			continue
		}
		if managerID != "" && (emp.ManagerID == nil || *emp.ManagerID != managerID) {
			continue
		}
		if search != "" {
			fullName := strings.ToLower(emp.FirstName + " " + emp.LastName)
			email := strings.ToLower(emp.Email)
			if !strings.Contains(fullName, search) && !strings.Contains(email, search) {
				continue
			}
		}
		filtered = append(filtered, emp)
	}

	total := len(filtered)
	start := (page - 1) * pageSize
	end := start + pageSize

	if start > total {
		start = total
	}
	if end > total {
		end = total
	}

	c.JSON(http.StatusOK, EmployeeList{
		Employees: filtered[start:end],
		Total:     total,
		Page:      page,
		PageSize:  pageSize,
	})
}

func createEmployee(c *gin.Context) {
	var req EmployeeCreate
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Code:    "INVALID_REQUEST",
			Message: err.Error(),
		})
		return
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	// Check for duplicate email
	for _, emp := range store.employees {
		if strings.EqualFold(emp.Email, req.Email) {
			c.JSON(http.StatusConflict, ErrorResponse{
				Code:    "DUPLICATE_EMAIL",
				Message: "An employee with this email already exists",
			})
			return
		}
	}

	now := time.Now()
	employee := Employee{
		ID:         uuid.New().String(),
		EmployeeID: req.EmployeeID,
		FirstName:  req.FirstName,
		LastName:   req.LastName,
		Email:      req.Email,
		Department: req.Department,
		Title:      req.Title,
		ManagerID:  req.ManagerID,
		Location:   req.Location,
		StartDate:  req.StartDate,
		Status:     "active",
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	store.employees[employee.ID] = employee
	c.JSON(http.StatusCreated, employee)
}

func getEmployee(c *gin.Context) {
	id := c.Param("id")

	store.mu.RLock()
	defer store.mu.RUnlock()

	emp, exists := store.employees[id]
	if !exists {
		c.JSON(http.StatusNotFound, ErrorResponse{
			Code:    "NOT_FOUND",
			Message: "Employee not found",
		})
		return
	}

	c.JSON(http.StatusOK, emp)
}

func updateEmployee(c *gin.Context) {
	id := c.Param("id")

	var req EmployeeUpdate
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Code:    "INVALID_REQUEST",
			Message: err.Error(),
		})
		return
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	emp, exists := store.employees[id]
	if !exists {
		c.JSON(http.StatusNotFound, ErrorResponse{
			Code:    "NOT_FOUND",
			Message: "Employee not found",
		})
		return
	}

	if req.FirstName != nil {
		emp.FirstName = *req.FirstName
	}
	if req.LastName != nil {
		emp.LastName = *req.LastName
	}
	if req.Email != nil {
		emp.Email = *req.Email
	}
	if req.Department != nil {
		emp.Department = *req.Department
	}
	if req.Title != nil {
		emp.Title = *req.Title
	}
	if req.ManagerID != nil {
		emp.ManagerID = req.ManagerID
	}
	if req.Location != nil {
		emp.Location = *req.Location
	}
	if req.Status != nil {
		emp.Status = *req.Status
	}

	emp.UpdatedAt = time.Now()
	store.employees[id] = emp

	c.JSON(http.StatusOK, emp)
}

func deactivateEmployee(c *gin.Context) {
	id := c.Param("id")

	store.mu.Lock()
	defer store.mu.Unlock()

	emp, exists := store.employees[id]
	if !exists {
		c.JSON(http.StatusNotFound, ErrorResponse{
			Code:    "NOT_FOUND",
			Message: "Employee not found",
		})
		return
	}

	emp.Status = "inactive"
	emp.UpdatedAt = time.Now()
	store.employees[id] = emp

	c.JSON(http.StatusOK, emp)
}

func getDirectReports(c *gin.Context) {
	id := c.Param("id")

	store.mu.RLock()
	defer store.mu.RUnlock()

	// Verify manager exists
	if _, exists := store.employees[id]; !exists {
		c.JSON(http.StatusNotFound, ErrorResponse{
			Code:    "NOT_FOUND",
			Message: "Employee not found",
		})
		return
	}

	var reports []Employee
	for _, emp := range store.employees {
		if emp.ManagerID != nil && *emp.ManagerID == id {
			reports = append(reports, emp)
		}
	}

	c.JSON(http.StatusOK, EmployeeList{
		Employees: reports,
		Total:     len(reports),
		Page:      1,
		PageSize:  len(reports),
	})
}

func listDepartments(c *gin.Context) {
	store.mu.RLock()
	defer store.mu.RUnlock()

	deptSet := make(map[string]bool)
	for _, emp := range store.employees {
		deptSet[emp.Department] = true
	}

	var departments []string
	for dept := range deptSet {
		departments = append(departments, dept)
	}

	c.JSON(http.StatusOK, gin.H{
		"departments": departments,
	})
}
