package DB_OPs

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/DB_OPs/thebegateway"
)

// TxRef is a (block, hash) reference to a transaction involving an address.
type TxRef = thebegateway.TxRef

// CountTransactionsByAddress returns how many stored transactions name addr as
// sender or receiver. Served by the ThebeDB SQL projection (transactions
// table, from_addr/to_addr indexes); this replaced the retired SQLite
// tx-address index (DB_OPs/txindex).
func CountTransactionsByAddress(ctx context.Context, addr common.Address) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	h, err := getHandle(nil)
	if err != nil {
		return 0, fmt.Errorf("CountTransactionsByAddress: %w", err)
	}
	n, err := h.CountTransactionsByAddress(ctx, addr.Hex())
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// TransactionRefsByAddress returns one page of transaction references for
// addr, newest first (block_number DESC, tx_index DESC). offset/limit follow
// SQL semantics. Callers hydrate full transactions by hash.
func TransactionRefsByAddress(ctx context.Context, addr common.Address, offset, limit int) ([]TxRef, error) {
	if limit <= 0 {
		return []TxRef{}, nil
	}
	if offset < 0 {
		offset = 0
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	h, err := getHandle(nil)
	if err != nil {
		return nil, fmt.Errorf("TransactionRefsByAddress: %w", err)
	}
	return h.GetTransactionRefsByAddress(ctx, addr.Hex(), limit, offset)
}
