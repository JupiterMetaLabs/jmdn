package config

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// ZKBlockTransaction represents a single transaction in a ZK block
type Transaction struct {
	Hash      common.Hash     `json:"hash"`               // 0x-prefixed 32-byte
	From      *common.Address `json:"from"`               // 0x-prefixed 20-byte
	To        *common.Address `json:"to,omitempty"`       // nil => contract creation
	Value     *big.Int        `json:"value"`              // big.Int as hex
	Type      uint8           `json:"type"`               // 0x0=Legacy, 0x1=AccessList, 0x2=DynamicFee
	Timestamp uint64          `json:"timestamp"`          // seconds since epoch (if you keep it)
	ChainID   *big.Int        `json:"chain_id,omitempty"` // present for 2930/1559 (and signed legacy w/155)
	Nonce     uint64          `json:"nonce"`
	GasLimit  uint64          `json:"gas_limit"` //TODO: Make it big int

	// Fee fields (use one set depending on Type)
	GasPrice       *big.Int `json:"gas_price,omitempty"`        // Legacy/EIP-2930
	MaxFee         *big.Int `json:"max_fee,omitempty"`          // 1559: maxFeePerGas
	MaxPriorityFee *big.Int `json:"max_priority_fee,omitempty"` // 1559: maxPriorityFeePerGas

	Data       []byte     `json:"data,omitempty"` // input
	AccessList AccessList `json:"access_list,omitempty"`

	// Signature (present once signed)
	V *big.Int `json:"v,omitempty"`
	R *big.Int `json:"r,omitempty"`
	S *big.Int `json:"s,omitempty"`
}

// ZKBlock represents a block processed by the ZKVM with proof
type ZKBlock struct {
	// ZK-Stark proof data
	StarkProof []byte   `json:"starkproof"`
	Commitment []uint32 `json:"commitment"`
	ProofHash  string   `json:"proof_hash"`
	Status     string   `json:"status"`
	TxnsRoot   string   `json:"txnsroot"`

	// Block data
	Transactions []Transaction   `json:"transactions"`
	Timestamp    int64           `json:"timestamp"`
	ExtraData    string          `json:"extradata"`
	StateRoot    common.Hash     `json:"stateroot"`
	LogsBloom    []byte          `json:"logsbloom"`
	CoinbaseAddr *common.Address `json:"coinbaseaddr"`
	ZKVMAddr     *common.Address `json:"zkvmaddr"`
	// FeeRecipients, when non-empty, distributes the coinbase-side gas-fee share
	// across these weighted addresses instead of the single CoinbaseAddr (which
	// remains the L1-paying wallet). Empty/omitted preserves the single-coinbase
	// behavior. Distribution is computed by config.SplitFee.
	FeeRecipients []FeeRecipient `json:"feerecipients,omitempty"`
	PrevHash      common.Hash    `json:"prevhash"`
	BlockHash     common.Hash    `json:"blockhash"`
	GasLimit      uint64         `json:"gaslimit"`
	GasUsed       uint64         `json:"gasused"`
	BlockNumber   uint64         `json:"blocknumber"`

	// L1 finality — set after commitRollup is mined on Ethereum.
	// Hydrated at read time from the append-only l1_finality table.
	L1TxHash      string `json:"l1_tx_hash,omitempty"`
	L1BlockNumber uint64 `json:"l1_block_number,omitempty"`

	// AccountNonces carries the canonical ART identity nonce for every distinct
	// sender and receiver this block touches, stamped by the sequencer before
	// consensus (DB_OPs.EnrichBlockAccountNonces). At apply, every node uses it to
	// (a) create accounts the block itself funds — deterministically, so all nodes
	// mint the identical identity — and (b) adopt the sequencer's nonce when a
	// stored account carries a different one (heals historical per-node mints).
	//
	// ADVISORY, NOT CONSENSUS-HASHED: the canonical block hash is computed from
	// transaction contents only (Security.RecomputeBlockHashFromContents), so this
	// field does not change BlockHash, and nodes on older builds simply ignore it
	// on JSON unmarshal — mixed fleets stay wire-compatible. It must therefore
	// never be treated as certificate-verified data: it is identity metadata whose
	// safety comes from the uniqueness/monotonicity rules in DB_OPs/art_ordinal.go.
	AccountNonces []AccountNonce `json:"account_nonces,omitempty"`

	// StateFingerprint is the canonical post-apply account-state fingerprint
	// (consensushash.StateFingerprintV1, hex) computed after this block is applied
	// — audit P2.5. The producer stamps it; every receiver recomputes after apply
	// and HALTS on mismatch instead of serving a divergent ledger (the reproduced
	// live=1000 vs synced=2000 class). ADVISORY, NOT CONSENSUS-HASHED (same as
	// AccountNonces — canonical BlockHash is tx-contents only), so it is
	// wire-compatible with older builds and unstamped blocks skip the check. Full
	// cryptographic binding (committee-signed) arrives with the CON-02 v3 block
	// hash; until then its trust rests on the single honest sequencer.
	StateFingerprint string `json:"state_fingerprint,omitempty"`

	// CommitteeCertificate is the JSON-encoded committee vote set
	// ([]BLS_Signer.BLSresponse) that certified this block — the 2f+1 block-bound
	// signatures verified on the live receive path (messaging.VerifyCertificate).
	// It is stamped just before the block is persisted (gossip receive +
	// ProcessBlockLocally) so the certificate survives past the ephemeral gossip
	// envelope (BlockMessage.Data["bls_results"]) and can be re-verified during
	// sync (ThebeSync / FastSync v4). Carried as a raw JSON string to avoid a
	// config→AVC import cycle.
	//
	// ADVISORY, NOT CONSENSUS-HASHED (same as AccountNonces / StateFingerprint):
	// the canonical BlockHash is tx-contents only, so this field does not change
	// BlockHash and older builds ignore it on unmarshal — mixed fleets stay
	// wire-compatible. Blocks produced before this field existed carry it empty
	// (the legacy prefix); their sync trust rests on the genesis anchor + the
	// state-root hash chain + the first certified block (see docs/THEBESYNC-DESIGN.md).
	CommitteeCertificate string `json:"committee_certificate,omitempty"`
	// AVC consensus metadata (M1/M2a — architecture doc §8). These six fields
	// put the committee-selection inputs in the block, so a re-syncing node can
	// reconstruct which committee a block claims instead of trusting the proposer.
	//
	// NOT YET HASH-COVERED. Like AccountNonces above, the block hash covers
	// transaction contents only, so a relay can still rewrite these post-commit.
	// Making them tamper-evident is M2b
	// (Security.RecomputeBlockHashWithConsensusFields, written but not wired).
	// Until then, do not treat these as certificate-verified data.
	//
	// Propagation is JSON and every field is omitempty, so old nodes ignore
	// unknown keys and new nodes read absent keys as zero.

	// Slot is the epoch clock (§7.1). Advances on a commit OR a timeout, so it
	// skips where BlockNumber never does. Selection epoch = Slot / N.
	Slot uint64 `json:"slot,omitempty"`

	// Period is the retry counter at this height (§7.1c). Feeds the committee
	// seed, so a retry re-draws. Resets to 0 on the next height.
	Period uint64 `json:"period,omitempty"`

	// RandaoReveals are the entropy-committee reveals collected in this block
	// (§4.4). Empty on blocks that carry none.
	RandaoReveals []Reveal `json:"randao_reveals,omitempty"`

	// VdfProof is the epoch VDF proof. Present only on the epoch-boundary
	// block (§7.2), empty on every other block.
	VdfProof []byte `json:"vdf_proof,omitempty"`

	// SeedEpoch is the frozen RANDAO snapshot lock — which snapshot produced
	// this epoch's entropy (§3.2). Changes only at epoch boundaries.
	SeedEpoch uint64 `json:"seed_epoch,omitempty"`

	// VotingSnapshotEpoch is the declared voting pool that T_vote and T_agg are
	// checked against (§3.2). Checkpoint-locked; verifiers check monotonicity
	// only, since "is this the newest" is unenforceable (finding A6).
	VotingSnapshotEpoch uint64 `json:"voting_snapshot_epoch,omitempty"`

	// PrevAggCert is the buddy-committee certificate for the PREVIOUS block —
	// the signatures that committed it. Added 2026-08-20 to unblock B1
	// (Architecture §4.2a's fallback formula, §10 decision 10).
	//
	// WHY IT IS THE PREVIOUS BLOCK'S, NOT THIS ONE'S. §4.2a describes folding
	// "every committed block's BLS aggregate signature", implying each block
	// carries its own. That is structurally impossible: the buddies sign this
	// block's HASH, so this block's certificate cannot be an input to its own
	// hash without a circular dependency. Verified in code — the fields are
	// attached in Block/consensus_fields.go's attachAVCConsensusFields, which
	// its own call site documents as running "before consensus.Start", and the
	// votes are taken over blk.BlockHash (Sequencer/Consensus.go). So the
	// certificate necessarily lags by exactly one block. The fold window
	// compensates by reading it one slot later.
	//
	// Present ONLY on blocks whose slot falls inside a fold window
	// [E*N+K+1, E*N+K+B+1) — the +1 is that same one-block lag. Empty on every
	// other block, which is ~90% of them at N=50, B=5, so the storage cost is
	// a tenth of what carrying it on every block would be.
	PrevAggCert []CertSigner `json:"prev_agg_cert,omitempty"`

	// CommitteeSnapshotHash anchors the entropy-committee eligible-set
	// snapshot on-chain: a 32-byte digest (avc/committee.HashSnapshot) of the
	// frozen validator list used to seed this block's slot's entropy
	// committee draw. Added 2026-08-24, docs/COMMITTEE-SNAPSHOT-FREEZE-TODO.md
	// items 1/6/8 — lets a node that has synced this block verify a snapshot
	// body served by a seed node (or recovered from its own local cache)
	// against a value that traveled with the chain itself, instead of having
	// to trust whoever served the body.
	//
	// Gated by JMDN_COMMITTEE_SNAPSHOT_ANCHOR (default off, same coordinated
	// rollout pattern as M2b and JMDN_AVC_AGG_CERT) and by
	// messaging.frozenSnapshotHashFor actually having a cached value for this
	// block's slot's epoch — empty on every block until both are true.
	// Deliberately does NOT carry the snapshot body itself (large, grows with
	// the pool) — only the hash. The body is served off-chain; see the TODO.
	CommitteeSnapshotHash []byte `json:"committee_snapshot_hash,omitempty"`

	// ConsensusHash is the M2b consensus-fields digest
	// (Security.RecomputeBlockHashWithConsensusFields): a hash over the six AVC
	// consensus fields (Slot/Period/RandaoReveals/VdfProof/SeedEpoch/
	// VotingSnapshotEpoch) plus PrevAggCert, CommitteeSnapshotHash, FeeRecipients,
	// and the transaction contents. It is a SEPARATE field and NEVER replaces
	// BlockHash — BlockHash stays the orchestrator's transactions-only identity.
	//
	// Unlike AccountNonces/StateFingerprint (purely advisory), ConsensusHash is
	// consensus-COVERED: when set, the committee's v4 vote signs over it (see
	// BLS_Signer.CanonicalVoteMessageV4), so Period/FeeRecipients/PrevAggCert
	// cannot be rewritten post-commit without invalidating the certificate. Empty
	// (zero hash) on blocks built without the consensus binding; v4 falls back to
	// v3 (BlockHash-only) then, so mixed fleets during rollout still converge.
	// Set by Block/consensus_fields.go's attachAVCConsensusFields; recomputed and
	// checked on receive by messaging.checkConsensusBinding.
	ConsensusHash common.Hash `json:"consensus_hash,omitempty"`
}

// ConsensusHashHex returns the ConsensusHash as a 0x-hex string, or "" when it
// is the zero hash (block built without the consensus binding). Vote sign/verify
// call sites use this so a zero ConsensusHash selects the v3 vote domain (block
// hash only) instead of binding an all-zero consensus hash.
func (b *ZKBlock) ConsensusHashHex() string {
	if b == nil || b.ConsensusHash == (common.Hash{}) {
		return ""
	}
	return b.ConsensusHash.Hex()
}

// CertSigner is one buddy's contribution to a block's commit certificate: who
// signed, with which committee key, and the signature itself.
//
// The components are carried rather than a pre-aggregated blob deliberately.
// An aggregate alone is opaque — a sequencer could put any 64 bytes there and a
// verifier could not tell. Carrying the parts lets every node re-verify each
// signature against the previous block's canonical vote message and then
// DERIVE the aggregate itself, so the sequencer's only remaining freedom is
// which qualifying subset to include. That residual is exactly the
// already-documented subset menu in §4.2a (up to 1,093 values at A=13), not a
// new unbounded freedom.
type CertSigner struct {
	// PeerID of the signing buddy.
	PeerID string `json:"peer_id"`

	// PubKey is the hex-encoded committee BLS public key, same encoding
	// BLS_Signer.BLSresponse uses.
	PubKey string `json:"pub_key"`

	// Signature is the hex-encoded BLS signature over the previous block's
	// canonical v3 vote message with vote=+1.
	Signature string `json:"signature"`
}

// Reveal is one entropy-committee member's RANDAO reveal carried in a block.
// Mirrors the (peerID -> secret) shape avc/randao uses internally, flattened to
// a slice because block encoding needs a deterministic order and maps don't
// have one. Each entry is verified against its own proposer's commitment, so
// the delivery path doesn't matter and duplicates are harmless (§4.4).
type Reveal struct {
	// ProposerID is the peer ID of the revealing member.
	ProposerID string `json:"proposer_id"`

	// Secret is the raw 32-byte value; its hash must match this member's
	// earlier commitment (§4.3).
	Secret []byte `json:"secret"`
}

// AccountNonce binds one account address to its canonical ART identity nonce
// for Fastsync AccountSync routing. See ZKBlock.AccountNonces.
type AccountNonce struct {
	Address common.Address `json:"address"`
	Nonce   uint64         `json:"nonce"`
}

// ParsedZKTransaction is a helper struct with parsed numeric fields
type ParsedZKTransaction struct {
	Original        *Transaction
	ValueBig        *big.Int
	EffectiveGasFee *big.Int
}

// Receipt represents the result of a transaction execution
type Receipt struct {
	// Transaction identification
	TxHash           common.Hash `json:"tx_hash"`           // Hash of the transaction
	BlockHash        common.Hash `json:"block_hash"`        // Hash of the block containing this transaction
	BlockNumber      uint64      `json:"block_number"`      // Block number where transaction was included
	TransactionIndex uint64      `json:"transaction_index"` // Index of transaction within the block

	// Transaction execution status
	Status uint64 `json:"status"` // 1 = success, 0 = failure
	Type   uint8  `json:"type"`   // Transaction type (0=Legacy, 1=EIP-2930, 2=EIP-1559)

	// Gas consumption
	GasUsed           uint64 `json:"gas_used"`            // Gas consumed by this transaction
	CumulativeGasUsed uint64 `json:"cumulative_gas_used"` // Total gas used up to this transaction in the block

	// Contract creation (if applicable)
	ContractAddress *common.Address `json:"contract_address,omitempty"` // Address of created contract (if any)

	// Event logs generated by the transaction
	Logs      []Log  `json:"logs"`       // Array of log entries
	LogsBloom []byte `json:"logs_bloom"` // Bloom filter for logs

	// ZK-specific fields
	ZKProof  []byte `json:"zk_proof,omitempty"`  // ZK proof for transaction execution
	ZKStatus string `json:"zk_status,omitempty"` // Status of ZK proof verification
}

// Log represents an event log generated during transaction execution
type Log struct {
	Address     common.Address `json:"address"`      // Address of the contract that generated the log
	Topics      []common.Hash  `json:"topics"`       // Indexed log parameters
	Data        []byte         `json:"data"`         // Non-indexed log data
	BlockNumber uint64         `json:"block_number"` // Block number where log was created
	BlockHash   common.Hash    `json:"block_hash"`   // Hash of the block containing this log
	TxHash      common.Hash    `json:"tx_hash"`      // Hash of the transaction that generated this log
	TxIndex     uint64         `json:"tx_index"`     // Index of transaction within the block
	LogIndex    uint64         `json:"log_index"`    // Index of log within the transaction
	Removed     bool           `json:"removed"`      // True if log was removed due to chain reorganization
}

// BlockMessage is a wrapper for a ZKBlock for network propagation.
// It includes metadata for routing and deduplication.
type BlockMessage struct {
	ID        string            `json:"id"`        // Unique message ID
	Sender    string            `json:"sender"`    // Original sender's peer ID
	Timestamp int64             `json:"timestamp"` // Unix timestamp when message was created
	Nonce     string            `json:"nonce"`     // Unique nonce for CRDT
	Block     *ZKBlock          `json:"block"`     // The ZK block data
	Hops      int               `json:"hops"`      // How many hops this message has made
	Type      string            `json:"type"`      // Type of message
	Data      map[string]string `json:"data"`      // Additional data
}
