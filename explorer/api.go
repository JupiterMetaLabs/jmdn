package explorer

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog/log"
	"go.uber.org/zap"

	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/logging"
)

const (
	LOG_DIR  = "logs"
	LOG_FILE = LOG_DIR + "/explorer.log"
	TOPIC    = "explorer"
)

// JWT token expiration time (24 hours)
const JWT_EXPIRATION = 1 * time.Hour

// ImmuDBServer represents the ImmuDB API server
type ImmuDBServer struct {
	defaultdb      config.PooledConnection
	accountsdb     config.PooledConnection
	router         *gin.Engine
	enableExplorer bool
}

// NewImmuDBServer creates a new ImmuDB API server
func NewImmuDBServer(enableExplorer bool) (*ImmuDBServer, error) {
	// Create ImmuDB client
	defaultdb, err := DB_OPs.GetMainDBConnectionandPutBack(context.Background())
	if err != nil {
		return nil, err
	}

	accountsdb, err := DB_OPs.GetAccountConnectionandPutBack(context.Background())
	if err != nil {
		return nil, err
	}

	// Create gin router with default middleware
	router := gin.Default()

	// Create server instance
	server := &ImmuDBServer{
		defaultdb:      *defaultdb,
		accountsdb:     *accountsdb,
		router:         router,
		enableExplorer: enableExplorer,
	}

	// Set up routes
	server.setupRoutes()

	return server, nil
}

func CloseImmuDBServer(server *ImmuDBServer) {
	DB_OPs.PutMainDBConnection(&server.defaultdb)
	DB_OPs.PutAccountsConnection(&server.accountsdb)
	server.Close()
}

// setupRoutes configures the API routes
func (s *ImmuDBServer) setupRoutes() {
	f, err := os.OpenFile(LOG_FILE, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatal().Err(err).Msg("Error opening log file")
	}
	defer f.Close()

	gin.DefaultWriter = f
	gin.DefaultErrorWriter = f

	// Add CORS middleware
	s.router.Use(cors())

	// Public endpoint for token generation (using API key)
	s.router.POST("/api/auth/token", s.generateToken)

	// Serve static HTML frontend only if explorer is enabled
	if s.enableExplorer {
		s.router.StaticFile("/", "./explorer/index.html")
		s.router.StaticFile("/explorer", "./explorer/index.html")
	}

	// API routes - protected with JWT
	api := s.router.Group("/api/block")
	api.Use(s.jwtAuthMiddleware())
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
	did.Use(s.jwtAuthMiddleware())
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
	stats.Use(s.jwtAuthMiddleware())
	{
		stats.GET("/block/latest", s.getLatestBlockStats)

		stats.GET("/", s.getStats)
	}

	// Address and balance endpoints - protected with JWT
	addresses := s.router.Group("/api/addresses")
	addresses.Use(s.jwtAuthMiddleware())
	{
		// Get transactions for a specific address
		addresses.GET("/transactions/:address", s.getAddressTransactions)
	}

	// transactions endpoint - protected with JWT
	transactions := s.router.Group("/api/transactions")
	transactions.Use(s.jwtAuthMiddleware())
	{
		// Get all the transactions based on the pagination
		transactions.GET("/all", s.listTransactions)

		// Get the transactions based on the block numbers from last
		transactions.GET("/blocks/all", s.listTransactions_fromLastBlock)
	}

	// Websockets to stream realtime blocks - protected with JWT
	sockets := s.router.Group("/api/sockets")
	sockets.Use(s.jwtAuthMiddleware())
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

	log.Info().Str("addr", bindAddr).Msg("Starting ImmuDB API server")

	// Use http.Server for explicit control over binding
	srv := &http.Server{
		Addr:           bindAddr,
		Handler:        s.router,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 20, // 1 MB
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// Close cleans up resources
func (s *ImmuDBServer) Close() {
	if s.defaultdb.Client != nil {
		s.defaultdb.Client.Logger.Logger.Info("Closing the MainDB Connection in the API.go File",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Function, "DB_OPs.Close"),
		)
		DB_OPs.PutAccountsConnection(&s.defaultdb)
	}
	if s.accountsdb.Client != nil {
		s.accountsdb.Client.Logger.Logger.Info("Closing the AccountsDB Connection in the API.go File",
			zap.String(logging.Connection_database, config.AccountsDBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Function, "DB_OPs.Close"),
		)
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

// jwtAuthMiddleware validates JWT tokens in the Authorization header
func (s *ImmuDBServer) jwtAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization header required"})
			c.Abort()
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid authorization header format. Expected: Bearer <token>"})
			c.Abort()
			return
		}

		tokenString := parts[1]

		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			// Validate signing method
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return []byte(config.JWT_SECRET), nil
		})

		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token", "details": err.Error()})
			c.Abort()
			return
		}

		if !token.Valid {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			c.Abort()
			return
		}

		if claims, ok := token.Claims.(jwt.MapClaims); ok {
			c.Set("token_claims", claims)
		}

		c.Next()
	}
}

// generateToken creates a JWT token when provided with a valid API key
func (s *ImmuDBServer) generateToken(c *gin.Context) {
	var req struct {
		APIKey string `json:"api_key" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "API key required"})
		return
	}

	if req.APIKey != config.EXPLORER_API_KEY {
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

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(config.JWT_SECRET))
	if err != nil {
		log.Error().Err(err).Msg("Failed to generate JWT token")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"token":      tokenString,
		"type":       "Bearer",
		"expires_in": int(JWT_EXPIRATION.Seconds()),
		"expires_at": now.Add(JWT_EXPIRATION).Unix(),
	})
}
