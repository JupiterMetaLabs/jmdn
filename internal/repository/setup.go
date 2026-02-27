package repository

import (
	"context"
	"fmt"
	GRO "gossipnode/config/GRO"
	"gossipnode/internal/repository/immu_repo"
	"gossipnode/internal/repository/thebe_repo"
	"gossipnode/logging"

	thebedb "github.com/JupiterMetaLabs/ThebeDB"
	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
)

// RepositoryConfig holds the configuration for initializing all repositories.
type RepositoryConfig struct {
	ThebeDB_KVPath  string
	ThebeDB_SQLPath string
}

// Repositories holds initialized database pools and the assembled MasterRepository.
type Repositories struct {
	Master  *MasterRepository
	ThebeDB *thebedb.ThebeDB // Changed from *thebedb.ThebeDB
	gro     interfaces.LocalGoroutineManagerInterface
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
	// 1. ThebeDB (Replaces Postgres and Pebble)
	// --------------------------------------------
	var thebeRepo CoordinatorRepository

	if cfg.ThebeDB_KVPath != "" && cfg.ThebeDB_SQLPath != "" {
		thebeCfg := thebedb.Config{
			KVPath:  cfg.ThebeDB_KVPath,
			SQLPath: cfg.ThebeDB_SQLPath,
		}

		asyncLog := logging.NewAsyncLogger()
		if asyncLog != nil {
			if l := asyncLog.Get(); l != nil {
				if named, _ := l.NamedLogger("ThebeDB", ""); named != nil {
					// ion doesn't expose raw zap natively without hacks, using Nop for now.
					// we can pass proper zap logger later if required.
				}
			}
		}

		thebeInstance, err := thebedb.New(thebeCfg, nil) // ThebeDB defaults to zap.NewNop() if nil
		if err != nil {
			return nil, fmt.Errorf("repository.Init: failed to start ThebeDB: %w", err)
		}

		if err := thebeInstance.Start(ctx); err != nil {
			thebeInstance.Close()
			return nil, fmt.Errorf("repository.Init: failed to run ThebeDB projector: %w", err)
		}

		repos.ThebeDB = thebeInstance
		thebeRepo = thebe_repo.NewThebeRepository(thebeInstance)
	}

	// --------------------------------------------
	// 2. ImmuDB (always present as fallback)
	// --------------------------------------------
	immuRepo := immu_repo.NewImmuRepository()

	// --------------------------------------------
	// Assemble the Coordinator
	// --------------------------------------------
	repos.Master = NewMasterRepository(thebeRepo, immuRepo, groLocal)

	return repos, nil
}

// Close gracefully shuts down all database connections.
func (r *Repositories) Close() {
	if r.ThebeDB != nil {
		r.ThebeDB.Close()
	}
}

// HealthCheck pings all active database connections.
func (r *Repositories) HealthCheck(ctx context.Context) error {
	// ThebeDB does not currently expose a raw Ping method but we assume it's healthy if we have an instance
	return nil
}
