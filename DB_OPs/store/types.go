package store

import (
	"github.com/ethereum/go-ethereum/common"
)

// Account mirrors DB_OPs.Account exactly — same fields, same types.
// Defined here so store/ has zero dependency on the DB_OPs package.
type Account struct {
	// Legacy DID fields (for backward compatibility)
	DIDAddress string `json:"did,omitempty"`

	// New PublicKey based fields
	Address     common.Address `json:"address"` // Derived from PublicKey
	Balance     string         `json:"balance,omitempty"`
	Nonce       uint64         `json:"nonce"`
	TxNonce     uint64         `json:"tx_nonce"`      // Real Ethereum nonce
	TxCountSent uint64         `json:"tx_count_sent"` // Analytical tx send count

	// Account metadata
	AccountType string `json:"account_type"` // "did" or "publickey"
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`

	// Optional metadata
	Metadata map[string]any `json:"metadata,omitempty"`
}

// LogFilter specifies the criteria for retrieving event logs.
type LogFilter struct {
	FromBlock uint64
	ToBlock   uint64
	Addresses []common.Address
	Topics    [][]common.Hash
}
