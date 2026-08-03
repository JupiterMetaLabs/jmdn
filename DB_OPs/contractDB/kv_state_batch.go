package contractDB

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/DB_OPs/cassata"
)

// kvBatchOperation holds a single staged write for KVStateBatch.
type kvBatchOperation struct {
	fn func() error
}

// KVStateBatch implements StateBatch backed by direct BadgerDB PutDerived writes.
// Hot-state writes (code, storage, nonce, meta) go directly to BadgerDB via PutDerived.
// Receipt writes still go through cassata (SQL-indexed for block/log queries).
type KVStateBatch struct {
	repo *KVStateRepository
	ops  []kvBatchOperation
}

var _ StateBatch = (*KVStateBatch)(nil)

func (b *KVStateBatch) stage(fn func() error) {
	b.ops = append(b.ops, kvBatchOperation{fn: fn})
}

// Commit flushes all staged writes to BadgerDB in order.
func (b *KVStateBatch) Commit() error {
	for _, op := range b.ops {
		if err := op.fn(); err != nil {
			return fmt.Errorf("KVStateBatch.Commit: %w", err)
		}
	}
	return nil
}

func (b *KVStateBatch) Close() error {
	b.ops = nil
	return nil
}

// ── Write operations (stage for Commit) ──────────────────────────

func (b *KVStateBatch) SaveCode(addr common.Address, code []byte) error {
	key := kvKeyCode(addr)
	val := make([]byte, len(code))
	copy(val, code)
	b.stage(func() error {
		return b.repo.kv.PutDerived(key, val)
	})
	return nil
}

func (b *KVStateBatch) DeleteCode(addr common.Address) error {
	key := kvKeyCode(addr)
	b.stage(func() error {
		return b.repo.kv.PutDerived(key, kvTombstone)
	})
	return nil
}

func (b *KVStateBatch) SaveStorage(addr common.Address, key common.Hash, value common.Hash) error {
	kvKey := kvKeyStorage(addr, key)
	val := []byte(value.Hex())
	b.stage(func() error {
		return b.repo.kv.PutDerived(kvKey, val)
	})
	return nil
}

func (b *KVStateBatch) DeleteStorage(addr common.Address, key common.Hash) error {
	kvKey := kvKeyStorage(addr, key)
	b.stage(func() error {
		return b.repo.kv.PutDerived(kvKey, kvTombstone)
	})
	return nil
}

func (b *KVStateBatch) SaveStorageMetadata(addr common.Address, key common.Hash, meta StorageMetadata) error {
	kvKey := kvKeyStorageMeta(addr, key)
	val, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("KVStateBatch.SaveStorageMetadata marshal: %w", err)
	}
	valCopy := make([]byte, len(val))
	copy(valCopy, val)
	b.stage(func() error {
		return b.repo.kv.PutDerived(kvKey, valCopy)
	})
	return nil
}

func (b *KVStateBatch) DeleteStorageMetadata(addr common.Address, key common.Hash) error {
	kvKey := kvKeyStorageMeta(addr, key)
	b.stage(func() error {
		return b.repo.kv.PutDerived(kvKey, kvTombstone)
	})
	return nil
}

func (b *KVStateBatch) SaveNonce(addr common.Address, nonce uint64) error {
	key := kvKeyNonce(addr)
	val := []byte(strconv.FormatUint(nonce, 10))
	b.stage(func() error {
		return b.repo.kv.PutDerived(key, val)
	})
	return nil
}

func (b *KVStateBatch) DeleteNonce(addr common.Address) error {
	key := kvKeyNonce(addr)
	b.stage(func() error {
		return b.repo.kv.PutDerived(key, kvTombstone)
	})
	return nil
}

func (b *KVStateBatch) SaveContractMetadata(addr common.Address, data []byte) error {
	key := kvKeyMeta(addr)
	val := make([]byte, len(data))
	copy(val, data)
	b.stage(func() error {
		return b.repo.kv.PutDerived(key, val)
	})
	return nil
}

// SaveReceipt writes to SQL via cassata — receipts must be SQL-indexed for
// block-level queries, log filtering, and the contract_receipts table.
func (b *KVStateBatch) SaveReceipt(txHash common.Hash, data []byte) error {
	var receipt TransactionReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		receipt = TransactionReceipt{TxHash: txHash}
	}
	r := cassata.ContractReceiptResult{
		TxHash:          txHash.Hex(),
		BlockNumber:     receipt.BlockNumber,
		TxIndex:         receipt.TxIndex,
		Status:          int16(receipt.Status),
		GasUsed:         strconv.FormatUint(receipt.GasUsed, 10),
		ContractAddress: receipt.ContractAddress.Hex(),
		RevertReason:    receipt.RevertReason,
		Raw:             data,
	}
	b.stage(func() error {
		return b.repo.cas.IngestContractReceipt(context.Background(), r)
	})
	return nil
}
