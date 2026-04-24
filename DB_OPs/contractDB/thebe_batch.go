package contractDB

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"gossipnode/DB_OPs/cassata"
)

// batchOperation holds a single staged write for ThebeBatch.
type batchOperation struct {
	fn func(ctx context.Context) error
}

// ThebeBatch implements StateBatch backed by ThebeDB.
// All writes are staged in memory and flushed sequentially on Commit().
// Each staged write calls the appropriate cassata Ingest* method.
type ThebeBatch struct {
	repo *ThebeStateRepository
	ops  []batchOperation
}

var _ StateBatch = (*ThebeBatch)(nil)

func (b *ThebeBatch) stage(fn func(ctx context.Context) error) {
	b.ops = append(b.ops, batchOperation{fn: fn})
}

// Commit flushes all staged writes to ThebeDB in order.
// If any write fails, the remaining writes are NOT attempted — the
// caller (ContractDB.CommitToDB) must handle the error.
func (b *ThebeBatch) Commit() error {
	ctx := context.Background()
	for _, op := range b.ops {
		if err := op.fn(ctx); err != nil {
			return fmt.Errorf("ThebeBatch.Commit: %w", err)
		}
	}
	return nil
}

func (b *ThebeBatch) Close() error {
	b.ops = nil
	return nil
}

// ── Write operations (stage for Commit) ──────────────────────────

func (b *ThebeBatch) SaveCode(addr common.Address, code []byte) error {
	codeHash := crypto.Keccak256Hash(code).Hex()
	r := cassata.ContractCodeResult{
		Address:  addr.Hex(),
		Code:     code,
		CodeHash: codeHash,
	}
	b.stage(func(ctx context.Context) error {
		return b.repo.cas.IngestContractCode(ctx, r)
	})
	return nil
}

func (b *ThebeBatch) DeleteCode(addr common.Address) error {
	r := cassata.ContractCodeResult{
		Address:  addr.Hex(),
		Code:     []byte{},
		CodeHash: zeroHashHex,
	}
	b.stage(func(ctx context.Context) error {
		return b.repo.cas.IngestContractCode(ctx, r)
	})
	return nil
}

func (b *ThebeBatch) SaveStorage(addr common.Address, key common.Hash, value common.Hash) error {
	r := cassata.ContractStorageResult{
		Address:   addr.Hex(),
		SlotHash:  key.Hex(),
		ValueHash: value.Hex(),
	}
	b.stage(func(ctx context.Context) error {
		return b.repo.cas.IngestContractStorage(ctx, r)
	})
	return nil
}

func (b *ThebeBatch) DeleteStorage(addr common.Address, key common.Hash) error {
	r := cassata.ContractStorageResult{
		Address:   addr.Hex(),
		SlotHash:  key.Hex(),
		ValueHash: zeroHashHex,
	}
	b.stage(func(ctx context.Context) error {
		return b.repo.cas.IngestContractStorage(ctx, r)
	})
	return nil
}

func (b *ThebeBatch) SaveStorageMetadata(addr common.Address, key common.Hash, meta StorageMetadata) error {
	r := cassata.ContractStorageMetaResult{
		Address:           addr.Hex(),
		SlotHash:          key.Hex(),
		ValueHash:         meta.ValueHash.Hex(),
		LastModifiedBlock: meta.LastModifiedBlock,
		LastModifiedTx:    meta.LastModifiedTx.Hex(),
	}
	b.stage(func(ctx context.Context) error {
		return b.repo.cas.IngestContractStorageMeta(ctx, r)
	})
	return nil
}

func (b *ThebeBatch) DeleteStorageMetadata(addr common.Address, key common.Hash) error {
	r := cassata.ContractStorageMetaResult{
		Address:           addr.Hex(),
		SlotHash:          key.Hex(),
		ValueHash:         zeroHashHex,
		LastModifiedBlock: 0,
		LastModifiedTx:    zeroHashHex,
	}
	b.stage(func(ctx context.Context) error {
		return b.repo.cas.IngestContractStorageMeta(ctx, r)
	})
	return nil
}

func (b *ThebeBatch) SaveNonce(addr common.Address, nonce uint64) error {
	r := cassata.ContractNonceResult{
		Address: addr.Hex(),
		Nonce:   strconv.FormatUint(nonce, 10),
	}
	b.stage(func(ctx context.Context) error {
		return b.repo.cas.IngestContractNonce(ctx, r)
	})
	return nil
}

func (b *ThebeBatch) DeleteNonce(addr common.Address) error {
	r := cassata.ContractNonceResult{
		Address: addr.Hex(),
		Nonce:   "0",
	}
	b.stage(func(ctx context.Context) error {
		return b.repo.cas.IngestContractNonce(ctx, r)
	})
	return nil
}

func (b *ThebeBatch) SaveContractMetadata(addr common.Address, data []byte) error {
	var meta ContractMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		meta = ContractMetadata{ContractAddress: addr}
	}
	r := cassata.ContractMetaResult{
		Address:         addr.Hex(),
		CodeHash:        meta.CodeHash.Hex(),
		CodeSize:        meta.CodeSize,
		DeployerAddress: meta.DeployerAddress.Hex(),
		DeploymentTx:    meta.DeploymentTxHash.Hex(),
		DeploymentBlock: meta.DeploymentBlock,
		Raw:             data,
	}
	b.stage(func(ctx context.Context) error {
		return b.repo.cas.IngestContractMeta(ctx, r)
	})
	return nil
}

func (b *ThebeBatch) SaveReceipt(txHash common.Hash, data []byte) error {
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
	b.stage(func(ctx context.Context) error {
		return b.repo.cas.IngestContractReceipt(ctx, r)
	})
	return nil
}
