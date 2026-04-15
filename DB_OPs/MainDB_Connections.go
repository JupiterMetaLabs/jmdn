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

	DB_OPs_common "gossipnode/DB_OPs/common"
	"gossipnode/config"
	GRO "gossipnode/config/GRO"
	"gossipnode/config/settings"
	"gossipnode/logging"
	"gossipnode/metrics"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/local"
	"github.com/JupiterMetaLabs/ion"
	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/codenotary/immudb/pkg/client"
	"google.golang.org/grpc/metadata"
)

var MainDBLocalGRO interfaces.LocalGoroutineManagerInterface
var (
	mainDBPool     *config.ConnectionPool
	mainDBPoolOnce sync.Once
)

// GetMainDBConnection retrieves a connection from the main database pool.
// Callers are responsible for returning the connection using PutMainDBConnection.
// The context parameter is accepted for future use but is not currently used by the pool.
func GetMainDBConnection(ctx context.Context) (*config.PooledConnection, error) {
	if mainDBPool == nil {
		return nil, errors.New("main database connection pool is not initialized. Call InitMainDBPool first")
	}

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mainDBPool.Logger.Debug(loggerCtx, "Getting main database connection",
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.GetMainDBConnection"))

	conn, err := mainDBPool.Get(loggerCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to get main database connection: %w", err)
	}

	// Update metrics with current pool state
	pc, _, _, ok := runtime.Caller(1)
	if ok {
		fn := runtime.FuncForPC(pc)
		metrics.NewMainDBMetricsBuilder().WithFunction(fn.Name()).ConnectionTaken()
	} else {
		metrics.NewMainDBMetricsBuilder().WithFunction("unknown").ConnectionTaken()
		logger(log.MainDB_Connections).Debug(context.Background(), "Failed to get caller information")
	}

	return conn, nil
}

// PutMainDBConnection returns a connection to the main database pool.
func PutMainDBConnection(conn *config.PooledConnection) {
	if mainDBPool != nil {
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		mainDBPool.Logger.Debug(loggerCtx, "Returning main database connection",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.PutMainDBConnection"))
		mainDBPool.Put(loggerCtx, conn)

		// Update metrics with current pool state
		pc, _, _, ok := runtime.Caller(1)
		if ok {
			fn := runtime.FuncForPC(pc)
			metrics.NewMainDBMetricsBuilder().WithFunction(fn.Name()).ConnectionReturned()
		} else {
			metrics.NewMainDBMetricsBuilder().WithFunction("unknown").ConnectionReturned()
			logger(log.MainDB_Connections).Debug(context.Background(), "Failed to get caller information")
		}
	}
}

// InitMainDBPool initializes the main database connection pool.
func InitMainDBPool(poolConfig *config.ConnectionPoolConfig) error {
	return InitMainDBPoolWithLoki(poolConfig, true, settings.Get().Database.Username, settings.Get().Database.Password)
}

func InitMainDBPoolWithLoki(poolConfig *config.ConnectionPoolConfig, enableLoki bool, username, password string) error {
	var initErr error

	mainDBPoolOnce.Do(func() {
		logger(log.MainDB_Connections).Debug(context.Background(), "Getting async logger for main DB pool")
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Get the async logger instance
		asyncLogger := logging.NewAsyncLogger()
		if asyncLogger == nil || asyncLogger.GlobalLogger == nil {
			logger(log.MainDB_Connections).Error(context.Background(), "Failed to get async logger", fmt.Errorf("logger init failed"))
			initErr = fmt.Errorf("failed to get async logger for main DB pool")
			return
		}
		ionLogger := asyncLogger.GlobalLogger
		logger(log.MainDB_Connections).Info(context.Background(), "Async logger retrieved successfully")

		ionLogger.Debug(loggerCtx, "Initializing main database connection pool",
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.InitMainDBPool"))
		logger(log.MainDB_Connections).Debug(context.Background(), "Connecting to main DB")
		if err := connectToMainDB(username, password); err != nil {
			logger(log.MainDB_Connections).Error(context.Background(), "Failed to connect to main DB", err)
			initErr = fmt.Errorf("failed to ensure main DB selected: %w", err)
			ionLogger.Error(loggerCtx, "Main DB setup failed",
				err,
				ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
				ion.String("log_file", LOG_FILE),
				ion.String("topic", TOPIC),
				ion.String("function", "DB_OPs.InitMainDBPool"))
			return
		}
		logger(log.MainDB_Connections).Info(context.Background(), "Connected to main DB successfully")

		// Now that the DB exists, initialize a dedicated pool for it.
		poolCfg := config.DefaultConnectionPoolConfig()

		poolingConfig := &config.PoolingConfig{
			DBAddress:  config.DBAddress,
			DBPort:     config.DBPort,
			DBName:     config.DBName,
			DBUsername: username,
			DBPassword: password,
		}

		mainDBPool = config.NewConnectionPool(loggerCtx, poolCfg, ionLogger, poolingConfig)
		mainDBPool.Logger.Debug(loggerCtx, "Main database connection pool initialized successfully.",
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.InitMainDBPool"))
		metrics.NewMainDBMetricsBuilder().WithFunction("DB_OPs.InitMainDBPool").SetTotal(mainDBPool.Config.MaxConnections)
		metrics.NewMainDBMetricsBuilder().WithFunction("DB_OPs.InitMainDBPool").SetActive(0)
		metrics.NewMainDBMetricsBuilder().WithFunction("DB_OPs.InitMainDBPool").SetIdle(mainDBPool.Config.MaxConnections)
	})

	return initErr
}

func ensureMainDBSelected(conn *config.PooledConnection) error {
	if conn == nil || conn.Client == nil {
		return fmt.Errorf("invalid connection")
	}

	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn.Client.Logger.Debug(loggerCtx, "Ensuring main database selected",
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.ensureMainDBSelected"))

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
		loggerCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		mainDBPool.Close(loggerCtx)
		mainDBPool = nil
	}
}

// ensureMainDBSelected handles the one-time setup of the main database.
func connectToMainDB(username, password string) error {
	// This function contains the database setup logic from the original NewMainDBClient.
	// It creates a temporary, single-use client.
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Get the async logger instance
	asyncLogger := logging.NewAsyncLogger()
	if asyncLogger == nil || asyncLogger.GlobalLogger == nil {
		return fmt.Errorf("failed to get async logger for DB setup")
	}
	logger := asyncLogger.GlobalLogger

	// defer func() {
	// 	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	// 	defer shutdownCancel()
	// 	if logger != nil {
	// 		logger.Shutdown(shutdownCtx)
	// 	}
	// }()

	// ensure our state dir exists
	stateDir := config.State_Path_Hidden
	if err := os.MkdirAll(stateDir, 0o750); err != nil {
		return fmt.Errorf("could not create state dir: %w", err)
	}

	// build file paths inside .immudb-state
	certFile := filepath.Join(stateDir, "server.cert.pem")
	keyFile := filepath.Join(stateDir, "server.key.pem")
	caFile := filepath.Join(stateDir, "ca.cert.pem")
	logger(log.MainDB_Connections).Debug(context.Background(), "Certificate paths built successfully")

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

	logger.Debug(loggerCtx, "Main database check completed",
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.ensureMainDBSelected"))

	// Create accounts database if it doesn't exist
	if !databaseExists {
		logger.Debug(loggerCtx, "Creating main database",
			ion.String("database", config.DBName),
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.ensureMainDBSelected"))
		err = c.CreateDatabase(authCtx, &schema.DatabaseSettings{
			DatabaseName: config.DBName,
		})
		if err != nil {
			return fmt.Errorf("failed to create main database: %w", err)
		}
		logger.Debug(loggerCtx, "Main database created successfully",
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.ensureMainDBSelected"))
	} else {
		logger.Debug(loggerCtx, "Main database already exists",
			ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
			ion.String("log_file", LOG_FILE),
			ion.String("topic", TOPIC),
			ion.String("function", "DB_OPs.ensureMainDBSelected"))
	}

	logger.Debug(loggerCtx, "Main database setup completed",
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.ensureMainDBSelected"))
	return nil
}

/* GetMainDBConnectionandPutBack retrieves a connection from the main database pool
and automatically returns it to the pool when the context is cancelled or done.

This factory method ensures proper connection cleanup without requiring explicit PutMainDBConnection calls.

Important: The connection will be automatically returned when:
   - The context is cancelled (via cancel() or timeout)
   - The context's Done channel is closed

If you need to return the connection earlier, you can still call PutMainDBConnection manually.
The PutMainDBConnection function is safe to call multiple times.

Usage:

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel() // This will trigger automatic connection return
	conn, err := GetMainDBConnectionandPutBack(ctx)
	if err != nil {
	    return err
	}
	- Use conn - it will be automatically returned when ctx is cancelled.
	- Optionally return early: PutMainDBConnection(conn)
*/

func GetMainDBConnectionandPutBack(ctx context.Context) (*config.PooledConnection, error) {
	if MainDBLocalGRO == nil {
		var err error
		MainDBLocalGRO, err = DB_OPs_common.InitializeGRO(GRO.DB_OPsLocal)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize local gro: %v", err)
		}
	}
	if ctx == nil {
		return nil, errors.New("context cannot be nil - GetMainDBConnectionandPutBack")
	}
	conn, err := GetMainDBConnection(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get main database connection: %w - GetMainDBConnectionandPutBack", err)
	}

	// Log successful connection retrieval
	loggerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn.Client.Logger.Debug(loggerCtx, "Got main database connection",
		ion.String("database", config.DBName),
		ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
		ion.String("log_file", LOG_FILE),
		ion.String("topic", TOPIC),
		ion.String("function", "DB_OPs.GetMainDBConnectionandPutBack"))

	// Set up automatic cleanup when context is done
	// Use a goroutine to monitor context cancellation
	callerCtx := ctx
	deadline, ok := ctx.Deadline()
	if ok {
		// Convert deadline (time.Time) to timeout duration (time.Duration)
		timeout := time.Until(deadline)

		// Only spawn goroutine if context has a deadline
		// This prevents goroutine leaks when using context.Background()
		MainDBLocalGRO.Go(GRO.DB_OPsMainDBConnectionsThread, func(groCtx context.Context) error {
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
			if err != nil {
				loggerCtx, cancel := context.WithCancel(context.Background())
				defer cancel()
				conn.Client.Logger.Debug(loggerCtx, "Auto-returning main database connection due to context completion",
					ion.String("database", config.DBName),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("log_file", LOG_FILE),
					ion.String("topic", TOPIC),
					ion.String("function", "DB_OPs.GetMainDBConnectionandPutBack"),
					ion.String("context_error", err.Error()))
			}

			PutMainDBConnection(conn)
			return nil
		}, local.WithTimeout(timeout))
	} else {
		MainDBLocalGRO.Go(GRO.DB_OPsMainDBConnectionsThread, func(groCtx context.Context) error {
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
			if err != nil {
				loggerCtx, cancel := context.WithCancel(context.Background())
				defer cancel()
				conn.Client.Logger.Debug(loggerCtx, "Auto-returning main database connection due to context completion",
					ion.String("database", config.DBName),
					ion.String("created_at", time.Now().UTC().Format(time.RFC3339)),
					ion.String("log_file", LOG_FILE),
					ion.String("topic", TOPIC),
					ion.String("function", "DB_OPs.GetMainDBConnectionandPutBack"),
					ion.String("context_error", err.Error()))
			}

			PutMainDBConnection(conn)
			return nil
		})
	}

	return conn, nil
}
