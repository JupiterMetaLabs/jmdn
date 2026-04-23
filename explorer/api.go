package explorer

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/JupiterMetaLabs/ion"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog/log"

	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/config/settings"
	"gossipnode/config/version"
	"gossipnode/pkg/gatekeeper"

	"go.opentelemetry.io/otel/attribute"
)

const (
	LOG_DIR  = "logs"
	LOG_FILE = ""
	TOPIC    = "explorer"
)

// JWT token expiration time (24 hours)
const JWT_EXPIRATION = 1 * time.Hour

// ImmuDBServer represents the ImmuDB API server
type ImmuDBServer struct {
	defaultdb  config.PooledConnection
	accountsdb config.PooledConnection
	router     *gin.Engine
	gatekeeper *gatekeeper.GinMiddleware
	tlsLoader  *gatekeeper.TLSLoader
}

// NewImmuDBServer creates a new ImmuDB API server
func NewImmuDBServer() (*ImmuDBServer, error) {
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create a temporary logger for tracing (will be replaced by actual connection logger)
	// For now, we'll use the defaultdb logger after connection
	startTime := time.Now().UTC()

	// Create ImmuDB client
	defaultdb, err := DB_OPs.GetMainDBConnectionandPutBack(context.Background())
	if err != nil {
		return nil, err
	}

	accountsdb, err := DB_OPs.GetAccountConnectionandPutBack(context.Background())
	if err != nil {
		return nil, err
	}

	// Create span for server initialization
	spanCtx, span := defaultdb.Client.Logger.Tracer("ExplorerAPI").Start(loggerCtx, "ExplorerAPI.NewImmuDBServer")
	defer span.End()

	span.SetAttributes(
		attribute.String("database", config.DBName),
		attribute.String("accounts_database", config.AccountsDBName),
	)

	// Create gin router with default middleware
	router := gin.Default()

	// Initialize Security Components
	secCfg := &settings.Get().Security
	logger := defaultdb.Client.Logger

	// Rate Limiter
	rl, err := gatekeeper.NewRateLimiter(secCfg, secCfg.IPCacheSize)
	if err != nil {
		return nil, err
	}
	// Middleware
	middleware := gatekeeper.NewGinMiddleware(secCfg, rl, logger)

	// TLS Loader
	tlsLoader := gatekeeper.NewTLSLoader(secCfg, logger)

	// Create server instance
	server := &ImmuDBServer{
		defaultdb:  *defaultdb,
		accountsdb: *accountsdb,
		router:     router,
		gatekeeper: middleware,
		tlsLoader:  tlsLoader,
	}

	// Set up routes
	server.setupRoutes()

	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration), attribute.String("status", "success"))

	defaultdb.Client.Logger.Info(spanCtx, "ImmuDB API server created successfully",
		ion.String("database", config.DBName),
		ion.String("accounts_database", config.AccountsDBName),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ExplorerAPI.NewImmuDBServer"))

	return server, nil
}

func CloseImmuDBServer(server *ImmuDBServer) {
	DB_OPs.PutMainDBConnection(&server.defaultdb)
	DB_OPs.PutAccountsConnection(&server.accountsdb)
	server.Close()
}

// setupRoutes configures the API routes
func (s *ImmuDBServer) setupRoutes() {
	gin.DefaultWriter = os.Stdout
	gin.DefaultErrorWriter = os.Stderr

	// Add CORS middleware
	s.router.Use(cors())

	// Public endpoint for token generation (using API key)
	s.router.POST("/api/auth/token", s.generateToken)

	// -------------------------------------------------------------------------
	// API Versioning Strategy
	// -------------------------------------------------------------------------
	// The API is transitioning to a versioned structure (e.g., /api/v1/).
	// Existing unversioned routes (legacy) are maintained for backward compatibility.
	// New endpoints should be added to the appropriate version group.
	//
	// Future: To add V2, create a new group: v2 := s.router.Group("/api/v2")
	//
	// The /api/v1 group serves as the foundation for modern, versioned endpoints.
	// It supports both:
	// 1. Open/Public endpoints (no authentication).
	// 2. Protected/Secured endpoints (JWT authentication).

	v1 := s.router.Group("/api/v1")
	{
		// [A] Open Endpoints
		// Place routes here that do not require authentication.
		// -----------------------------------------------------
		// Example: v1.GET("/public", s.getPublicData)
		v1.GET("/node/version", s.getVersion)

		// [B] Protected Endpoints
		// Place routes here that require valid JWT Authentication.
		// A middleware wrapper is applied to this subgroup.
		// -----------------------------------------------------
		protected := v1.Group("/")
		protected.Use(s.gatekeeper.Middleware(settings.ServiceExplorerAPI))
		{
			// Example: protected.GET("/users", s.getUsers)
			// protected.GET("/node/version", s.getVersion)
		}
	}
	// -------------------------------------------------------------------------

	// Root health check — lightweight, unauthenticated
	s.router.GET("/", s.rootHealth)

	// API routes - protected with JWT
	api := s.router.Group("/api/block")
	api.Use(s.gatekeeper.Middleware(settings.ServiceExplorerAPI))
	{
		// Get specific block by ID
		api.GET("/id/:id", s.getBlock)

		// Get block by number
		api.GET("/number/:number", s.getBlockByNumber)

		// List all blocks by pagination
		api.GET("/all", s.listBlocks)

		// Get transaction by hash
		api.GET("/transactions/:hash", s.getTransaction)

		// List all transactions in a block
		api.GET("/transactions/block/:number", s.listTransactions_inBlock)

		// Return the Missing blocks - take the current block as input and return the missing blocks from current to latest
		api.GET("/missing/:number", s.getMissingBlocks)

		// Health check
		api.GET("/health", s.healthCheck)

		// Get Latest Blocks by count using pagination - max 100 blocks at a time
		api.GET("/latest", s.getLatestBlock)

		// Get all the transactions based on the pagination
		// api.GET("/transactions/all", s.listTransactions)
	}

	// Add a new group for Ethereum JSON-RPC
	// s.router.POST("/rpc", s.handleJsonRpc)

	// API routes for DID - protected with JWT
	did := s.router.Group("/api/did")
	did.Use(s.gatekeeper.Middleware(settings.ServiceExplorerAPI))
	{
		// Get all dids by pagination
		did.GET("/all/", s.listDIDs)

		// Get DID details by one or more DID strings
		did.GET("/details", s.getDIDDetails)

		// Get DID details by giving addr
		did.GET("/details/pubaddr", s.getDIDDetailsFromAddr)

		// Health check
		did.GET("/health", s.didHealthCheck)
	}

	// stats api - protected with JWT
	stats := s.router.Group("/api/stats")
	stats.Use(s.gatekeeper.Middleware(settings.ServiceExplorerAPI))
	{
		stats.GET("/block/latest", s.getLatestBlockStats)

		stats.GET("/", s.getStats)
	}

	// Address and balance endpoints - protected with JWT
	addresses := s.router.Group("/api/addresses")
	addresses.Use(s.gatekeeper.Middleware(settings.ServiceExplorerAPI))
	{
		// Get transactions for a specific address
		addresses.GET("/transactions/:address", s.getAddressTransactions)
	}

	// transactions endpoint - protected with JWT
	transactions := s.router.Group("/api/transactions")
	transactions.Use(s.gatekeeper.Middleware(settings.ServiceExplorerAPI))
	{
		// Get all the transactions based on the pagination
		transactions.GET("/all", s.listTransactions)

		// Get the transactions based on the block numbers from last
		transactions.GET("/blocks/all", s.listTransactions_fromLastBlock)
	}

	// Websockets to stream realtime blocks - protected with JWT
	sockets := s.router.Group("/api/sockets")
	sockets.Use(s.gatekeeper.Middleware(settings.ServiceExplorerAPI))
	{
		sockets.GET("/blocks", s.streamBlocks)
	}

}

// Start runs the HTTP server until it exits.
// Prefer StartWithContext in long-running processes so shutdown can be graceful.
func (s *ImmuDBServer) Start(addr string) error {
	return s.StartWithContext(context.Background(), addr)
}

// StartWithContext runs the HTTP server and shuts it down when ctx is cancelled.
func (s *ImmuDBServer) StartWithContext(ctx context.Context, addr string) error {
	// Create span for server start
	spanCtx, span := s.defaultdb.Client.Logger.Tracer("ExplorerAPI").Start(ctx, "ExplorerAPI.StartWithContext")
	defer span.End()

	startTime := time.Now().UTC()

	// Ensure we bind to all interfaces for production deployments
	// If addr doesn't specify a host, bind to 0.0.0.0 explicitly
	bindAddr := addr
	if len(addr) > 0 && addr[0] == ':' {
		// Address is in format :port, ensure we bind to all interfaces
		bindAddr = "0.0.0.0" + addr
	} else if addr == "" {
		// Default to binding to all interfaces on a default port
		bindAddr = "0.0.0.0:8090"
	}

	span.SetAttributes(
		attribute.String("bind_address", bindAddr),
	)

	s.defaultdb.Client.Logger.Info(spanCtx, "Starting ImmuDB API server",
		ion.String("bind_address", bindAddr),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ExplorerAPI.StartWithContext"))

	log.Info().Str("addr", bindAddr).Msg("Starting ImmuDB API server")

	// Use http.Server for explicit control over binding
	srv := &http.Server{
		Addr:           bindAddr,
		Handler:        s.router,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 20, // 1 MB
	}

	config, err := s.tlsLoader.LoadServerTLS(settings.ServiceExplorerAPI)
	if err != nil {
		// FAIL HARD: If TLS is enabled in policy but fails to load, we must not start insecurely.
		s.defaultdb.Client.Logger.Error(spanCtx, "Failed to load TLS config for Explorer API", err)
		return fmt.Errorf("failed to load TLS config for Explorer API: %w", err)
	}

	// If TLS config is present, use it
	if config != nil {
		srv.TLSConfig = config
		s.defaultdb.Client.Logger.Info(spanCtx, "TLS Enabled for Explorer API")
	} else {
		s.defaultdb.Client.Logger.Warn(spanCtx, "TLS Disabled for Explorer API (Configuration)", ion.String("service", settings.ServiceExplorerAPI))
	}

	errCh := make(chan error, 1)
	go func() {
		if config != nil {
			errCh <- srv.ListenAndServeTLS("", "")
		} else {
			errCh <- srv.ListenAndServe()
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		span.SetAttributes(attribute.String("status", "shutdown"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			span.SetAttributes(attribute.String("status", "success"))
			duration := time.Since(startTime).Seconds()
			span.SetAttributes(attribute.Float64("duration", duration))
			return nil
		}
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "error"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		return err
	}
}

// Close cleans up resources
func (s *ImmuDBServer) Close() {
	if s.defaultdb.Client != nil {
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		spanCtx, span := s.defaultdb.Client.Logger.Tracer("ExplorerAPI").Start(loggerCtx, "ExplorerAPI.Close")
		defer span.End()

		span.SetAttributes(
			attribute.String("database", config.DBName),
			attribute.String("function", "ExplorerAPI.Close"),
		)

		s.defaultdb.Client.Logger.Info(spanCtx, "Closing the MainDB Connection in the API.go File",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "ExplorerAPI.Close"))
		DB_OPs.PutAccountsConnection(&s.defaultdb)
	}
	if s.accountsdb.Client != nil {
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		spanCtx, span := s.accountsdb.Client.Logger.Tracer("ExplorerAPI").Start(loggerCtx, "ExplorerAPI.Close")
		defer span.End()

		span.SetAttributes(
			attribute.String("database", config.AccountsDBName),
			attribute.String("function", "ExplorerAPI.Close"),
		)

		s.accountsdb.Client.Logger.Info(spanCtx, "Closing the AccountsDB Connection in the API.go File",
			ion.String("database", config.AccountsDBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "ExplorerAPI.Close"))
		DB_OPs.PutMainDBConnection(&s.accountsdb)
	}
}

// CORS middleware
func cors() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Origin, Content-Type, Content-Length, Authorization")
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Max-Age", "86400") // 24 hours

		// Handle WebSocket upgrade
		if c.GetHeader("Upgrade") == "websocket" {
			c.Writer.Header().Set("Connection", "Upgrade")
			c.Writer.Header().Set("Upgrade", "websocket")
		}

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// generateToken creates a JWT token when provided with a valid API key
func (s *ImmuDBServer) generateToken(c *gin.Context) {
	// Create span for token generation
	spanCtx, span := s.defaultdb.Client.Logger.Tracer("ExplorerAPI").Start(c.Request.Context(), "ExplorerAPI.generateToken")
	defer span.End()

	startTime := time.Now().UTC()

	var req struct {
		APIKey string `json:"api_key" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "bad_request"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		c.JSON(http.StatusBadRequest, gin.H{"error": "API key required"})
		return
	}

	// Use cached token resolution (GetResolvedToken) + Constant time compare
	validKey := settings.Get().Security.GetResolvedToken("EXPLORER_API_KEY")
	if subtle.ConstantTimeCompare([]byte(req.APIKey), []byte(validKey)) != 1 {
		span.SetAttributes(attribute.String("status", "unauthorized"), attribute.String("reason", "invalid_api_key"))
		span.RecordError(errors.New("invalid API key"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid API key"})
		return
	}

	now := time.Now()
	claims := jwt.MapClaims{
		"api_key": req.APIKey,
		"iat":     now.Unix(),
		"exp":     now.Add(JWT_EXPIRATION).Unix(),
		"type":    "explorer_api",
	}

	span.SetAttributes(
		attribute.Int64("expires_at", now.Add(JWT_EXPIRATION).Unix()),
		attribute.Int("expires_in_seconds", int(JWT_EXPIRATION.Seconds())),
	)

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	// Use cached token resolution
	jwtSecret := settings.Get().Security.GetResolvedToken("JWT_SECRET")
	tokenString, err := token.SignedString([]byte(jwtSecret))
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.String("status", "error"))
		duration := time.Since(startTime).Seconds()
		span.SetAttributes(attribute.Float64("duration", duration))
		s.defaultdb.Client.Logger.Error(spanCtx, "Failed to generate JWT token",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "ExplorerAPI.generateToken"))
		log.Error().Err(err).Msg("Failed to generate JWT token")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate token"})
		return
	}

	span.SetAttributes(attribute.String("status", "success"))
	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration))

	s.defaultdb.Client.Logger.Info(spanCtx, "JWT token generated successfully",
		ion.Int("expires_in_seconds", int(JWT_EXPIRATION.Seconds())),
		ion.Int64("expires_at", now.Add(JWT_EXPIRATION).Unix()),
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ExplorerAPI.generateToken"))

	c.JSON(http.StatusOK, gin.H{
		"token":      tokenString,
		"type":       "Bearer",
		"expires_in": int(JWT_EXPIRATION.Seconds()),
		"expires_at": now.Add(JWT_EXPIRATION).Unix(),
	})
}

// getVersion returns the current version information
func (s *ImmuDBServer) getVersion(c *gin.Context) {
	// Create span for version endpoint
	spanCtx, span := s.defaultdb.Client.Logger.Tracer("ExplorerAPI").Start(c.Request.Context(), "ExplorerAPI.getVersion")
	defer span.End()

	startTime := time.Now().UTC()
	span.SetAttributes(attribute.String("status", "success"))

	versionInfo := version.GetVersionInfo()
	duration := time.Since(startTime).Seconds()
	span.SetAttributes(attribute.Float64("duration", duration))

	s.defaultdb.Client.Logger.Info(spanCtx, "Version information retrieved",
		ion.Float64("duration", duration),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "ExplorerAPI.getVersion"))

	c.JSON(http.StatusOK, versionInfo)
}

// rootHealth is a lightweight, unauthenticated health endpoint served at GET /.
// It intentionally exposes minimal information — just enough to confirm the node is alive.
func (s *ImmuDBServer) rootHealth(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"service": "jmdn",
		"version": version.GetVersionInfo(),
	})
}
