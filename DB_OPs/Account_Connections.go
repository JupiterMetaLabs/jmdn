package DB_OPs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"gossipnode/DB_OPs/common"
	"gossipnode/config"
	GRO "gossipnode/config/GRO"
	"gossipnode/config/settings"
	log "gossipnode/logging"
	"gossipnode/metrics"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/local"
	"github.com/JupiterMetaLabs/ion"
	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/codenotary/immudb/pkg/client"
	"google.golang.org/grpc/metadata"
)

var (
	DB_OPsLocalGRO interfaces.LocalGoroutineManagerInterface
)

var (
	accountsPool     *config.ConnectionPool
	accountsPoolOnce sync.Once
)

// InitAccountsPoolWithLoki initializes the connection pool for the accounts database with optional Loki support
func InitAccountsPool() error {
	var initErr error
	accountsPoolOnce.Do(func() {
		loggerInstance := logger(log.DB_OPs_AccountConnectionPool)
		if loggerInstance == nil {
			initErr = fmt.Errorf("failed to get logger")
			return
		}

		// Create context for logging (without changing function signature)
		loggerCtx := context.Background()

		// This logic is extracted from the original NewAccountsClient function.
		// It creates a temporary client to ensure the database exists.
		loggerInstance.Debug(loggerCtx, "Initializing accounts database connection pool",
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.InitAccountsPool"))

		if err := ensureAccountsDBExists(settings.Get().Database.Username, settings.Get().Database.Password); err != nil {
			initErr = fmt.Errorf("failed to ensure accounts database exists: %w", err)
			loggerInstance.Error(loggerCtx, "Accounts DB setup failed",
				err,
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.InitAccountsPool"))
			return
		}

		// Now that the DB exists, initialize a dedicated pool for it.
		poolCfg := config.DefaultConnectionPoolConfig()

		poolingConfig := &config.PoolingConfig{
			DBAddress:  config.DBAddress,
			DBPort:     config.DBPort,
			DBName:     config.AccountsDBName,
			DBUsername: settings.Get().Database.Username,
			DBPassword: settings.Get().Database.Password,
		}

		accountsPool = config.NewConnectionPool(loggerCtx, poolCfg, loggerInstance, poolingConfig)
		accountsPool.Logger.Debug(loggerCtx, "Accounts database connection pool initialized successfully",
			ion.String("database", config.AccountsDBName),
			ion.Int("max_connections", accountsPool.Config.MaxConnections),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.InitAccountsPool"))

		metrics.NewAccountsDBMetricsBuilder().WithFunction("DB_OPs.InitAccountsPool").SetTotal(accountsPool.Config.MaxConnections)
		metrics.NewAccountsDBMetricsBuilder().WithFunction("DB_OPs.InitAccountsPool").SetActive(0)
		metrics.NewAccountsDBMetricsBuilder().WithFunction("DB_OPs.InitAccountsPool").SetIdle(accountsPool.Config.MaxConnections)
	})
	return initErr
}

// GetAccountsConnections retrieves a connection from the accounts database pool.
// Callers are responsible for returning the connection using PutAccountsConnection.
// The context parameter is accepted for future use but is not currently used by the pool.
func GetAccountsConnections(ctx context.Context) (*config.PooledConnection, error) {
	if accountsPool == nil {
		return nil, errors.New("accounts connection pool is not initialized. Call InitAccountsPool first")
	}

	// Use provided context or create background context if nil
	loggerCtx := ctx
	var cancel context.CancelFunc
	if loggerCtx == nil {
		loggerCtx, cancel = context.WithCancel(context.Background())
		defer cancel()
	}

	accountsPool.Logger.Debug(loggerCtx, "Getting accounts connection",
		ion.String("database", config.AccountsDBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.GetAccountsConnections"))

	conn, err := accountsPool.Get(loggerCtx)
	if err != nil {
		accountsPool.Logger.Error(loggerCtx, "Failed to get accounts connection",
			err,
			ion.String("database", config.AccountsDBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetAccountsConnections"))
		return nil, err
	}

	// Update metrics with current pool state
	pc, _, _, ok := runtime.Caller(1)
	if ok {
		fn := runtime.FuncForPC(pc)
		metrics.NewAccountsDBMetricsBuilder().WithFunction(fn.Name()).ConnectionTaken()
	} else {
		metrics.NewAccountsDBMetricsBuilder().WithFunction("unknown").ConnectionTaken()
	}

	return conn, nil
}

// PutAccountsConnection returns a connection to the accounts database pool.
func PutAccountsConnection(conn *config.PooledConnection) {
	if accountsPool != nil {
		// Create context for logging (without changing function signature)
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()

		if conn != nil {
			accountsPool.Logger.Debug(loggerCtx, "Returning accounts connection",
				ion.String("database", config.AccountsDBName),
				ion.String("connection_id", conn.Token),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.PutAccountsConnection"))
		} else {
			accountsPool.Logger.Warn(loggerCtx, "PutAccountsConnection called with nil connection",
				ion.String("database", config.AccountsDBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.PutAccountsConnection"))
		}

		accountsPool.Put(loggerCtx, conn)

		// Update metrics with current pool state
		pc, _, _, ok := runtime.Caller(1)
		if ok {
			fn := runtime.FuncForPC(pc)
			metrics.NewAccountsDBMetricsBuilder().WithFunction(fn.Name()).ConnectionReturned()
		} else {
			metrics.NewAccountsDBMetricsBuilder().WithFunction("unknown").ConnectionReturned()
		}
	}
}

// ensureAccountsDBExists handles the one-time setup of the accounts database.
func ensureAccountsDBExists(username, password string) error {
	// Create context for logging (without changing function signature)
	loggerCtx := context.Background()

	// Get logger instance
	loggerInstance := logger("DB_OPs.ensureAccountsDBExists")
	if loggerInstance == nil {
		return fmt.Errorf("failed to get logger for DB setup")
	}

	// ensure our state dir exists
	stateDir := config.State_Path_Hidden
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		loggerInstance.Error(loggerCtx, "Failed to create state directory",
			err,
			ion.String("state_dir", stateDir),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.ensureAccountsDBExists"))
		return fmt.Errorf("could not create state dir: %w", err)
	}

	// build file paths inside .immudb-state
	certFile := filepath.Join(stateDir, "server.cert.pem")
	keyFile := filepath.Join(stateDir, "server.key.pem")
	caFile := filepath.Join(stateDir, "ca.cert.pem")

	loggerInstance.Debug(loggerCtx, "Certificate paths built successfully",
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.ensureAccountsDBExists"))

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
		loggerInstance.Error(loggerCtx, "Failed to create temporary client for DB setup",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.ensureAccountsDBExists"))
		return fmt.Errorf("failed to create temporary client for DB setup: %w", err)
	}
	defer c.Disconnect()

	ctx, cancel := context.WithTimeout(loggerCtx, 30*time.Second)
	defer cancel()

	// Login with admin credentials
	loggerInstance.Debug(ctx, "Authenticating with ImmuDB",
		ion.String("username", username),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.ensureAccountsDBExists"))

	lr, err := c.Login(ctx, []byte(username), []byte(password))
	if err != nil {
		loggerInstance.Error(ctx, "Temporary client login failed",
			err,
			ion.String("username", username),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.ensureAccountsDBExists"))
		return fmt.Errorf("temporary client login failed: %w", err)
	}

	authCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", lr.Token))

	// Check if accounts database exists
	loggerInstance.Debug(authCtx, "Checking if accounts database exists",
		ion.String("database", config.AccountsDBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.ensureAccountsDBExists"))

	databaseList, err := c.DatabaseList(authCtx)
	if err != nil {
		loggerInstance.Error(authCtx, "Failed to get database list",
			err,
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.ensureAccountsDBExists"))
		return fmt.Errorf("failed to get database list: %w", err)
	}

	databaseExists := false
	for _, db := range databaseList.Databases {
		if db.DatabaseName == config.AccountsDBName {
			databaseExists = true
			break
		}
	}

	loggerInstance.Debug(authCtx, "Accounts database check completed",
		ion.Bool("database_exists", databaseExists),
		ion.String("database", config.AccountsDBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.ensureAccountsDBExists"))

	// Create accounts database if it doesn't exist
	if !databaseExists {
		loggerInstance.Debug(authCtx, "Creating accounts database",
			ion.String("database", config.AccountsDBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.ensureAccountsDBExists"))

		err = c.CreateDatabase(authCtx, &schema.DatabaseSettings{
			DatabaseName: config.AccountsDBName,
		})
		if err != nil {
			loggerInstance.Error(authCtx, "Failed to create accounts database",
				err,
				ion.String("database", config.AccountsDBName),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.ensureAccountsDBExists"))
			return fmt.Errorf("failed to create accounts database: %w", err)
		}

		loggerInstance.Debug(authCtx, "Accounts database created successfully",
			ion.String("database", config.AccountsDBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.ensureAccountsDBExists"))
	} else {
		loggerInstance.Debug(authCtx, "Accounts database already exists",
			ion.String("database", config.AccountsDBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.ensureAccountsDBExists"))
	}

	loggerInstance.Debug(authCtx, "Accounts database setup completed",
		ion.String("database", config.AccountsDBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.ensureAccountsDBExists"))

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

	// Create context for logging (without changing function signature)
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var lastErr error

	// Try to check connection with retries
	for i := 0; i < maxRetries; i++ {
		// Try to get current state
		ctx, cancel := context.WithTimeout(loggerCtx, 10*time.Second)
		defer cancel()

		_, err := accountsPool.Client.Client.CurrentState(ctx)
		if err == nil {
			accountsPool.Client.Logger.Debug(ctx, "Database connection check successful",
				ion.String("database", accountsPool.Database),
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.EnsureDBConnection"))
			// Connection is good
			return nil
		}

		lastErr = err

		// Log the failed attempt
		accountsPool.Client.Logger.Error(ctx, "Failed to establish database connection",
			err,
			ion.Int("attempt", i+1),
			ion.Int("max_retries", maxRetries),
			ion.String("database", accountsPool.Database),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.EnsureDBConnection"))

		// If not the last attempt, wait before retrying
		if i < maxRetries-1 {
			time.Sleep(retryDelay)
		}
	}

	// If we got here, all retries failed
	accountsPool.Client.Logger.Error(loggerCtx, "Failed to establish database connection after all retries",
		lastErr,
		ion.Int("max_retries", maxRetries),
		ion.String("database", accountsPool.Database),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.EnsureDBConnection"))

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
	if DB_OPsLocalGRO == nil {
		var err error
		DB_OPsLocalGRO, err = common.InitializeGRO(GRO.DB_OPsLocal)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize local gro: %v", err)
		}
	}
	if ctx == nil {
		return nil, errors.New("context cannot be nil - GetAccountConnectionandPutBack")
	}
	conn, err := GetAccountsConnections(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get accounts connection: %w - GetAccountConnectionandPutBack", err)
	}

	// Log successful connection retrieval
	if conn != nil && conn.Client != nil && conn.Client.Logger != nil {
		conn.Client.Logger.Debug(ctx, "Got accounts connection",
			ion.String("database", config.AccountsDBName),
			ion.String("connection_id", conn.Token),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.GetAccountConnectionandPutBack"))
	}

	// Set up automatic cleanup when context is done
	// Use a goroutine to monitor context cancellation
	callerCtx := ctx
	deadline, ok := ctx.Deadline()
	if ok {

		DB_OPsLocalGRO.Go(GRO.DB_OPsAccountsThread, func(groCtx context.Context) error {
			select {
			case <-callerCtx.Done():
				// caller context cancelled/timed out
			case <-groCtx.Done():
				// goroutine manager is shutting down; still best-effort return the conn
			}

			if conn == nil || conn.Client == nil {
				return nil
			}

			// Put is designed to be idempotent; avoid unsynchronized reads of conn.InUse here.
			err := callerCtx.Err()
			if err == nil {
				err = groCtx.Err()
			}
			if err != nil && conn != nil && conn.Client != nil && conn.Client.Logger != nil {
				conn.Client.Logger.Debug(groCtx, "Auto-returning accounts connection due to context completion",
					ion.String("database", config.AccountsDBName),
					ion.String("connection_id", conn.Token),
					ion.String("context_error", err.Error()),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("log_file", LOG_FILE),
					ion.String("topic", TOPIC),
					ion.String("function", "DB_OPs.GetAccountConnectionandPutBack"))
			}

			PutAccountsConnection(conn)
			return nil
		}, local.WithTimeout(time.Until(deadline)))
	} else {
		DB_OPsLocalGRO.Go(GRO.DB_OPsAccountsThread, func(groCtx context.Context) error {
			select {
			case <-callerCtx.Done():
				// caller context cancelled/timed out
			case <-groCtx.Done():
				// goroutine manager is shutting down; still best-effort return the conn
			}

			if conn == nil || conn.Client == nil {
				return nil
			}

			// Put is designed to be idempotent; avoid unsynchronized reads of conn.InUse here.
			err := callerCtx.Err()
			if err == nil {
				err = groCtx.Err()
			}
			if err != nil && conn != nil && conn.Client != nil && conn.Client.Logger != nil {
				conn.Client.Logger.Debug(groCtx, "Auto-returning accounts connection due to context completion",
					ion.String("database", config.AccountsDBName),
					ion.String("connection_id", conn.Token),
					ion.String("context_error", err.Error()),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("log_file", LOG_FILE),
					ion.String("topic", TOPIC),
					ion.String("function", "DB_OPs.GetAccountConnectionandPutBack"))
			}

			PutAccountsConnection(conn)
			return nil
		})
	}

	return conn, nil
}
