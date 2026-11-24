// Package adapters bridges jmdn's native consensus types onto the standalone
// avc module's host interfaces (github.com/JupiterMetaLabs/avc/interfaces).
//
// This is the jmdn side of AVC extraction work item A3. It is deliberately a
// thin, pure mapping: no behaviour, no state, no DB access — it only exposes
// an existing *config.ZKBlock through the method set avc expects. Keeping it
// dumb is the point; all real validation logic lives in avc (structural) or
// in the checkers jmdn injects later (A2.4).
//
// NOTHING in jmdn's live runtime calls this yet. It exists so the A3
// integration can be built and, above all, so the hash-parity assumption
// (that avc's recompute matches jmdn's) can be PROVEN by test before any
// wiring — see parity_test.go.
package adapters

import (
	"github.com/JupiterMetaLabs/avc/interfaces"

	"gossipnode/config"
)

// txAdapter exposes one config.Transaction as an interfaces.Transaction.
//
// TxHashBytes returns the transaction's precomputed 32-byte Hash
// (config.Transaction.Hash, a go-ethereum common.Hash). That field is the
// EXACT leaf jmdn's own generators consume — messaging.RecomputeBlockHashFromTxs
// and messaging.RecomputeTxnsRoot both read txs[i].Hash.Bytes(). avc's
// recompute must be fed the identical bytes or every block fails structural
// validation; the parity test pins that they are identical.
type txAdapter struct {
	tx config.Transaction
}

func (t txAdapter) TxHashBytes() []byte {
	// common.Hash is a fixed [32]byte; Bytes() returns a fresh slice copy, so
	// the adapter never aliases the block's underlying storage.
	return t.tx.Hash.Bytes()
}

// JMDNTransaction exposes the underlying config.Transaction.
//
// avc's interfaces.Transaction intentionally exposes ONLY TxHashBytes — it is
// host-agnostic and must never carry Ethereum-specific fields (From, V/R/S,
// ChainID, Nonce…). But the jmdn-side checkers (StatelessChecker /
// StatefulChecker) need the full transaction to call jmdn's real Security
// functions. This method is the recovery seam: a checker type-asserts the
// interfaces.Transaction it receives back to jmdnBacked and pulls out the
// concrete tx. avc itself never calls this; only jmdn's own checkers do, so
// the host-agnostic boundary is preserved.
func (t txAdapter) JMDNTransaction() config.Transaction { return t.tx }

// NewTxAdapter wraps a single config.Transaction as an interfaces.Transaction.
// Exported so callers (including tests in package adapters_test, which cannot
// see the unexported txAdapter type) can build one transaction's adapter
// directly, without going through a whole ZKBlockAdapter.
func NewTxAdapter(tx config.Transaction) interfaces.Transaction { return txAdapter{tx: tx} }

// jmdnBacked is implemented by any interfaces.Transaction that wraps a real
// config.Transaction (i.e. txAdapter). The checkers assert to this to recover
// the concrete transaction; a value that does not implement it is rejected
// fail-closed rather than silently skipped.
type jmdnBacked interface {
	JMDNTransaction() config.Transaction
}

// ZKBlockAdapter exposes a *config.ZKBlock as an interfaces.ZKBlock.
//
// It holds a pointer, not a copy: a ZKBlock carries a StarkProof and a full
// transaction list, and copying it per validation call would be wasteful. The
// adapter never mutates the block.
type ZKBlockAdapter struct {
	blk *config.ZKBlock
}

// NewZKBlockAdapter wraps a jmdn block for avc. A nil blk is allowed and is
// handled safely by avc's interfaces.IsNilBlock guard (the adapter itself is
// a non-nil interface value wrapping a nil pointer — the classic typed-nil
// case avc explicitly defends against), but callers should prefer passing a
// real block.
func NewZKBlockAdapter(blk *config.ZKBlock) *ZKBlockAdapter {
	return &ZKBlockAdapter{blk: blk}
}

// Every accessor guards `a == nil || a.blk == nil` and returns a zero value.
//
// WHY: avc's interfaces.IsNilBlock catches a TYPED-NIL interface (an interface
// value whose dynamic type is *ZKBlockAdapter and whose pointer is nil). It
// CANNOT catch a non-nil *ZKBlockAdapter that wraps a nil *config.ZKBlock —
// the NESTED-nil case — because from avc's side the interface holds a valid
// pointer. That case is reachable (NewZKBlockAdapter(nil), or a zero-value
// &ZKBlockAdapter{}), so without these guards the first field access would
// panic inside avc's validator. Returning zero values instead makes a
// nil-backed block present as an empty block, which avc's StructuralValidator
// rejects (ReasonEmptyTransactions) — a clean veto, never a panic. A panic in
// the consensus path is a DoS vector, so this is a safety guard, not cosmetics.

// BlockHashString returns the block's claimed hash as "0x"-prefixed lowercase
// hex. go-ethereum common.Hash.Hex() and avc's recompute (0x + hex of the
// Keccak digest) produce the same form; avc compares case-insensitively
// regardless.
func (a *ZKBlockAdapter) BlockHashString() string {
	if a == nil || a.blk == nil {
		return ""
	}
	return a.blk.BlockHash.Hex()
}

func (a *ZKBlockAdapter) BlockNumber() uint64 {
	if a == nil || a.blk == nil {
		return 0
	}
	return a.blk.BlockNumber
}

func (a *ZKBlockAdapter) TransactionCount() int {
	if a == nil || a.blk == nil {
		return 0
	}
	return len(a.blk.Transactions)
}

func (a *ZKBlockAdapter) PrevHashString() string {
	if a == nil || a.blk == nil {
		return ""
	}
	return a.blk.PrevHash.Hex()
}

// TxnsRootString returns the block's claimed transactions root. jmdn stores
// this as a string already ("0x"-prefixed hex), so it passes through directly.
func (a *ZKBlockAdapter) TxnsRootString() string {
	if a == nil || a.blk == nil {
		return ""
	}
	return a.blk.TxnsRoot
}

// Transactions returns the block's transactions as avc's interface type, in
// block order (order matters: both the block hash and the txns root are
// order-sensitive).
func (a *ZKBlockAdapter) Transactions() []interfaces.Transaction {
	if a == nil || a.blk == nil {
		return nil
	}
	out := make([]interfaces.Transaction, len(a.blk.Transactions))
	for i := range a.blk.Transactions {
		out[i] = txAdapter{tx: a.blk.Transactions[i]}
	}
	return out
}

// Compile-time assertions that the adapters satisfy avc's interfaces. If avc
// ever widens interfaces.ZKBlock or interfaces.Transaction, this fails to
// build here — a loud, immediate signal rather than a runtime surprise.
var (
	_ interfaces.ZKBlock     = (*ZKBlockAdapter)(nil)
	_ interfaces.Transaction = txAdapter{}
)
