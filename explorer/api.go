package explorer

import (
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"
	"go.uber.org/zap"

	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/logging"
)

const(
	LOG_FILE = "explorer.log"
	TOPIC = "explorer"
)

// ImmuDBServer represents the ImmuDB API server
type ImmuDBServer struct {
	defaultdb  config.PooledConnection
	accountsdb config.PooledConnection
	router     *gin.Engine
}

// NewImmuDBServer creates a new ImmuDB API server
func NewImmuDBServer() (*ImmuDBServer, error) {
	// Create ImmuDB client
	defaultdb, err := DB_OPs.GetMainDBConnection()
	if err != nil {
		return nil, err
	}

	accountsdb, err := DB_OPs.GetAccountsConnection()
	if err != nil {
		return nil, err
	}

	// Create gin router with default middleware
	router := gin.Default()

	// Create server instance
	server := &ImmuDBServer{
		defaultdb:  *defaultdb,
		accountsdb: *accountsdb,
		router:     router,
	}

	// Set up routes
	server.setupRoutes()

	return server, nil
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

	// API routes
	api := s.router.Group("/api/block")
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
		api.GET("/latest/:count", s.getLatestBlock)

		// Get all the transactions based on the pagination
		api.GET("/transactions/all", s.listTransactions)
	}

	// Add a new group for Ethereum JSON-RPC
	// s.router.POST("/rpc", s.handleJsonRpc)

	// API routes for DID
	did := s.router.Group("/api/did")
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

	// stats api
	stats := s.router.Group("/api/stats")
	{
		stats.GET("/", s.getStats)
	}

	// Websockets to stream realtime blocks
	sockets := s.router.Group("/api/sockets")
	{
		sockets.GET("/blocks", s.streamBlocks)
	}

}

// Start runs the HTTP server
func (s *ImmuDBServer) Start(addr string) error {
	log.Info().Str("addr", addr).Msg("Starting ImmuDB API server")
	return s.router.Run(addr)
}

// Close cleans up resources
func (s *ImmuDBServer) Close() {
	if s.defaultdb.Client != nil {
		s.defaultdb.Client.Logger.Logger.Info("Closing the MainDB Connection in the API.go File",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Function, "DB_OPs.Close"),
		)
		DB_OPs.PutAccountsConnection(&s.defaultdb)
	}
	if s.accountsdb.Client != nil {
		s.accountsdb.Client.Logger.Logger.Info("Closing the AccountsDB Connection in the API.go File",
			zap.String(logging.Connection_database, config.AccountsDBName),
			zap.Time(logging.Created_at, time.Now()),
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
