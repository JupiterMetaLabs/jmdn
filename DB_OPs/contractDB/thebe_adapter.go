package contractDB

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"

	"gossipnode/DB_OPs/cassata"
)

// zeroHashHex is the canonical EVM zero storage value (32 zero bytes).
var zeroHashHex = common.Hash{}.Hex()

// ThebeStateRepository implements StateRepository using cassata.Cassata
// as the backing store. It replaces PebbleAdapter for new deployments.
type ThebeStateRepository struct {
	cas *cassata.Cassata
}

// Ensure ThebeStateRepository satisfies StateRepository at compile time.
var _ StateRepository = (*ThebeStateRepository)(nil)

// NewThebeStateRepository creates a StateRepository backed by ThebeDB.
func NewThebeStateRepository(cas *cassata.Cassata) *ThebeStateRepository {
	return &ThebeStateRepository{cas: cas}
}

// GetBalance is a stub — balances are owned by the DID service, not ThebeDB.
func (r *ThebeStateRepository) GetBalance(ctx context.Context, addr common.Address) (*uint256.Int, error) {
	return nil, nil
}

// ── Read operations ───────────────────────────────────────────────

func (r *ThebeStateRepository) GetCode(ctx context.Context, addr common.Address) ([]byte, error) {
	res, err := r.cas.GetContractCode(ctx, addr.Hex())
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("ThebeStateRepository.GetCode: %w", err)
	}
	if len(res.Code) == 0 {
		return nil, nil
	}
	return res.Code, nil
}

func (r *ThebeStateRepository) GetStorage(ctx context.Context, addr common.Address, key common.Hash) (common.Hash, error) {
	res, err := r.cas.GetContractStorage(ctx, addr.Hex(), key.Hex())
	if err != nil {
		if isNotFound(err) {
			return common.Hash{}, nil
		}
		return common.Hash{}, fmt.Errorf("ThebeStateRepository.GetStorage: %w", err)
	}
	return common.HexToHash(res.ValueHash), nil
}

func (r *ThebeStateRepository) GetStorageMetadata(ctx context.Context, addr common.Address, key common.Hash) (*StorageMetadata, error) {
	res, err := r.cas.GetContractStorageMeta(ctx, addr.Hex(), key.Hex())
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("ThebeStateRepository.GetStorageMetadata: %w", err)
	}
	if strings.EqualFold(res.ValueHash, zeroHashHex) {
		return nil, nil
	}
	return &StorageMetadata{
		ContractAddress:   addr,
		StorageKey:        key,
		ValueHash:         common.HexToHash(res.ValueHash),
		LastModifiedBlock: res.LastModifiedBlock,
		LastModifiedTx:    common.HexToHash(res.LastModifiedTx),
		UpdatedAt:         res.UpdatedAt.Unix(),
	}, nil
}

func (r *ThebeStateRepository) GetNonce(ctx context.Context, addr common.Address) (uint64, error) {
	res, err := r.cas.GetContractNonce(ctx, addr.Hex())
	if err != nil {
		if isNotFound(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("ThebeStateRepository.GetNonce: %w", err)
	}
	n, err := strconv.ParseUint(res.Nonce, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("ThebeStateRepository.GetNonce parse: %w", err)
	}
	return n, nil
}

func (r *ThebeStateRepository) GetContractMetadata(ctx context.Context, addr common.Address) ([]byte, error) {
	res, err := r.cas.GetContractMeta(ctx, addr.Hex())
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("ThebeStateRepository.GetContractMetadata: %w", err)
	}
	if len(res.Raw) == 0 || string(res.Raw) == "{}" {
		return nil, nil
	}
	return res.Raw, nil
}

func (r *ThebeStateRepository) GetReceipt(ctx context.Context, txHash common.Hash) ([]byte, error) {
	res, err := r.cas.GetContractReceipt(ctx, txHash.Hex())
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("ThebeStateRepository.GetReceipt: %w", err)
	}
	return res.Raw, nil
}

// ── Batch ─────────────────────────────────────────────────────────

func (r *ThebeStateRepository) NewBatch() StateBatch {
	return &ThebeBatch{repo: r, ops: nil}
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no rows") || strings.Contains(msg, "not found")
}
