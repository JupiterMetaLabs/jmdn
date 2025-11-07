package DB_OPs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gossipnode/config"
	"gossipnode/logging"
	"gossipnode/metrics"

	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/codenotary/immudb/pkg/client"
	"go.uber.org/zap"
	"google.golang.org/grpc/metadata"
)

var (
	mainDBPool     *config.ConnectionPool
	mainDBPoolOnce sync.Once
)

// GetMainDBConnection retrieves a connection from the main database pool.
// Callers are responsible for returning the connection using PutMainDBConnection.
func GetMainDBConnection() (*config.PooledConnection, error) {
	fmt.Println("GetMainDBConnection called - checking if pool is initialized...")
	if mainDBPool == nil {
		fmt.Println("mainDBPool is nil - pool not initialized")
		return nil, errors.New("main database connection pool is not initialized. Call InitMainDBPool first")
	}
	// fmt.Println("mainDBPool is not nil - attempting to get connection...")

	mainDBPool.Logger.Logger.Info("Getting main database connection",
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.GetMainDBConnection"),
	)

	Pool, err := mainDBPool.Get()
	if err != nil {
		// debugging
		// fmt.Println("failed to get main database connection: %w", err)
		return nil, fmt.Errorf("failed to get main database connection: %w", err)
	}

	// Update metrics with current pool state
	metrics.UpdateMainDBConnectionPoolMetrics(
		mainDBPool.GetPoolSize(),
		mainDBPool.GetActiveConnections(),
		mainDBPool.GetIdleConnections(),
	)

	// debugging
	// fmt.Println("got main database connection successfully", Pool)
	return Pool, nil
}

// PutMainDBConnection returns a connection to the main database pool.
func PutMainDBConnection(conn *config.PooledConnection) {
	if mainDBPool != nil {
		mainDBPool.Logger.Logger.Info("Returning main database connection",
			zap.String(logging.Connection_database, config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.PutMainDBConnection"),
		)
		mainDBPool.Put(conn)

		// Update metrics with current pool state
		metrics.UpdateMainDBConnectionPoolMetrics(
			mainDBPool.GetPoolSize(),
			mainDBPool.GetActiveConnections(),
			mainDBPool.GetIdleConnections(),
		)
	}
}

// InitMainDBPool initializes the main database connection pool.
func InitMainDBPool(poolConfig *config.ConnectionPoolConfig) error {
	return InitMainDBPoolWithLoki(poolConfig, true, config.DBUsername, config.DBPassword)
}

func InitMainDBPoolWithLoki(poolConfig *config.ConnectionPoolConfig, enableLoki bool, username, password string) error {
	var initErr error

	mainDBPoolOnce.Do(func() {
		fmt.Println("Creating logger for main DB pool...")
		logger, err := config.NewAsyncLoggerWithLoki(enableLoki)
		if err != nil {
			fmt.Printf("Failed to create logger: %v\n", err)
			initErr = fmt.Errorf("failed to create logger for main DB pool: %w", err)
			return
		}
		defer logger.Close()
		fmt.Println("Logger created successfully")

		logger.Logger.Info("Initializing main database connection pool",
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.InitMainDBPool"),
		)
		fmt.Println("Connecting to main DB...")
		if err := connectToMainDB(username, password); err != nil {
			fmt.Printf("Failed to connect to main DB: %v\n", err)
			initErr = fmt.Errorf("failed to ensure main DB selected: %w", err)
			logger.Logger.Error("Main DB setup failed",
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.InitMainDBPool"),
			)
			return
		}
		fmt.Println("Connected to main DB successfully")

		// Now that the DB exists, initialize a dedicated pool for it.
		poolCfg := config.DefaultConnectionPoolConfig()

		poolingConfig := &config.PoolingConfig{
			DBAddress:  config.DBAddress,
			DBPort:     config.DBPort,
			DBName:     config.DBName,
			DBUsername: username,
			DBPassword: password,
		}

		mainDBPool = config.NewConnectionPool(poolCfg, logger, poolingConfig)
		mainDBPool.Logger.Logger.Info("Main database connection pool initialized successfully.",
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.InitMainDBPool"),
		)
		metrics.InitlizeMainDBConnectionPoolCount(poolCfg.MinConnections)
	})

	return initErr
}

func ensureMainDBSelected(conn *config.PooledConnection) error {
	if conn == nil || conn.Client == nil {
		return fmt.Errorf("invalid connection")
	}

	conn.Client.Logger.Logger.Info("Ensuring main database selected",
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.ensureMainDBSelected"),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Re-select database to get database-specific token
	dbResp, err := conn.Client.Client.UseDatabase(ctx, &schema.Database{DatabaseName: config.DBName})
	if err != nil {
		return fmt.Errorf("failed to re-select database during token refresh: %w", err)
	}

	// Update connection with new token and context
	conn.Token = dbResp.Token
	conn.Client.Ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", conn.Token))

	return nil
}

// CloseMainDBPool closes the main database connection pool
func CloseMainDBPool() {
	if mainDBPool != nil {
		mainDBPool.Close()
		mainDBPool = nil
	}
}

// ensureMainDBSelected handles the one-time setup of the main database.
func connectToMainDB(username, password string) error {
	// This function contains the database setup logic from the original NewMainDBClient.
	// It creates a temporary, single-use client.
	logger, err := config.NewAsyncLoggerWithLoki(false) // Disable Loki for temporary client
	if err != nil {
		return fmt.Errorf("failed to create logger for DB setup: %w", err)
	}
	defer logger.Close()

	// ensure our state dir exists
	stateDir := config.State_Path_Hidden
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("could not create state dir: %w", err)
	}

	// build file paths inside .immudb-state
	certFile := filepath.Join(stateDir, "server.cert.pem")
	keyFile := filepath.Join(stateDir, "server.key.pem")
	caFile := filepath.Join(stateDir, "ca.cert.pem")
	fmt.Println("Certificate paths built successfully")

	// Configure the client - disable mTLS for local development
	opts := client.DefaultOptions().
		WithAddress(config.DBAddress).
		WithPort(config.DBPort).
		WithDir(stateDir).
		WithMaxRecvMsgSize(1024 * 1024 * 200). // 20MB message size
		WithDisableIdentityCheck(false).
		WithMTLsOptions(
			client.MTLsOptions{}.WithCertificate(certFile).WithPkey(keyFile).WithClientCAs(caFile).WithServername(config.DBAddress),
		)

	c, err := client.NewImmuClient(opts)
	if err != nil {
		return fmt.Errorf("failed to create temporary client for DB setup: %w", err)
	}
	defer c.Disconnect()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Login with admin credentials
	lr, err := c.Login(ctx, []byte(username), []byte(password))
	if err != nil {
		return fmt.Errorf("temporary client login failed: %w", err)
	}

	authCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", lr.Token))

	// Check if accounts database exists
	databaseList, err := c.DatabaseList(authCtx)
	if err != nil {
		return fmt.Errorf("failed to get database list: %w", err)
	}

	databaseExists := false
	for _, db := range databaseList.Databases {
		if db.DatabaseName == config.DBName {
			databaseExists = true
			break
		}
	}

	logger.Logger.Info("Main database check completed",
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.ensureMainDBSelected"),
	)

	// Create accounts database if it doesn't exist
	if !databaseExists {
		logger.Logger.Info("Creating main database", zap.String("database", config.DBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.ensureMainDBSelected"),
		)
		err = c.CreateDatabase(authCtx, &schema.DatabaseSettings{
			DatabaseName: config.DBName,
		})
		if err != nil {
			return fmt.Errorf("failed to create main database: %w", err)
		}
		logger.Logger.Info("Main database created successfully",
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.ensureMainDBSelected"),
		)
	} else {
		logger.Logger.Info("Main database already exists",
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.ensureMainDBSelected"),
		)
	}

	logger.Logger.Info("Main database setup completed",
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.ensureMainDBSelected"),
	)
	return nil
}
