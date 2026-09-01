package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// StoreBlock converts a config.ZKBlock to a thebegateway.BlockRecord and writes it.
// Time: O(1) — single gateway write.
func (b *thebeBackend) StoreBlock(ctx context.Context, block *config.ZKBlock) error {
	if block == nil {
		return fmt.Errorf("backend.StoreBlock: block is nil")
	}

	rec := toBlockRecord(block)
	if err := b.gw.WriteBlock(ctx, rec); err != nil {
		return fmt.Errorf("backend.StoreBlock(%d): %w", block.BlockNumber, err)
	}
	return nil
}

// GetBlock retrieves a block by block number.
// Time: O(1) — cache-through PK lookup.
func (b *thebeBackend) GetBlock(ctx context.Context, blockNumber uint64) (*thebegateway.BlockRecord, error) {
	rec, err := b.r.GetBlock(ctx, blockNumber)
	if err != nil {
		return nil, fmt.Errorf("backend.GetBlock(%d): %w", blockNumber, err)
	}
	return rec, nil
}

// GetBlockByHash retrieves a block by its hash.
// Delegates to ThebeReader.GetBlockByHash (Phase 2.0 — hash-indexed SQL query).
// Time: O(1) — cache-through hash-index lookup.
func (b *thebeBackend) GetBlockByHash(ctx context.Context, hash string) (*thebegateway.BlockRecord, error) {
	rec, err := b.r.GetBlockByHash(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("backend.GetBlockByHash(%q): %w", hash, err)
	}
	return rec, nil
}

// GetLatestBlockNumber returns the block number of the most recently stored block.
// Delegates to ThebeReader.GetLatestBlock and extracts BlockNumber.
// Time: O(1) — cache-through query.
func (b *thebeBackend) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
	rec, err := b.r.GetLatestBlock(ctx)
	if err != nil {
		return 0, fmt.Errorf("backend.GetLatestBlockNumber: %w", err)
	}
	return rec.BlockNumber, nil
}

// BulkGetBlocks retrieves all blocks in [from, to] inclusive via a single SQL range query.
// Delegates to ThebeReader.BulkGetBlocks (Phase 2.0).
// Time: O(n) where n = to-from+1 — single SQL scan, not n individual lookups.
func (b *thebeBackend) BulkGetBlocks(ctx context.Context, from, to uint64) ([]*thebegateway.BlockRecord, error) {
	if from > to {
		return nil, fmt.Errorf("backend.BulkGetBlocks: from(%d) > to(%d)", from, to)
	}
	recs, err := b.r.BulkGetBlocks(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("backend.BulkGetBlocks(%d,%d): %w", from, to, err)
	}
	return recs, nil
}

// toBlockRecord converts a config.ZKBlock to thebegateway.BlockRecord.
// Field-by-field mapping documented inline.
func toBlockRecord(b *config.ZKBlock) *thebegateway.BlockRecord {
	rec := &thebegateway.BlockRecord{
		BlockNumber: b.BlockNumber,                   // direct field
		BlockHash:   b.BlockHash.Hex(),               // common.Hash → 0x-prefixed hex string
		ParentHash:  b.PrevHash.Hex(),                // ZKBlock.PrevHash → BlockRecord.ParentHash
		Timestamp:   time.Unix(b.Timestamp, 0).UTC(), // int64 epoch-seconds → time.Time
		TxsRoot:     b.TxnsRoot,                      // ZKBlock.TxnsRoot → BlockRecord.TxsRoot
		StateRoot:   b.StateRoot.Hex(),               // common.Hash → hex string
		LogsBloom:   b.LogsBloom,                     // []byte → []byte direct
		GasLimit:    b.GasLimit,                      // uint64 direct
		GasUsed:     b.GasUsed,                       // uint64 direct
	}

	// CoinbaseAddr: nil pointer → empty string; present → 0x-prefixed hex
	if b.CoinbaseAddr != nil {
		rec.CoinbaseAddr = b.CoinbaseAddr.Hex()
	}

	// ZKVMAddr: nil pointer → empty string; present → 0x-prefixed hex
	if b.ZKVMAddr != nil {
		rec.ZKVMAddr = b.ZKVMAddr.Hex()
	}

	// ExtraData: ZKBlock.ExtraData is a raw string; store as map for JSONB
	if b.ExtraData != "" {
		rec.ExtraData = map[string]any{"raw": b.ExtraData}
	}

	// CommitteeCertificate: persist the verified committee vote set (JSON) so it
	// survives past the ephemeral gossip envelope and is re-verifiable on sync
	// (P-cert / ThebeSync). Stashed in ExtraData JSONB; applyBlock marshals the
	// whole map, so any key here round-trips. This is the FIRST block write in
	// StoreZKBlock (before toBlockRecordWithZK), and blocks are append-only with
	// ON CONFLICT DO NOTHING, so the cert must be set here to win the projection.
	if b.CommitteeCertificate != "" {
		if rec.ExtraData == nil {
			rec.ExtraData = map[string]any{}
		}
		rec.ExtraData["committee_certificate"] = b.CommitteeCertificate
	}

	// StateFingerprint: persist the P2.5 post-apply state fingerprint (stamped by
	// ProcessBlockTransactions before store) so a block served on the sync path
	// (ThebeSync) still carries it — the receiver's ProcessBlockTransactions then
	// COMPARES (and halts on divergence) instead of re-stamping. Without this the
	// P2.5 gate silently no-ops on sync. Same ExtraData round-trip as the cert.
	if b.StateFingerprint != "" {
		if rec.ExtraData == nil {
			rec.ExtraData = map[string]any{}
		}
		rec.ExtraData["state_fingerprint"] = b.StateFingerprint
	}

	// AccountNonces: persist the canonical per-account ART identities the sequencer
	// stamped (DB_OPs.EnrichBlockAccountNonces) so a block served on the sync path
	// (ThebeSync) carries them and the receiver's ProcessBlockTransactions creates
	// new accounts with the IDENTICAL identity — matching the gossip path, which
	// reads the in-memory carried value. Without this, catch-up ships
	// AccountNonces=nil and apply fails on the first new-account (contract-deploy)
	// block. JSON string in ExtraData; same round-trip as the cert/fingerprint.
	if len(b.AccountNonces) > 0 {
		if rec.ExtraData == nil {
			rec.ExtraData = map[string]any{}
		}
		if raw, err := json.Marshal(b.AccountNonces); err == nil {
			rec.ExtraData["account_nonces"] = string(raw)
		}
	}
	// FeeRecipients: persist the FROZEN buddy-reward split (address+weight) so a
	// block served on the sync path (ThebeSync) or re-read after a restart carries
	// the IDENTICAL split the sequencer computed. Without this, a stored/served
	// block comes back with empty FeeRecipients and a syncing node applies NO fee
	// credits — diverging balances from nodes that applied the split live. The
	// weight is frozen here and NEVER recomputed from live balances, so sync is
	// deterministic regardless of when it runs. JSON string in ExtraData; same
	// round-trip as account_nonces. See docs/STAKING-REWARDS-DESIGN.md.
	if len(b.FeeRecipients) > 0 {
		if rec.ExtraData == nil {
			rec.ExtraData = map[string]any{}
		}
		if raw, err := json.Marshal(b.FeeRecipients); err == nil {
			rec.ExtraData["fee_recipients"] = string(raw)
		}
	}
	// ConsensusHash: persist the consensus-fields digest so a synced/restarted
	// node carries the SAME value the sequencer stamped and the committee's v4
	// certificate signed — checkConsensusBinding recomputes and matches it on the
	// receive/apply path. Absent on pre-v4 blocks. Hex string in ExtraData, same
	// round-trip as fee_recipients.
	if (b.ConsensusHash != common.Hash{}) {
		if rec.ExtraData == nil {
			rec.ExtraData = map[string]any{}
		}
		rec.ExtraData["consensus_hash"] = b.ConsensusHash.Hex()
	}
	// Slot/Period: persisted so a restarted node can recover its slot counter
	// from the tip block instead of resetting to 0 - see
	// messaging.SlotStore.SeedFromCommittedTip and
	// docs/COMMITTEE-SNAPSHOT-FREEZE-TODO.md item 8. Written unconditionally,
	// including when both are legitimately 0 (genesis / first commit) - a
	// value must always be present for the reseed to distinguish "no data
	// persisted yet" from "this block's slot really is 0".
	if rec.ExtraData == nil {
		rec.ExtraData = map[string]any{}
	}
	rec.ExtraData["slot"] = b.Slot
	rec.ExtraData["period"] = b.Period

	// The remaining AVC consensus fields, added 2026-08-26. Slot/Period were
	// persisted first because the slot-clock recovery needed them; the other
	// six were left behind, and that gap turned out to be load-bearing in
	// three separate places:
	//
	//   - PrevAggCert is blocker B1's own field. It rides the wire correctly
	//     and every node re-verifies it live, but dropping it here means a
	//     node replaying its own history — or fast-syncing from a peer, which
	//     serves from these records — cannot reconstruct the fallback seed for
	//     any epoch that fell back. "Persisted aggSig" was only ever true of
	//     the in-memory path.
	//   - VdfProof exists so a node that hasn't finished its own VDF
	//     evaluation can take the millisecond verify path instead of the
	//     ~20-minute one. Dropped here, a rejoining node always pays the full
	//     evaluation, which is the exact cost the field was added to avoid.
	//   - All six are covered by the M2b block hash
	//     (Security.RecomputeBlockHashWithConsensusFields). A record that
	//     loses them cannot be used to re-derive its own block's hash.
	//
	// Written unconditionally, nil and zero included, for the same reason
	// slot/period are: a reader must be able to tell "this block genuinely
	// carried no reveals" from "this record predates the fix". For the fold
	// window that distinction is the difference between a real gap and an
	// artifact, and a fold cannot be correct without it.
	//
	// Keys match each field's own JSON tag on config.ZKBlock, so the stored
	// JSONB reads the same as the wire format.
	rec.ExtraData["randao_reveals"] = b.RandaoReveals
	rec.ExtraData["vdf_proof"] = b.VdfProof
	rec.ExtraData["seed_epoch"] = b.SeedEpoch
	rec.ExtraData["voting_snapshot_epoch"] = b.VotingSnapshotEpoch
	rec.ExtraData["prev_agg_cert"] = b.PrevAggCert
	rec.ExtraData["committee_snapshot_hash"] = b.CommitteeSnapshotHash

	return rec
}

// toBlockRecordWithZK extends toBlockRecord with ZK proof fields for StoreZKBlock.
// Returns a BlockRecord with ZK status embedded in ExtraData.
func toBlockRecordWithZK(b *config.ZKBlock) *thebegateway.BlockRecord {
	rec := toBlockRecord(b)
	// Embed ZK fields in ExtraData since BlockRecord has no dedicated ZK columns.
	if rec.ExtraData == nil {
		rec.ExtraData = map[string]any{}
	}
	rec.ExtraData["proof_hash"] = b.ProofHash
	rec.ExtraData["zk_status"] = b.Status
	return rec
}

// toZKProofRecord converts a config.ZKBlock to thebegateway.ZKProofRecord.
func toZKProofRecord(b *config.ZKBlock) *thebegateway.ZKProofRecord {
	// Commitment: []uint32 → []byte (big-endian uint32 packing)
	commitment := commitmentToBytes(b.Commitment)
	return &thebegateway.ZKProofRecord{
		BlockNumber: b.BlockNumber,
		ProofHash:   b.ProofHash,
		StarkProof:  b.StarkProof,
		Commitment:  commitment,
	}
}

// commitmentToBytes packs []uint32 commitment into []byte (4 bytes per element, big-endian).
// Time: O(n) where n = len(commitment).
func commitmentToBytes(c []uint32) []byte {
	if len(c) == 0 {
		return nil
	}
	out := make([]byte, len(c)*4)
	for i, v := range c {
		out[i*4] = byte(v >> 24)
		out[i*4+1] = byte(v >> 16)
		out[i*4+2] = byte(v >> 8)
		out[i*4+3] = byte(v)
	}
	return out
}

// StoreL1Finality appends an L1 finality record through the gateway
// (canonical log → projector → l1_finality table).
func (b *thebeBackend) StoreL1Finality(ctx context.Context, rec *thebegateway.L1FinalityRecord) error {
	if rec == nil || rec.Confirmation == "" {
		return fmt.Errorf("backend.StoreL1Finality: nil or empty confirmation")
	}
	if err := b.gw.WriteL1Finality(ctx, rec); err != nil {
		return fmt.Errorf("backend.StoreL1Finality(%s): %w", rec.Confirmation, err)
	}
	return nil
}

// GetL1FinalityForBlock returns the latest L1 commit covering blockNumber.
func (b *thebeBackend) GetL1FinalityForBlock(ctx context.Context, blockNumber uint64) (*thebegateway.L1FinalityRecord, error) {
	rec, err := b.r.GetL1FinalityForBlock(ctx, blockNumber)
	if err != nil {
		return nil, fmt.Errorf("backend.GetL1FinalityForBlock(%d): %w", blockNumber, err)
	}
	return rec, nil
}

// GetBlocksByRewardAddress returns blocks where address is coinbase or ZKVM.
// Used by historical balance reconstruction.
func (b *thebeBackend) GetBlocksByRewardAddress(ctx context.Context, address string, fromBlock, toBlock uint64) ([]*thebegateway.BlockRecord, error) {
	recs, err := b.r.GetBlocksByRewardAddress(ctx, address, fromBlock, toBlock)
	if err != nil {
		return nil, fmt.Errorf("backend.GetBlocksByRewardAddress(%s, %d, %d): %w", address, fromBlock, toBlock, err)
	}
	return recs, nil
}

// GetBlocksByFeeRecipient returns blocks whose buddy staking-reward split credits
// the address (extra_data.fee_recipients). Buddy-earnings report; distinct from
// GetBlocksByRewardAddress (coinbase/zkvm).
func (b *thebeBackend) GetBlocksByFeeRecipient(ctx context.Context, address string, fromBlock, toBlock uint64) ([]*thebegateway.BlockRecord, error) {
	recs, err := b.r.GetBlocksByFeeRecipient(ctx, address, fromBlock, toBlock)
	if err != nil {
		return nil, fmt.Errorf("backend.GetBlocksByFeeRecipient(%s, %d, %d): %w", address, fromBlock, toBlock, err)
	}
	return recs, nil
}

// PutSyncKV / GetSyncKV expose the gateway's durable sync-state KV
// (tx markers, applied anchor, latest_block marker — F-train modules).
func (b *thebeBackend) PutSyncKV(key string, value []byte) error {
	return b.gw.PutSyncKV(key, value)
}

func (b *thebeBackend) GetSyncKV(key string) ([]byte, error) {
	return b.gw.GetSyncKV(key)
}
