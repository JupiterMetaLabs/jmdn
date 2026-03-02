package repository

import (
	"context"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/gETH/Facade/Service/Types"

	"github.com/ethereum/go-ethereum/common"
)

// AccountRepository defines the interface for interacting with Account data
type AccountRepository interface {
	StoreAccount(ctx context.Context, account *DB_OPs.Account) error
	GetAccount(ctx context.Context, address common.Address) (*DB_OPs.Account, error)
	GetAccountByDID(ctx context.Context, did string) (*DB_OPs.Account, error)
	UpdateAccountBalance(ctx context.Context, address common.Address, newBalance string) error
}

// BlockRepository defines the interface for interacting with Block and Log data
type BlockRepository interface {
	StoreZKBlock(ctx context.Context, block *config.ZKBlock) error
	GetZKBlockByNumber(ctx context.Context, number uint64) (*config.ZKBlock, error)
	GetZKBlockByHash(ctx context.Context, hash string) (*config.ZKBlock, error)
	GetLatestBlockNumber(ctx context.Context) (uint64, error)

	// Log related
	GetLogs(ctx context.Context, filterQuery Types.FilterQuery) ([]Types.Log, error)
}

// TransactionRepository defines the interface for interacting with Transaction data
type TransactionRepository interface {
	StoreTransaction(ctx context.Context, tx interface{}) error
	GetTransactionByHash(ctx context.Context, hash string) (*config.Transaction, error)
}

// CoordinatorRepository embeds all three to serve as a single injection point
type CoordinatorRepository interface {
	AccountRepository
	BlockRepository
	TransactionRepository
}
