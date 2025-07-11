package DB_OPs

import (
	"context"
	"fmt"
	"gossipnode/config"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/codenotary/immudb/pkg/client"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc/metadata"
)

// DIDDocument represents a DID document
type DIDDocument struct {
	DID       string `json:"did"`
	PublicKey string `json:"public_key"`
	Balance   string `json:"balance,omitempty"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// AccountsConnectionPool extends the base connection pool for accounts database
type AccountsConnectionPool struct {
	*ConnectionPool // Embed the base connection pool
	databaseName    string
}

// Global accounts connection pool
var (
	accountsPool      *AccountsConnectionPool
	accountsPoolOnce  sync.Once
	accountsPoolMutex sync.RWMutex
	clientInitMutex   sync.Mutex // Keep existing mutex for backward compatibility
)

// InitializeAccountsPool initializes the accounts database connection pool
func InitializeAccountsPool(poolConfig *ConnectionPoolConfig) error {
	accountsPoolMutex.Lock()
	defer accountsPoolMutex.Unlock()

	if accountsPool != nil {
		return nil // Already initialized
	}

	logger, err := NewAsyncLogger()
	if err != nil {
		return fmt.Errorf("failed to create logger for accounts pool: %w", err)
	}

	// Create base connection pool
	basePool := NewConnectionPool(poolConfig, logger)

	// Create accounts-specific pool
	accountsPool = &AccountsConnectionPool{
		ConnectionPool: basePool,
		databaseName:   config.AccountsDBName,
	}

	// Initialize minimum connections for accounts database
	if err := initializeAccountsConnections(accountsPool); err != nil {
		accountsPool.Close()
		accountsPool = nil
		return err
	}

	return nil
}

// GetAccountsPool returns the accounts connection pool, initializing it if necessary
func GetAccountsPool() *AccountsConnectionPool {
	accountsPoolMutex.RLock()
	if accountsPool != nil {
		accountsPoolMutex.RUnlock()
		return accountsPool
	}
	accountsPoolMutex.RUnlock()

	// Initialize with default config if not already done
	accountsPoolOnce.Do(func() {
		accountsPoolMutex.Lock()
		defer accountsPoolMutex.Unlock()

		if accountsPool == nil {
			logger, err := NewAsyncLogger()
			if err != nil {
				panic(fmt.Sprintf("failed to create logger for accounts pool: %v", err))
			}

			basePool := NewConnectionPool(DefaultConnectionPoolConfig(), logger)
			accountsPool = &AccountsConnectionPool{
				ConnectionPool: basePool,
				databaseName:   config.AccountsDBName,
			}

			if err := initializeAccountsConnections(accountsPool); err != nil {
				panic(fmt.Sprintf("failed to initialize accounts pool: %v", err))
			}
		}
	})

	return accountsPool
}

// createAccountsConnection creates a new connection specifically for accounts database
func (ap *AccountsConnectionPool) createAccountsConnection() (*PooledConnection, error) {
	if err := os.MkdirAll(config.State_Path_Hidden, 0755); err != nil {
		return nil, fmt.Errorf("failed to create ImmuDB state directory: %w", err)
	}

	config.Info(ap.logger, "Creating new connection to ImmuDB accounts database at %s:%d", ap.address, ap.port)

	opts := client.DefaultOptions().
		WithAddress(ap.address).
		WithPort(ap.port).
		WithDir(config.State_Path_Hidden).
		WithMaxRecvMsgSize(1024 * 1024 * 20). // 20MB message size
		WithDisableIdentityCheck(true)

	c, err := client.NewImmuClient(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}

	// Create context with timeout for connection
	ctx, cancel := context.WithTimeout(context.Background(), ap.config.ConnectionTimeout)

	// Step 1: Login to immudb with default credentials
	config.Info(ap.logger, "Authenticating with ImmuDB for accounts database")
	lr, err := c.Login(ctx, []byte("immudb"), []byte("immudb"))
	if err != nil {
		cancel()
		c.Disconnect()
		return nil, fmt.Errorf("login failed: %w", err)
	}

	// Add auth token to context
	md := metadata.Pairs("authorization", lr.Token)
	authCtx := metadata.NewOutgoingContext(context.Background(), md)

	// Step 2: Check if accounts database exists and create if needed
	if err := ap.ensureAccountsDatabaseExists(c, authCtx); err != nil {
		cancel()
		c.Disconnect()
		return nil, fmt.Errorf("failed to ensure accounts database exists: %w", err)
	}

	// Step 3: Re-login to get a fresh token after database operations
	lr, err = c.Login(authCtx, []byte("immudb"), []byte("immudb"))
	if err != nil {
		cancel()
		c.Disconnect()
		return nil, fmt.Errorf("failed to refresh login: %w", err)
	}

	// Update context with new token
	md = metadata.Pairs("authorization", lr.Token)
	authCtx = metadata.NewOutgoingContext(context.Background(), md)

	// Step 4: Select the accounts database
	config.Info(ap.logger, "Selecting accounts database: %s", ap.databaseName)
	dbResp, err := c.UseDatabase(authCtx, &schema.Database{DatabaseName: ap.databaseName})
	if err != nil {
		cancel()
		c.Disconnect()
		return nil, fmt.Errorf("failed to use accounts database %s: %w", ap.databaseName, err)
	}

	// Step 5: Update context with database-specific token
	md = metadata.Pairs("authorization", dbResp.Token)
	finalCtx := metadata.NewOutgoingContext(context.Background(), md)

	now := time.Now()
	conn := &PooledConnection{
		Client:      c,
		Token:       dbResp.Token,
		TokenExpiry: now.Add(24 * time.Hour), // Tokens typically expire in 24 hours
		Database:    ap.databaseName,
		CreatedAt:   now,
		LastUsed:    now,
		InUse:       false,
		Ctx:         finalCtx,
		Cancel:      cancel,
	}

	config.Info(ap.logger, "Successfully created new connection to accounts database: %s", ap.databaseName)
	return conn, nil
}

// ensureAccountsDatabaseExists checks if accounts database exists and creates it if needed
func (ap *AccountsConnectionPool) ensureAccountsDatabaseExists(c client.ImmuClient, ctx context.Context) error {
	config.Info(ap.logger, "Checking if accounts database exists: %s", ap.databaseName)

	databaseList, err := c.DatabaseList(ctx)
	if err != nil {
		return fmt.Errorf("failed to get database list: %w", err)
	}

	// Check if accounts database exists
	databaseExists := false
	for _, db := range databaseList.Databases {
		if db.DatabaseName == ap.databaseName {
			databaseExists = true
			break
		}
	}

	// Create accounts database if it doesn't exist
	if !databaseExists {
		config.Info(ap.logger, "Creating accounts database: %s", ap.databaseName)

		err = c.CreateDatabase(ctx, &schema.DatabaseSettings{
			DatabaseName: ap.databaseName,
		})
		if err != nil {
			return fmt.Errorf("failed to create accounts database: %w", err)
		}
		config.Info(ap.logger, "Accounts database created successfully: %s", ap.databaseName)
	} else {
		config.Info(ap.logger, "Accounts database already exists: %s", ap.databaseName)
	}

	return nil
}

// Override the base createConnection method to use accounts-specific logic
func (ap *AccountsConnectionPool) getConnection() (*PooledConnection, error) {
	ap.mutex.Lock()
	defer ap.mutex.Unlock()

	if ap.closed {
		return nil, ErrPoolClosed
	}

	// Look for an available connection
	for _, conn := range ap.connections {
		if !conn.InUse {
			// Check if token needs refresh
			if ap.needsTokenRefresh(conn) {
				if err := ap.refreshAccountsConnectionToken(conn); err != nil {
					config.Warning(ap.logger, "Failed to refresh token for accounts connection: %v", err)
					continue
				}
			}

			conn.InUse = true
			conn.LastUsed = time.Now()
			return conn, nil
		}
	}

	// If no available connection and we can create more
	if len(ap.connections) < ap.config.MaxConnections {
		conn, err := ap.createAccountsConnection()
		if err != nil {
			return nil, err
		}

		conn.InUse = true
		ap.connections = append(ap.connections, conn)
		return conn, nil
	}

	return nil, ErrNoAvailableConn
}

// refreshAccountsConnectionToken refreshes the authentication token for accounts database
func (ap *AccountsConnectionPool) refreshAccountsConnectionToken(conn *PooledConnection) error {
	config.Info(ap.logger, "Refreshing authentication token for accounts database")

	// Create new context for login
	ctx, cancel := context.WithTimeout(context.Background(), ap.config.ConnectionTimeout)
	defer cancel()

	// Re-authenticate with default credentials
	lr, err := conn.Client.Login(ctx, []byte("immudb"), []byte("immudb"))
	if err != nil {
		return fmt.Errorf("token refresh login failed: %w", err)
	}

	// Update context with new token
	md := metadata.Pairs("authorization", lr.Token)
	authCtx := metadata.NewOutgoingContext(context.Background(), md)

	// Re-select accounts database to get database-specific token
	dbResp, err := conn.Client.UseDatabase(authCtx, &schema.Database{DatabaseName: ap.databaseName})
	if err != nil {
		return fmt.Errorf("failed to re-select accounts database during token refresh: %w", err)
	}

	// Update connection with new token and context
	conn.Token = dbResp.Token
	conn.TokenExpiry = time.Now().Add(24 * time.Hour)

	md = metadata.Pairs("authorization", dbResp.Token)
	conn.Ctx = metadata.NewOutgoingContext(context.Background(), md)

	config.Info(ap.logger, "Successfully refreshed authentication token for accounts database")
	return nil
}

// initializeAccountsConnections creates the minimum number of connections for accounts database
func initializeAccountsConnections(pool *AccountsConnectionPool) error {
	pool.mutex.Lock()
	defer pool.mutex.Unlock()

	for i := 0; i < pool.config.MinConnections; i++ {
		conn, err := pool.createAccountsConnection()
		if err != nil {
			return fmt.Errorf("failed to initialize accounts connection %d: %w", i+1, err)
		}
		pool.connections = append(pool.connections, conn)
	}

	config.Info(pool.logger, "Initialized accounts connection pool with %d connections", pool.config.MinConnections)
	return nil
}

// withAccountsPooledRetry executes operations using the accounts connection pool
func withAccountsPooledRetry(operation string, fn func(*PooledConnection) error) error {
	pool := GetAccountsPool()
	var err error

	retryLimit := 3 // Default retry limit

	for attempt := 0; attempt <= retryLimit; attempt++ {
		// Get connection from accounts pool
		conn, connErr := pool.getConnection()
		if connErr != nil {
			config.Error(pool.logger, "Failed to get connection from accounts pool for %s (attempt %d/%d): %v",
				operation, attempt+1, retryLimit+1, connErr)
			if attempt == retryLimit {
				return fmt.Errorf("%s failed: %w", operation, connErr)
			}
			time.Sleep(time.Second * time.Duration(attempt+1))
			continue
		}

		// Execute the operation
		err = fn(conn)

		// Always release the connection back to pool
		pool.releaseConnection(conn)

		// If successful, return nil
		if err == nil {
			return nil
		}

		// Check if error is due to connection issues or token expiration
		if isConnectionError(err) || isTokenExpiredError(err) {
			config.Warning(pool.logger, "%s operation failed due to connection/token issue (attempt %d/%d): %v",
				operation, attempt+1, retryLimit+1, err)
			if attempt < retryLimit {
				time.Sleep(time.Second * time.Duration(attempt+1))
				continue
			}
		}

		// Non-connection error or final attempt
		config.Error(pool.logger, "%s operation failed (attempt %d/%d): %v",
			operation, attempt+1, retryLimit+1, err)
		return fmt.Errorf("%s failed: %w", operation, err)
	}

	return err
}

// NewAccountsClient creates a dedicated client for the accounts database
func NewAccountsClient() (*config.ImmuClient, error) {
    // Ensure only one client initialization happens at a time
    clientInitMutex.Lock()
    defer clientInitMutex.Unlock()
    
    // Create a default async logger
    defaultLogger, err := NewAsyncLogger()
    if err != nil {
        return nil, fmt.Errorf("failed to create default logger: %w", err)
    }

        // ensure our state dir exists
    stateDir := config.State_Path_Hidden
    if err := os.MkdirAll(stateDir, 0o755); err != nil {
        return nil, fmt.Errorf("could not create state dir: %w", err)
    }

    // build file paths inside .immudb-state
    certFile := filepath.Join(stateDir, "server.cert.pem")
    keyFile  := filepath.Join(stateDir, "server.key.pem")
    caFile   := filepath.Join(stateDir, "ca.cert.pem") // or ca.cert.pem
    
    // Create a default client
    ic := &config.ImmuClient{
        BaseCtx:     context.Background(),
        RetryLimit:  3,
        Logger:      defaultLogger,
        IsConnected: false,
    }
	if err := os.MkdirAll(config.State_Path_Hidden, 0755); err != nil {
		return nil, fmt.Errorf("failed to create ImmuDB state directory: %w", err)
	}

    // Connect to ImmuDB with a longer timeout for database operations
    opts := client.DefaultOptions().
        WithAddress(config.DBAddress).
        WithPort(config.DBPort).
		WithDir(config.State_Path_Hidden).
        WithMaxRecvMsgSize(1024 * 1024 * 20). // 20MB message sizeo
        WithDisableIdentityCheck(false). 
		WithMTLsOptions(
            client.MTLsOptions{}.WithCertificate(certFile).WithPkey(keyFile).WithClientCAs(caFile).WithServername(config.DBAddress),
        )

    c, err := client.NewImmuClient(opts)
    if err != nil {
        config.Close(ic.Logger)
        return nil, fmt.Errorf("failed to create client: %w", err)
    }

    // Create context with longer timeout for database operations
    ctx, cancel := context.WithTimeout(ic.BaseCtx, 30*time.Second)
    defer func() {
        if !ic.IsConnected {
            cancel()
            c.Disconnect()
        }
    }()
    
    // Step 1: Login to immudb with default credentials
    log.Info().Msg("Authenticating with ImmuDB for accounts database")
    lr, err := c.Login(ctx, []byte(config.DBUsername), []byte(config.DBPassword))
    if err != nil {
        config.Close(ic.Logger)
        return nil, fmt.Errorf("login failed: %w", err)
    }

    // Store initial token for reconnection
    ic.Token = lr.Token
    
    // Add auth token to context
    md := metadata.Pairs("authorization", lr.Token)
    ctx = metadata.NewOutgoingContext(ctx, md)
    
    // Step 2: Check if accounts database exists
    log.Info().Str("database", config.AccountsDBName).Msg("Checking if accounts database exists")
    
    databaseList, err := c.DatabaseList(ctx)
    if err != nil {
        config.Close(ic.Logger)
        return nil, fmt.Errorf("failed to get database list: %w", err)
    }
    
    // Check if accounts database exists
    databaseExists := false
    for _, db := range databaseList.Databases {
        if db.DatabaseName == config.AccountsDBName {
            databaseExists = true
            break
        }
    }
    
    // Step 3: Create accounts database if it doesn't exist
    if !databaseExists {
        log.Info().Str("database", config.AccountsDBName).Msg("Creating accounts database")
        
        // Create the database using the initial token
        err = c.CreateDatabase(ctx, &schema.DatabaseSettings{
            DatabaseName: config.AccountsDBName,
        })
        if err != nil {
            config.Close(ic.Logger)
            return nil, fmt.Errorf("failed to create accounts database: %w", err)
        }
        log.Info().Str("database", config.AccountsDBName).Msg("Accounts database created successfully")
    } else {
        log.Info().Str("database", config.AccountsDBName).Msg("Accounts database already exists")
    }
    
    // Step 4: Re-login to get a fresh token
    // This is critical as tokens are database-specific
    lr, err = c.Login(ctx, []byte(config.DBUsername), []byte(config.DBPassword))
    if err != nil {
        config.Close(ic.Logger)
        return nil, fmt.Errorf("failed to refresh login: %w", err)
    }
    
    // Update the token and context
    ic.Token = lr.Token
    md = metadata.Pairs("authorization", lr.Token)
    ctx = metadata.NewOutgoingContext(ctx, md)
    
    // Step 5: Select the accounts database - critical step!
    log.Info().Str("database", config.AccountsDBName).Msg("Selecting accounts database")
    dbResp, err := c.UseDatabase(ctx, &schema.Database{DatabaseName: config.AccountsDBName})
    if err != nil {
        config.Close(ic.Logger)
        return nil, fmt.Errorf("failed to use accounts database: %w", err)
    }
    
    // Step 6: Update token again with the database-specific token
    ic.Token = dbResp.Token
    
    // Keep the context with authentication and database selection
    ic.Client = c
    ic.Ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", ic.Token))
    ic.Cancel = cancel
    ic.IsConnected = true
	ic.Database = config.AccountsDBName
    
    log.Info().Str("database", config.AccountsDBName).Msg("Successfully connected to accounts database")
    return ic, nil
}


// StoreDID stores a DID document in the accounts database (UNCHANGED - but can optionally use connection pool)
func StoreDID(ic *config.ImmuClient, didDoc *DIDDocument) error {
	if didDoc == nil {
		return fmt.Errorf("DID document cannot be nil")
	}

	// Set creation and update timestamps
	if didDoc.CreatedAt == 0 {
		didDoc.CreatedAt = time.Now().Unix()
	}
	didDoc.UpdatedAt = time.Now().Unix()

	key := fmt.Sprintf("did:%s", didDoc.DID)

	// Try to use connection pool if available, otherwise fall back to traditional approach
	if ic == nil {
		// Use accounts connection pool approach
		return withAccountsPooledRetry("StoreDID", func(conn *PooledConnection) error {
			// Ensure we're connected to accounts database
			if err := ensureAccountsDBSelectedForConnection(conn); err != nil {
				return fmt.Errorf("failed to ensure accounts database is selected: %w", err)
			}

			pool := GetAccountsPool()
			config.Info(pool.logger, "Storing DID document: %s", didDoc.DID)

			// Convert to bytes for storage
			valueBytes, err := toBytes(didDoc)
			if err != nil {
				return err
			}

			// Store with verification
			_, err = conn.Client.VerifiedSet(conn.Ctx, []byte(key), valueBytes)
			if err != nil {
				return err
			}

			config.Info(pool.logger, "Successfully stored DID document: %s", didDoc.DID)
			return nil
		})
	}

	// Traditional approach with single connection
	// Ensure we're using the accounts database
	if err := ensureAccountsDBSelected(ic); err != nil {
		return fmt.Errorf("failed to ensure accounts database is selected: %w", err)
	}

	// Store using SafeCreate for cryptographic verification
	return SafeCreate(ic, key, didDoc)
}

// GetDID retrieves a DID document from the accounts database (UNCHANGED - but can optionally use connection pool)
func GetDID(ic *config.ImmuClient, did string) (*DIDDocument, error) {
	key := fmt.Sprintf("did:%s", did)

	// Try to use connection pool if available, otherwise fall back to traditional approach
	if ic == nil {
		// Use accounts connection pool approach
		var doc DIDDocument

		err := withAccountsPooledRetry("GetDID", func(conn *PooledConnection) error {
			// Ensure we're connected to accounts database
			if err := ensureAccountsDBSelectedForConnection(conn); err != nil {
				return fmt.Errorf("failed to ensure accounts database is selected: %w", err)
			}

			pool := GetAccountsPool()
			config.Info(pool.logger, "Reading DID document: %s", did)

			entry, err := conn.Client.VerifiedGet(conn.Ctx, []byte(key))
			if err != nil {
				if err.Error() == "key not found" {
					return ErrNotFound
				}
				return err
			}

			// Unmarshal the data
			if _, err := toBytes(entry.Value); err != nil {
				return fmt.Errorf("failed to unmarshal DID document: %w", err)
			}

			config.Info(pool.logger, "Successfully retrieved DID document: %s", did)
			return nil
		})

		if err != nil {
			return nil, err
		}

		return &doc, nil
	}

	// Traditional approach with single connection
	// Ensure we're using the accounts database
	if err := ensureAccountsDBSelected(ic); err != nil {
		return nil, fmt.Errorf("failed to ensure accounts database is selected: %w", err)
	}

	var doc DIDDocument
	err := SafeReadJSON(ic, key, &doc)
	if err != nil {
		return nil, err
	}

	return &doc, nil
}

// UpdateDIDBalance updates the balance for a DID (UNCHANGED)
func UpdateDIDBalance(ic *config.ImmuClient, did string, newBalance string) error {
	// Ensure we're using the accounts database
	if ic != nil {
		if err := ensureAccountsDBSelected(ic); err != nil {
			return fmt.Errorf("failed to ensure accounts database is selected: %w", err)
		}
	}

	doc, err := GetDID(ic, did)
	if err != nil {
		return err
	}

	doc.Balance = newBalance
	doc.UpdatedAt = time.Now().Unix()

	return StoreDID(ic, doc)
}

// ListAllDIDs retrieves all DIDs with a limit (UNCHANGED - but can optionally use connection pool)
func ListAllDIDs(ic *config.ImmuClient, limit int) ([]*DIDDocument, error) {
	// Try to use connection pool if available, otherwise fall back to traditional approach
	if ic == nil {
		// Use accounts connection pool approach
		var keys []string

		err := withAccountsPooledRetry("ListAllDIDs", func(conn *PooledConnection) error {
			// Ensure we're connected to accounts database
			if err := ensureAccountsDBSelectedForConnection(conn); err != nil {
				return fmt.Errorf("failed to ensure accounts database is selected: %w", err)
			}

			pool := GetAccountsPool()
			config.Info(pool.logger, "Scanning for all DID keys")

			// Get all keys with "did:" prefix
			var allKeys []string
			batchSize := 1000
			var lastKey []byte

			for {
				scanReq := &schema.ScanRequest{
					Prefix:  []byte("did:"),
					Limit:   uint64(batchSize),
					SeekKey: lastKey,
				}

				scanResult, err := conn.Client.Scan(conn.Ctx, scanReq)
				if err != nil {
					return err
				}

				if len(scanResult.Entries) == 0 {
					break
				}

				for _, entry := range scanResult.Entries {
					allKeys = append(allKeys, string(entry.Key))
				}

				if len(scanResult.Entries) < batchSize {
					break
				}

				lastKey = []byte(allKeys[len(allKeys)-1])
			}

			keys = allKeys
			config.Info(pool.logger, "Found %d DID keys", len(keys))
			return nil
		})

		if err != nil {
			return nil, err
		}

		// Now retrieve all DID documents
		docs := make([]*DIDDocument, 0, len(keys))
		for _, key := range keys {
			// Extract DID from key (remove "did:" prefix)
			didValue := key[4:] // Remove "did:" prefix

			doc, err := GetDID(nil, didValue)
			if err != nil {
				pool := GetAccountsPool()
				config.Warning(pool.logger, "Error reading DID %s: %v", key, err)
				continue
			}
			docs = append(docs, doc)
		}

		return docs, nil
	}

	// Traditional approach with single connection
	// Ensure we're using the accounts database
	if err := ensureAccountsDBSelected(ic); err != nil {
		return nil, fmt.Errorf("failed to ensure accounts database is selected: %w", err)
	}

	keys, err := GetAllKeys(ic, "did:")
	if err != nil {
		return nil, err
	}

	docs := make([]*DIDDocument, 0, len(keys))
	for _, key := range keys {
		var doc DIDDocument
		if err := SafeReadJSON(ic, key, &doc); err != nil {
			config.Warning(ic.Logger, "Error reading DID %s: %v", key, err)
			continue
		}
		docs = append(docs, &doc)
	}

	return docs, nil
}

// ensureAccountsDBSelected makes sure we're using the accounts database (UNCHANGED)
// This helps prevent the "please select a database first" error
func ensureAccountsDBSelected(ic *config.ImmuClient) error {
	if ic == nil || ic.Client == nil || !ic.IsConnected {
		return fmt.Errorf("client not connected")
	}

	// Try a simple operation to see if the database selection is still valid
	ctx, cancel := context.WithTimeout(ic.BaseCtx, 5*time.Second)
	defer cancel()

	// Use the stored token
	md := metadata.Pairs("authorization", ic.Token)
	ctx = metadata.NewOutgoingContext(ctx, md)

	// Try to execute a simple operation to check if we're still connected
	_, err := ic.Client.CurrentState(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("Database state check failed, reconnecting...")
		return reconnectToAccountsDB(ic)
	}

	return nil
}

// ensureAccountsDBSelectedForConnection ensures pooled connection is valid for accounts database
func ensureAccountsDBSelectedForConnection(conn *PooledConnection) error {
	if conn == nil || conn.Client == nil {
		return fmt.Errorf("connection not available")
	}

	// Try a simple operation to see if the database selection is still valid
	_, err := conn.Client.CurrentState(conn.Ctx)
	if err != nil {
		return fmt.Errorf("accounts database connection check failed: %w", err)
	}

	return nil
}

// reconnectToAccountsDB attempts to reestablish a lost connection to the accounts database (UNCHANGED)
func reconnectToAccountsDB(ic *config.ImmuClient) error {
	config.Warning(ic.Logger, "Attempting to reconnect to ImmuDB accounts database")

	// Clean up existing connection if any
	if ic.Cancel != nil {
		ic.Cancel()
	}

	if ic.Client != nil {
		ic.Client.Disconnect()
	}

	ic.IsConnected = false

	// Create a new client
	opts := client.DefaultOptions().
		WithAddress(config.DBAddress).
		WithPort(config.DBPort).
		WithMaxRecvMsgSize(1024 * 1024 * 20) // 20MB message size

	c, err := client.NewImmuClient(opts)
	if err != nil {
		return fmt.Errorf("failed to create client during reconnect: %w", err)
	}

	// Create context
	ctx, cancel := context.WithTimeout(ic.BaseCtx, 30*time.Second)

	// Login to immudb
	config.Info(ic.Logger, "Authenticating with ImmuDB during reconnect")
	lr, err := c.Login(ctx, []byte("immudb"), []byte("immudb"))
	if err != nil {
		cancel()
		c.Disconnect()
		return fmt.Errorf("login failed during reconnect: %w", err)
	}

	// Update token
	ic.Token = lr.Token

	// Add auth token to context
	md := metadata.Pairs("authorization", lr.Token)
	ctx = metadata.NewOutgoingContext(ctx, md)

	// Select the accounts database
	log.Info().Str("database", config.AccountsDBName).Msg("Selecting accounts database during reconnection")
	dbResp, err := c.UseDatabase(ctx, &schema.Database{DatabaseName: config.AccountsDBName})
	if err != nil {
		cancel()
		c.Disconnect()
		return fmt.Errorf("failed to select accounts database during reconnect: %w", err)
	}

	// Update token with database-specific token
	ic.Token = dbResp.Token
	ic.Client = c
	ic.Ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", ic.Token))
	ic.Cancel = cancel
	ic.IsConnected = true

	config.Info(ic.Logger, "Successfully reconnected to ImmuDB accounts database")
	return nil
}

// ========================================
// OPTIONAL ENHANCED FUNCTIONS FOR CONVENIENCE
// ========================================

// EnableAccountsConnectionPooling enables connection pooling specifically for accounts database
func EnableAccountsConnectionPooling(poolConfig *ConnectionPoolConfig) error {
	return InitializeAccountsPool(poolConfig)
}

// GetAccountsPoolStatistics returns accounts connection pool statistics
func GetAccountsPoolStatistics() map[string]interface{} {
	pool := GetAccountsPool()
	return pool.GetPoolStats()
}

// CloseAccountsPool closes the accounts connection pool
func CloseAccountsPool() error {
	accountsPoolMutex.Lock()
	defer accountsPoolMutex.Unlock()

	if accountsPool != nil {
		err := accountsPool.Close()
		accountsPool = nil
		return err
	}

	return nil
}

// StoreDIDPooled stores a DID using connection pool (convenience function)
func StoreDIDPooled(didDoc *DIDDocument) error {
	return StoreDID(nil, didDoc)
}

// GetDIDPooled retrieves a DID using connection pool (convenience function)
func GetDIDPooled(did string) (*DIDDocument, error) {
	return GetDID(nil, did)
}

// UpdateDIDBalancePooled updates DID balance using connection pool (convenience function)
func UpdateDIDBalancePooled(did string, newBalance string) error {
	return UpdateDIDBalance(nil, did, newBalance)
}

// ListAllDIDsPooled retrieves all DIDs using connection pool (convenience function)
func ListAllDIDsPooled(limit int) ([]*DIDDocument, error) {
	return ListAllDIDs(nil, limit)
}
