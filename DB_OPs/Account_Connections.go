package DB_OPs

import (
	"context"
	"errors"
	"fmt"
	"gossipnode/config"
	"gossipnode/logging"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/codenotary/immudb/pkg/client"
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
	var initErr error
	accountsPoolOnce.Do(func() {
		logger, err := config.NewAsyncLogger()
		if err != nil {
			initErr = fmt.Errorf("could not create logger for accounts pool: %w", err)
			fmt.Println("Logger creation for accounts pool failed")
			return
		}

		// This logic is extracted from the original NewAccountsClient function.
		// It creates a temporary client to ensure the database exists.
		logger.Logger.Info("Initializing accounts database connection pool",
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.InitAccountsPool"),
		)
		if err := ensureAccountsDBExists(); err != nil {
			initErr = fmt.Errorf("failed to ensure accounts database exists: %w", err)
			logger.Logger.Error("Accounts DB setup failed",
				zap.Time(logging.Created_at, time.Now()),
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
			DBUsername: config.DBUsername,
			DBPassword: config.DBPassword,
		}

		accountsPool = config.NewConnectionPool(poolCfg, logger, poolingConfig)
		accountsPool.Logger.Logger.Info("Accounts database connection pool initialized successfully.",
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.InitAccountsPool"),
		)
	})
	return initErr
}

// GetAccountsConnection retrieves a connection from the accounts database pool.
// Callers are responsible for returning the connection using PutAccountsConnection.
func GetAccountsConnection() (*config.PooledConnection, error) {
	if accountsPool == nil {
		return nil, errors.New("accounts connection pool is not initialized. Call InitAccountsPool first")
	}
	accountsPool.Logger.Logger.Info("Getting accounts connection: %s",
		zap.String(logging.Connection_database, config.AccountsDBName),
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.GetAccountsConnection"),
	)
	return accountsPool.Get()
}

// PutAccountsConnection returns a connection to the accounts database pool.
func PutAccountsConnection(conn *config.PooledConnection) {
	if accountsPool != nil {
		accountsPool.Logger.Logger.Info("Returning accounts connection: %s",
			zap.String(logging.Connection_database, config.AccountsDBName),
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.PutAccountsConnection"),
		)
		accountsPool.Put(conn)
	}
}

// ensureAccountsDBExists handles the one-time setup of the accounts database.
func ensureAccountsDBExists() error {
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

	// Configure the client to use TLS with our static certs
	opts := client.DefaultOptions().
		WithAddress(config.DBAddress).
		WithPort(config.DBPort).
		WithMTLs(true).
		WithMTLsOptions(
			client.MTLsOptions{}.
				WithCertificate(certFile).
				WithPkey(keyFile).
				WithClientCAs(caFile).
				WithServername(config.DBAddress),
		)

	c, err := client.NewImmuClient(opts)
	if err != nil {
		return fmt.Errorf("failed to create temporary client for DB setup: %w", err)
	}
	defer c.Disconnect()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Login with admin credentials
	lr, err := c.Login(ctx, []byte(config.DBUsername), []byte(config.DBPassword))
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
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.ensureAccountsDBExists"),
	)	

	// Create accounts database if it doesn't exist
	if !databaseExists {
		logger.Logger.Info("Creating accounts database", zap.String("database", config.AccountsDBName),
			zap.Time(logging.Created_at, time.Now()),
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
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.ensureAccountsDBExists"),
		)
	} else {
		logger.Logger.Info("Accounts database already exists",
			zap.Time(logging.Created_at, time.Now()),
			zap.String(logging.Log_file, LOG_FILE),
			zap.String(logging.Topic, TOPIC),
			zap.String(logging.Loki_url, LOKI_URL),
			zap.String(logging.Function, "DB_OPs.ensureAccountsDBExists"),
		)
	}

	logger.Logger.Info("Accounts database setup completed",
		zap.Time(logging.Created_at, time.Now()),
		zap.String(logging.Log_file, LOG_FILE),
		zap.String(logging.Topic, TOPIC),
		zap.String(logging.Loki_url, LOKI_URL),
		zap.String(logging.Function, "DB_OPs.ensureAccountsDBExists"),
	)
	return nil
}
