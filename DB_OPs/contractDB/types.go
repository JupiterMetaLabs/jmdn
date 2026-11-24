package contractDB

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

// ============================================================================
// Account State
// ============================================================================

// AccountData holds the pure data fields of an Ethereum account.
type AccountData struct {
	Nonce    uint64       // Transaction count for this account
	Balance  *uint256.Int // Account balance in wei
	Root     common.Hash  // Storage root (Merkle root of contract storage)
	CodeHash []byte       // Hash of the contract bytecode
}

// NewAccountData creates a new empty AccountData instance.
func NewAccountData() *AccountData {
	return &AccountData{
		Nonce:    0,
		Balance:  uint256.NewInt(0),
		Root:     common.Hash{},
		CodeHash: emptyCodeHash,
	}
}

// Copy creates a deep copy of AccountData.
func (a *AccountData) Copy() *AccountData {
	if a == nil {
		return NewAccountData()
	}
	return &AccountData{
		Nonce:    a.Nonce,
		Balance:  new(uint256.Int).Set(a.Balance),
		Root:     a.Root,
		CodeHash: append([]byte(nil), a.CodeHash...),
	}
}

// Empty returns true if the account has zero nonce, zero balance, and no code.
func (a *AccountData) Empty() bool {
	return a.Nonce == 0 && a.Balance.Sign() == 0 && len(a.CodeHash) == 0
}

// emptyCodeHash is the Keccak256 hash of empty bytecode.
var emptyCodeHash = []byte{
	0xc5, 0xd2, 0x46, 0x01, 0x86, 0xf7, 0x23, 0x3c,
	0x92, 0x7e, 0x7d, 0xb2, 0xdc, 0xc7, 0x03, 0xc0,
	0xe5, 0x00, 0xb6, 0x53, 0xca, 0x82, 0x27, 0x3b,
	0x7b, 0xfa, 0xd8, 0x04, 0x5d, 0x85, 0xa4, 0x70,
}

// ============================================================================
// Persistence Types
// ============================================================================

// ContractMetadata holds the immutable metadata of a deployed smart contract.
type ContractMetadata struct {
	ContractAddress  common.Address `json:"contract_address"`
	CodeHash         common.Hash    `json:"code_hash"`
	CodeSize         uint64         `json:"code_size"`
	DeployerAddress  common.Address `json:"deployer_address"`
	DeploymentTxHash common.Hash    `json:"deployment_tx_hash"`
	DeploymentBlock  uint64         `json:"deployment_block"`
	CreatedAt        int64          `json:"created_at"` // Unix timestamp
}

// TransactionReceipt holds the result of a transaction execution.
type TransactionReceipt struct {
	TxHash          common.Hash    `json:"tx_hash"`
	BlockNumber     uint64         `json:"block_number"`
	TxIndex         uint64         `json:"tx_index"`
	Status          uint64         `json:"status"` // 1 = Success, 0 = Fail
	GasUsed         uint64         `json:"gas_used"`
	ContractAddress common.Address `json:"contract_address,omitempty"` // For deployments
	Logs            []*types.Log   `json:"logs"`
	RevertReason    string         `json:"revert_reason,omitempty"`
	CreatedAt       int64          `json:"created_at"`
}

// StorageMetadata holds metadata for a specific contract storage slot update.
type StorageMetadata struct {
	ContractAddress   common.Address `json:"contract_address"`
	StorageKey        common.Hash    `json:"storage_key"`
	ValueHash         common.Hash    `json:"value_hash"`
	LastModifiedBlock uint64         `json:"last_modified_block"`
	LastModifiedTx    common.Hash    `json:"last_modified_tx"`
	UpdatedAt         int64          `json:"updated_at"`
}

// ============================================================================
// Key Prefix Constants
// ============================================================================

var (
	PrefixCode         = []byte("code:")
	PrefixStorage      = []byte("storage:")
	PrefixNonce        = []byte("nonce:")
	PrefixStorageMeta  = []byte("meta:storage:")
	PrefixContractMeta = []byte("meta:contract:")
	PrefixReceipt      = []byte("receipt:")
)
