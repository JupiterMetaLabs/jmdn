// Package txstatus answers "what happened to this transaction hash?" for
// transactions that are not (yet) in a block.
//
// The problem it exists to solve: jmdn's chain store only knows about mined
// transactions, so a miss there is ambiguous. It could mean the transaction is
// queued in the mempool, that it was rejected, that it is still in flight, or
// that the hash never existed at all. Reporting the wrong one of those is
// actively harmful — telling a wallet "processing" for a hash that was never
// submitted makes it poll forever, and telling it "absent" because a lookup
// timed out makes it give up on a transaction that is about to be mined.
//
// The rules this package enforces:
//
//   - NEVER report `processing` without positive evidence the transaction
//     existed. "Not in the chain store and not in the mempool" is also true of
//     a typo and of an adversarial probe. Evidence comes from the local submit
//     log, written when jmdn forwarded the transaction to the mempool.
//
//   - NEVER treat an inconclusive mempool answer as absence. MRE distinguishes
//     "conclusively not pending" from "could not tell"; a degraded answer
//     collapses to `unknown`, never to `processing` and never to `queued`.
//
//   - ALWAYS re-check the chain store after a mempool hit. Destructive fetches
//     delete asynchronously, so the mempool can report a transaction present
//     that is already in a block being assembled. The second chain read wins.
//
//   - NEVER let a remote call block the caller. Every lookup is deadline-bound
//     and fails open to `unknown`; a slow or dead MRE degrades the answer, it
//     does not hang or error the RPC.
//
// This package deliberately depends on nothing inside jmdn except the metrics
// registry: the chain store and the mempool are reached through the narrow
// interfaces below, so the resolution rules can be tested without a database,
// a network, or a running node.
package txstatus

import (
	"context"
	"time"
)

// Status is the answer returned to a caller.
type Status string

const (
	// StatusMined means the transaction is in a block.
	StatusMined Status = "mined"
	// StatusQueued means the transaction is pending in the mempool fleet.
	StatusQueued Status = "queued"
	// StatusProcessing means we have positive evidence the transaction exists
	// (we forwarded it) but it is neither mined nor currently visible in the
	// mempool. This is a legitimate in-flight window, not a guess.
	StatusProcessing Status = "processing"
	// StatusFailed means the transaction was rejected and a reason is recorded.
	StatusFailed Status = "failed"
	// StatusUnknown means we have no evidence this transaction ever existed, or
	// we could not determine its state. Callers should treat it as terminal for
	// polling purposes only in combination with Detail.
	StatusUnknown Status = "unknown"
)

// Source records which store produced the answer, for observability and so a
// caller can tell a confident answer from a degraded one.
type Source string

const (
	// SourceChain — jmdn's own chain store.
	SourceChain Source = "chain"
	// SourceMempool — the MRE lookup.
	SourceMempool Source = "mempool"
	// SourceFailedStore — the local rejected-transaction store.
	SourceFailedStore Source = "failed_store"
	// SourceSubmitLog — jmdn's local record of having forwarded the transaction.
	SourceSubmitLog Source = "submit_log"
	// SourceNegativeCache — a previously computed `unknown`, served locally.
	SourceNegativeCache Source = "negative_cache"
	// SourceNone — nothing answered.
	SourceNone Source = "none"
)

// AccessTuple is one EIP-2930 access-list entry.
type AccessTuple struct {
	Address     string
	StorageKeys []string
}

// PendingTx is a mempool transaction body in a form independent of any wire or
// storage type, so this package does not have to import the mempool protos or
// the RPC facade's types.
//
// Numeric fields are strings because that is how the mempool carries them; the
// adapter that produced this value is responsible for the encoding, and the
// consumer for parsing it.
type PendingTx struct {
	Hash           string
	From           string
	To             string
	Value          string
	Type           uint32
	Timestamp      uint64
	ChainID        string
	Nonce          uint64
	GasLimit       string
	GasPrice       string
	MaxFee         string
	MaxPriorityFee string
	Data           []byte
	AccessList     []AccessTuple
	V              string
	R              string
	S              string
}

// Result is the resolved status of a hash.
type Result struct {
	Hash   string
	Status Status
	Source Source
	// Detail is a short human-readable note explaining an unusual answer —
	// why a lookup was degraded, why a forward failed. Empty on the happy path.
	Detail string
	// SubmittedAt is set when the answer came from the submit log.
	SubmittedAt *time.Time
	// MempoolNode and ShardID are set when the answer came from the mempool.
	MempoolNode string
	ShardID     *int32
	// Reason is the recorded rejection reason when Status is StatusFailed.
	Reason string
	// Tx is the pending transaction body when Status is StatusQueued and the
	// mempool returned one. Callers may use it to serve a pending
	// eth_getTransactionByHash response.
	Tx *PendingTx
	// Degraded is true when some part of the resolution could not be completed.
	// A degraded Result is never proof of absence.
	Degraded bool
}

// ─────────────────────────────────────────────────────────────────────────────
// Ports
// ─────────────────────────────────────────────────────────────────────────────

// ChainStore is the narrow slice of jmdn's database this package needs.
type ChainStore interface {
	// IsMined reports whether the hash is present in a block. An error means
	// the question could not be answered; it must not be read as "no".
	IsMined(ctx context.Context, hash string) (bool, error)
}

// MempoolResult is a mempool lookup outcome.
//
// Found and Degraded must be read together, mirroring MRE's contract:
// Found=false with Degraded=false is a conclusive absence; Found=false with
// Degraded=true means the fleet could not answer.
type MempoolResult struct {
	Found    bool
	Degraded bool
	ShardID  int32
	NodeID   string
	Tx       *PendingTx
	// Detail explains a degraded answer (breaker open, deadline, RPC error).
	Detail string
}

// MempoolLookup asks the mempool fleet about a hash without mutating it.
//
// Implementations MUST NOT return an error for an unreachable or slow mempool:
// that is a degraded result, not a failure of the status query. Returning an
// error here would propagate to eth_getTransactionByHash, which must never
// error because the mempool is down.
type MempoolLookup interface {
	Lookup(ctx context.Context, hash string) (*MempoolResult, error)
}

// FailedRecord is a recorded rejection.
type FailedRecord struct {
	Hash        string
	Reason      string
	MempoolNode string
	RecordedAt  time.Time
}

// FailedStore is the local rejected-transaction store.
//
// Not implemented yet: it requires an agreed contract with
// JMDT-Sequencer-Orchestrator for how rejections reach jmdn. The resolver
// treats a nil FailedStore as "no rejections known", so `failed` simply never
// appears until this is wired — it never produces a wrong answer in the
// meantime.
type FailedStore interface {
	Get(ctx context.Context, hash string) (*FailedRecord, bool)
}
