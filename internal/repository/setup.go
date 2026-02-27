package repository

import (
	"context"
	"fmt"
	"gossipnode/config"
	GRO "gossipnode/config/GRO"
	"gossipnode/internal/repository/immu_repo"
	"gossipnode/internal/repository/kv_repo"
	"gossipnode/internal/repository/sql_repo"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
)

// RepositoryConfig holds the configuration for initializing all repositories.
type RepositoryConfig struct {
	// PostgreSQL pool config (nil = skip SQL repo)
	Postgres *config.PostgresPoolConfig

	// PebbleDB config (nil = skip KV repo)
	Pebble *config.PebbleConfig
}

// Repositories holds initialized database pools and the assembled MasterRepository.
type Repositories struct {
	Master       *MasterRepository
	PostgresPool *config.PostgresPool
	PebblePool   *config.PebblePool
	gro          interfaces.LocalGoroutineManagerInterface
}

// InitRepositories creates connection pools for PostgreSQL and PebbleDB,
// wraps them with their repository implementations, and assembles the
// MasterRepository coordinator.
//
// Usage at node startup:
//
//	repos, err := repository.InitRepositories(ctx, cfg)
//	if err != nil { log.Fatal(err) }
//	defer repos.Close()
//	DB_OPs.GlobalRepo = repos.Master
func InitRepositories(ctx context.Context, cfg RepositoryConfig) (*Repositories, error) {
	repos := &Repositories{}

	// --------------------------------------------
	// 0. Initialize GRO local manager for tracking coordinator goroutines
	// --------------------------------------------
	groLocal, err := GRO.GetApp(GRO.DB_OPsApp).NewLocalManager(GRO.DB_OPsCoordinatorLocal)
	if err != nil {
		return nil, fmt.Errorf("repository.Init: failed to create GRO local manager: %w", err)
	}
	repos.gro = groLocal

	// --------------------------------------------
	// 1. PostgreSQL
	// --------------------------------------------
	var sqlRepo CoordinatorRepository
	if cfg.Postgres != nil && cfg.Postgres.DSN != "" {
		pgPool, err := config.NewPostgresPool(ctx, cfg.Postgres)
		if err != nil {
			return nil, fmt.Errorf("repository.Init: %w", err)
		}
		repos.PostgresPool = pgPool
		sqlRepo = sql_repo.NewSQLRepository(pgPool.Pool)
	}

	// --------------------------------------------
	// 2. PebbleDB
	// --------------------------------------------
	var kvRepo CoordinatorRepository
	if cfg.Pebble != nil && cfg.Pebble.DataDir != "" {
		pebblePool, err := config.NewPebblePool(cfg.Pebble)
		if err != nil {
			// Close any previously opened resources
			repos.Close()
			return nil, fmt.Errorf("repository.Init: %w", err)
		}
		repos.PebblePool = pebblePool
		kvRepo = kv_repo.NewPebbleRepository(pebblePool.DB)
	}

	// --------------------------------------------
	// 3. ImmuDB (always present as fallback)
	// --------------------------------------------
	immuRepo := immu_repo.NewImmuRepository()

	// --------------------------------------------
	// Assemble the Coordinator
	// --------------------------------------------
	repos.Master = NewMasterRepository(sqlRepo, kvRepo, immuRepo, groLocal)

	return repos, nil
}

// Close gracefully shuts down all database connections.
func (r *Repositories) Close() {
	if r.PostgresPool != nil {
		r.PostgresPool.Close()
	}
	if r.PebblePool != nil {
		r.PebblePool.Close()
	}
}

// HealthCheck pings all active database connections.
func (r *Repositories) HealthCheck(ctx context.Context) error {
	if r.PostgresPool != nil {
		if err := r.PostgresPool.Ping(ctx); err != nil {
			return fmt.Errorf("postgres health check failed: %w", err)
		}
	}
	if r.PebblePool != nil {
		if err := r.PebblePool.Ping(); err != nil {
			return fmt.Errorf("pebble health check failed: %w", err)
		}
	}
	return nil
}
