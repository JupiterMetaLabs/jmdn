package router

import (

	// For compiler only now
	"gossipnode/SmartContract/internal/contract_registry"
	"gossipnode/SmartContract/internal/evm"
	"gossipnode/SmartContract/internal/state"
	"gossipnode/config"
	pb "gossipnode/gETH/proto"

	"github.com/ethereum/go-ethereum/core/vm"
)

// Router handles Smart Contract gRPC requests
type Router struct {
	executor          evm.Executor
	stateDB           state.StateDB
	contract_registry contract_registry.RegistryDB
	dbConn            *config.PooledConnection
	chainClient       pb.ChainClient
	chainID           int
}

// NewRouter creates a new Smart Contract Router
func NewRouter(chainID int, stateDB state.StateDB, reg contract_registry.RegistryDB, dbConn *config.PooledConnection, chainClient pb.ChainClient) *Router {
	return &Router{
		executor:          evm.NewEVMExecutor(chainID),
		stateDB:           stateDB,
		contract_registry: reg,
		dbConn:            dbConn,
		chainClient:       chainClient,
		chainID:           chainID,
	}
}

// Close cleans up resources
func (r *Router) Close() error {
	// If stateDB has a Close method, call it
	// If registry has a Close method, call it
	if r.contract_registry != nil {
		return r.contract_registry.Close()
	}
	return nil
}

// StateDB returns the underlying StateDB
func (r *Router) StateDB() vm.StateDB {
	return r.stateDB
}

// Registry returns the underlying RegistryDB
func (r *Router) Registry() contract_registry.RegistryDB {
	return r.contract_registry
}
