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
	"gossipnode/logging"
	"gossipnode/metrics"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/codenotary/immudb/pkg/client"
	"go.uber.org/zap"
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

	mainDBPool.Logger.Logger.Info("Getting main database connection",
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.GetMainDBConnection"),
	)

	conn, err := mainDBPool.Get()
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
		fmt.Println("Failed to get caller information")
	}

	return conn, nil
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
		pc, _, _, ok := runtime.Caller(1)
		if ok {
			fn := runtime.FuncForPC(pc)
			metrics.NewMainDBMetricsBuilder().WithFunction(fn.Name()).ConnectionReturned()
		} else {
			metrics.NewMainDBMetricsBuilder().WithFunction("unknown").ConnectionReturned()
			fmt.Println("Failed to get caller information")
		}
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
	conn.Client.Logger.Logger.Info("Got main database connection",
		zap.String(logging.Connection_database, config.DBName),
		zap.Time(logging.Created_at, time.Now().UTC()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.GetMainDBConnectionandPutBack"),
	)

	// Set up automatic cleanup when context is done
	// Use a goroutine to monitor context cancellation
	callerCtx := ctx
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
			conn.Client.Logger.Logger.Info("Auto-returning main database connection due to context completion",
				zap.String(logging.Connection_database, config.DBName),
				zap.Time(logging.Created_at, time.Now().UTC()),
				zap.String(logging.Log_file, LOG_FILE),
				zap.String(logging.Topic, TOPIC),
				zap.String(logging.Loki_url, LOKI_URL),
				zap.String(logging.Function, "DB_OPs.GetMainDBConnectionandPutBack"),
				zap.String("context_error", err.Error()),
			)
		}

		PutMainDBConnection(conn)
		return nil
	})

	return conn, nil
}
