package DB_OPs

import (
	"context"
	"errors"
	"fmt"
	"gossipnode/config"
	"gossipnode/logging"
	"gossipnode/metrics"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/codenotary/immudb/pkg/client"
	"github.com/rs/zerolog/log"
	"go.uber.org/zap"
	"google.golang.org/grpc/metadata"
)

var (
	accountsPool     *config.ConnectionPool
	accountsPoolOnce sync.Once
)

// InitAccountsPool initializes the connection pool for the accounts database.
// It ensures the database exists, creating it if necessary. This function
// should be called once at application startup.
func InitAccountsPool() error {
	return InitAccountsPoolWithLoki(true, config.DBUsername, config.DBPassword)
}

// InitAccountsPoolWithLoki initializes the connection pool for the accounts database with optional Loki support
func InitAccountsPoolWithLoki(enableLoki bool, username, password string) error {
	var initErr error
	accountsPoolOnce.Do(func() {
		logger, err := config.NewAsyncLoggerWithLoki(enableLoki)
		if err != nil {
			initErr = fmt.Errorf("could not create logger for accounts pool: %w", err)
			fmt.Println("Logger creation for accounts pool failed")
			return
		}

		// This logic is extracted from the original NewAccountsClient function.
		// It creates a temporary client to ensure the database exists.
		logger.Logger.Info("Initializing accounts database connection pool",
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.InitAccountsPool"),
		)
		if err := ensureAccountsDBExists(username, password); err != nil {
			initErr = fmt.Errorf("failed to ensure accounts database exists: %w", err)
			logger.Logger.Error("Accounts DB setup failed",
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.InitAccountsPool"),
			)
			return
		}

		// Now that the DB exists, initialize a dedicated pool for it.
		poolCfg := config.DefaultConnectionPoolConfig()

		poolingConfig := &config.PoolingConfig{
			DBAddress:  config.DBAddress,
			DBPort:     config.DBPort,
			DBName:     config.AccountsDBName,
			DBUsername: username,
			DBPassword: password,
		}

		accountsPool = config.NewConnectionPool(poolCfg, logger, poolingConfig)
		accountsPool.Logger.Logger.Info("Accounts database connection pool initialized successfully.",
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.InitAccountsPool"),
		)
		metrics.InitlizeAccountsDBConnectionPoolCount(poolCfg.MinConnections)
	})
	return initErr
}

// GetAccountsConnection retrieves a connection from the accounts database pool.
// Callers are responsible for returning the connection using PutAccountsConnection.
// This context help to put back the connections properly in the pool if putback connection is not called.
func GetAccountsConnections(ctx context.Context) (*config.PooledConnection, error) {
	if accountsPool == nil {
		return nil, errors.New("accounts connection pool is not initialized. Call InitAccountsPool first")
	}
	accountsPool.Logger.Logger.Info("Getting accounts connection: %s",
		zap.String(logging.Connection_database, config.AccountsDBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.GetAccountsConnection"),
	)
	conn, err := accountsPool.Get()
	if err != nil {
		return nil, err
	}

	// Update metrics with current pool state
	metrics.UpdateAccountsDBConnectionPoolMetrics(
		accountsPool.GetPoolSize(),
		accountsPool.GetActiveConnections(),
		accountsPool.GetIdleConnections(),
	)

	return conn, nil
}

// PutAccountsConnection returns a connection to the accounts database pool.
func PutAccountsConnection(conn *config.PooledConnection) {
	if accountsPool != nil {
		accountsPool.Logger.Logger.Info("Returning accounts connection: %s",
			zap.String(logging.Connection_database, config.AccountsDBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.PutAccountsConnection"),
		)
		accountsPool.Put(conn)

		// Update metrics with current pool state
		metrics.UpdateAccountsDBConnectionPoolMetrics(
			accountsPool.GetPoolSize(),
			accountsPool.GetActiveConnections(),
			accountsPool.GetIdleConnections(),
		)
	}
}

// ensureAccountsDBExists handles the one-time setup of the accounts database.
func ensureAccountsDBExists(username, password string) error {
	// This function contains the database setup logic from the original NewAccountsClient.
	// It creates a temporary, single-use client.
	logger, err := config.NewAsyncLogger()
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
		WithMaxRecvMsgSize(1024 * 1024 * 20). // 20MB message size
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
		if db.DatabaseName == config.AccountsDBName {
			databaseExists = true
			break
		}
	}
	logger.Logger.Info("Accounts database check completed",
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.ensureAccountsDBExists"),
	)

	// Create accounts database if it doesn't exist
	if !databaseExists {
		logger.Logger.Info("Creating accounts database", zap.String("database", config.AccountsDBName),
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.ensureAccountsDBExists"),
		)
		err = c.CreateDatabase(authCtx, &schema.DatabaseSettings{
			DatabaseName: config.AccountsDBName,
		})
		if err != nil {
			return fmt.Errorf("failed to create accounts database: %w", err)
		}
		logger.Logger.Info("Accounts database created successfully",
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.ensureAccountsDBExists"),
		)
	} else {
		logger.Logger.Info("Accounts database already exists",
			zap.Time(logging.Created_at, time.Now().UTC()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.ensureAccountsDBExists"),
		)
	}

	logger.Logger.Info("Accounts database setup completed",
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.ensureAccountsDBExists"),
	)
	return nil
}

// EnsureDBConnection checks if the database connection is active and attempts to reconnect if necessary.
// It returns an error if the connection cannot be established after retries.
func EnsureDBConnection(accountsPool *config.PooledConnection) error {
	const maxRetries = 3
	const retryDelay = 2 * time.Second

	if accountsPool == nil {
		return errors.New("accountsPool is nil")
	}

	// Check if client exists
	if accountsPool.Client == nil {
		return errors.New("database client is not initialized")
	}

	var lastErr error

	// Try to check connection with retries
	for i := 0; i < maxRetries; i++ {

		// Try to get current state
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		_, err := accountsPool.Client.Client.CurrentState(ctx)
		if err == nil {
			accountsPool.Client.Logger.Logger.Info("Database connection check successful",
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.EnsureDBConnection"),
			)
			// Connection is good
			return nil
		}

		lastErr = err

		// Log the failed attempt
		log.Error().Err(err).Msg("Failed to establish database connection")

		// If not the last attempt, wait before retrying
		if i < maxRetries-1 {
			time.Sleep(retryDelay)
		}
	}

	// If we got here, all retries failed
	return fmt.Errorf("failed to establish database connection after %d attempts: %w", maxRetries, lastErr)
}


/* GetAccountConnectionandPutBack retrieves a connection from the accounts database pool
and automatically returns it to the pool when the context is cancelled or done.

This factory method ensures proper connection cleanup without requiring explicit PutAccountsConnection calls.

Important: The connection will be automatically returned when:
   - The context is cancelled (via cancel() or timeout)
   - The context's Done channel is closed

If you need to return the connection earlier, you can still call PutAccountsConnection manually.
The PutAccountsConnection function is safe to call multiple times.

Usage:

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel() // This will trigger automatic connection return
	conn, err := GetAccountConnectionandPutBack(ctx)
	if err != nil {
	    return err
	}
	- Use conn - it will be automatically returned when ctx is cancelled.
	- Optionally return early: PutAccountsConnection(conn)
*/

func GetAccountConnectionandPutBack(ctx context.Context) (*config.PooledConnection, error) {
	if ctx == nil {
		return nil, errors.New("context cannot be nil - GetAccountConnectionandPutBack")
	}
	conn, err := GetAccountsConnections(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get accounts connection: %w - GetAccountConnectionandPutBack", err)
	}

	// Log successful connection retrieval
	conn.Client.Logger.Logger.Info("Got accounts connection",
		zap.String(logging.Connection_database, config.AccountsDBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.GetAccountConnectionandPutBack"),
	)

	// Set up automatic cleanup when context is done
	// Use a goroutine to monitor context cancellation
	go func() {
		<-ctx.Done()
		// Context was cancelled or timed out, return connection to pool
		// Only return if connection is still in use (not already manually returned)
		if conn != nil && conn.Client != nil && conn.InUse {
			conn.Client.Logger.Logger.Info("Auto-returning accounts connection due to context cancellation",
				zap.String(logging.Connection_database, config.AccountsDBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.GetAccountConnectionandPutBack"),
				zap.String("context_error", ctx.Err().Error()),
			)
			// Debugging
			fmt.Printf("Auto-returning accounts connection due to context cancellation: %s\n", conn.Client.Ctx)
			PutAccountsConnection(conn)
		} else if conn != nil && conn.Client != nil && !conn.InUse {
			// Connection was already manually returned, no need to return again
			conn.Client.Logger.Logger.Info("Connection already returned, skipping auto-return",
				zap.String(logging.Connection_database, config.AccountsDBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.GetAccountConnectionandPutBack"),
			)
		}
	}()

	return conn, nil
}
