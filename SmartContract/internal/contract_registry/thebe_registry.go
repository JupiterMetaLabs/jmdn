package contract_registry

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/DB_OPs/cassata"
	"gossipnode/SmartContract/pkg/types"
)

// ThebeRegistryDB implements RegistryDB using ThebeDB via cassata.
// Writes go through cassata.IngestContractRegistry → ThebeDB.Append → projector → contracts SQL table.
// Reads query the contracts SQL table directly via cassata.
type ThebeRegistryDB struct {
	cas *cassata.Cassata
}

var _ RegistryDB = (*ThebeRegistryDB)(nil)

// NewThebeRegistryDB creates a RegistryDB backed by ThebeDB.
func NewThebeRegistryDB(cas *cassata.Cassata) *ThebeRegistryDB {
	return &ThebeRegistryDB{cas: cas}
}

func (db *ThebeRegistryDB) RegisterContract(ctx context.Context, meta *types.ContractMetadata) error {
	if meta == nil {
		return fmt.Errorf("ThebeRegistryDB.RegisterContract: nil metadata")
	}
	r := cassata.ContractRegistryResult{
		Address:      meta.Address.Hex(),
		Deployer:     meta.Deployer.Hex(),
		Name:         meta.Name,
		ABI:          meta.ABI,
		BytecodeHash: meta.BytecodeHash.Hex(),
		DeployBlock:  meta.DeployBlock,
		DeployTime:   meta.DeployTime,
		DeployTxHash: meta.DeployTxHash.Hex(),
		CodeSize:     meta.CodeSize,
		ContractType: meta.ContractType,
		State:        meta.State,
	}
	if r.State == "" {
		r.State = "active"
	}
	if r.ContractType == "" {
		r.ContractType = "custom"
	}
	return db.cas.IngestContractRegistry(ctx, r)
}

func (db *ThebeRegistryDB) GetContract(ctx context.Context, address common.Address) (*types.ContractMetadata, error) {
	r, err := db.cas.GetContractFromRegistry(ctx, address.Hex())
	if err != nil {
		if isRegistryNotFound(err) {
			return nil, fmt.Errorf("contract not found: %s", address.Hex())
		}
		return nil, fmt.Errorf("ThebeRegistryDB.GetContract: %w", err)
	}
	return toContractMetadata(r), nil
}

func (db *ThebeRegistryDB) ListContracts(ctx context.Context, opts *ListOptions) ([]*types.ContractMetadata, error) {
	copts := cassata.ListContractOptions{}
	if opts != nil {
		copts.Deployer = opts.Deployer.Hex()
		if opts.Deployer == (common.Address{}) {
			copts.Deployer = ""
		}
		copts.FromBlock = opts.FromBlock
		copts.ToBlock = opts.ToBlock
		copts.FromTime = opts.FromTime
		copts.ToTime = opts.ToTime
		copts.Limit = opts.Limit
		copts.Offset = opts.Offset
	}
	rows, err := db.cas.ListContractsFromRegistry(ctx, copts)
	if err != nil {
		return nil, fmt.Errorf("ThebeRegistryDB.ListContracts: %w", err)
	}
	out := make([]*types.ContractMetadata, 0, len(rows))
	for i := range rows {
		out = append(out, toContractMetadata(&rows[i]))
	}
	return out, nil
}

func (db *ThebeRegistryDB) ContractExists(ctx context.Context, address common.Address) (bool, error) {
	_, err := db.cas.GetContractFromRegistry(ctx, address.Hex())
	if err != nil {
		if isRegistryNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("ThebeRegistryDB.ContractExists: %w", err)
	}
	return true, nil
}

func (db *ThebeRegistryDB) GetTotalCount(ctx context.Context) (uint64, error) {
	n, err := db.cas.CountContracts(ctx)
	if err != nil {
		return 0, fmt.Errorf("ThebeRegistryDB.GetTotalCount: %w", err)
	}
	return n, nil
}

func (db *ThebeRegistryDB) Close() error { return nil }

// ── helpers ──────────────────────────────────────────────────────

func toContractMetadata(r *cassata.ContractRegistryResult) *types.ContractMetadata {
	return &types.ContractMetadata{
		Address:      common.HexToAddress(r.Address),
		Deployer:     common.HexToAddress(r.Deployer),
		Name:         r.Name,
		ABI:          r.ABI,
		BytecodeHash: common.HexToHash(r.BytecodeHash),
		DeployBlock:  r.DeployBlock,
		DeployTime:   r.DeployTime,
		DeployTxHash: common.HexToHash(r.DeployTxHash),
		CodeSize:     r.CodeSize,
		ContractType: r.ContractType,
		State:        r.State,
	}
}

func isRegistryNotFound(err error) bool {
	if err == nil {
		return false
	}
	if err == sql.ErrNoRows {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no rows") || strings.Contains(msg, "not found")
}
