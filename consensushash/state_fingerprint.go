// State fingerprint v1 — the canonical, domain-tagged commitment to full
// account + contract state that P2.5 folds into the block header and CON-02
// folds into the block identity (blockhash v3's stateRoot term).
//
// WHY THIS EXISTS (audit P2.5 / B2 / EVM-A1):
// Every receiver independently APPLIES a block, and fast-synced nodes derive
// balances and then vote. So all nodes must arrive at the SAME post-apply state
// or the chain has silently forked. Today the block's consensus StateRoot commits
// to NO account/contract state (Block/helper/stateroot.go hashes only parent root
// + block hash, and the block hash is just the tx-hash concatenation), so a
// balance divergence is invisible — reproduced live=1000 vs synced=2000 for the
// same account. The fix is a canonical fingerprint the committee signs and every
// node recomputes after applying: a mismatch HALTS the node instead of serving a
// wrong balance. In the single-sequencer model this is the cheap, correct
// substitute for a full Merkle-Patricia trie root — it DETECTS divergence, which
// is all a single-sequencer chain needs.
//
// RELATION TO THE EXISTING DIGESTS (deliberately superseding, not duplicating):
//   - DB_OPs.ComputeAccountStateFingerprint streams live accounts but uses SHA-256,
//     no domain tag, and omits contract state — an operator diff tool, not a header
//     commitment. This v1 is keccak (matching the rest of the consensus hashing),
//     domain-separated, and contract-inclusive.
//   - DB_OPs/contractDB.stateHasher digests the CHANGED objects of ONE commit (a
//     per-block delta). This v1 digests the FULL post-apply state (all accounts +
//     all contracts) — the thing the header must commit to.
//
// Reconciling the runtime streaming path to this canonical encoding is the
// CGO-gated + 2-node-gated wiring step; this file is the pure, sandbox-verifiable
// primitive it will call.
//
// Dependency-light and CGO-free (only go-ethereum/common for the Hash/Address
// value types and x/crypto/sha3 for keccak — both pure Go), matching
// blockhash_v3.go, so it carries no import cycle and is unit-testable in a plain
// `go test` with CGO off.
package consensushash

import (
	"encoding/binary"
	"hash"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"golang.org/x/crypto/sha3"
)

// StateFingerprintV1Domain is the domain-separation tag for the v1 state
// fingerprint preimage. Distinct from BlockHashV3Domain so the two hashes can
// never collide even on coincidentally-identical byte streams.
const StateFingerprintV1Domain = "jmdn/state-fingerprint/v1"

// Section type tags. Every record is prefixed with its section tag so an account
// record and a contract record for the same address can never cross-cancel.
const (
	accountRecordTag  byte = 0x01
	contractRecordTag byte = 0x02
)

// AccountLeaf is one plain account's consensus-relevant state. Volatile local
// metadata (UpdatedAt/CreatedAt/DID linkage/custom metadata) is deliberately
// excluded — it may legitimately differ across nodes and must not affect the
// fingerprint. This mirrors the field set of DB_OPs.ComputeAccountStateFingerprint
// (address, balance, tx nonce, sent-tx count).
type AccountLeaf struct {
	Address     string // hex; normalized to lowercase+trimmed before hashing
	Balance     string // decimal string; "" is treated as "0"
	TxNonce     uint64
	TxCountSent uint64
}

// ContractLeaf is one contract account's consensus-relevant state. CodeHash and
// StorageRoot are 32-byte commitments already produced by the contract-state
// layer; nonce is the contract's account nonce.
type ContractLeaf struct {
	Address     string // hex; normalized to lowercase+trimmed before hashing
	Nonce       uint64
	CodeHash    common.Hash
	StorageRoot common.Hash
}

func normAddr(a string) string { return strings.ToLower(strings.TrimSpace(a)) }

// isEmptyAccount reports whether a plain account is EIP-161 "empty": zero balance,
// zero tx nonce, and zero sent-tx count (plain accounts carry no code, so there is
// no code term). Such an account is treated as ABSENT and contributes nothing to
// the fingerprint — mirroring go-ethereum, where an empty account is pruned from
// the state trie and does not affect the state root.
//
// WHY THIS MATTERS (consensus-relevant): the block-apply path can leave a
// zero-balance account row behind when a block fails the fingerprint gate and is
// rolled back (the recipient of a failed transfer is created, then restored to
// balance 0 rather than removed, because the canonical KV log is append-only and
// has no delete). Folding that empty stub made a node's post-rollback state
// fingerprint diverge from a clean apply that never created it — a false
// divergence that halts the node. Skipping empty accounts makes the fingerprint
// depend only on accounts that actually hold value or have transacted, so an
// empty stub is indistinguishable from absence and cannot cause divergence.
//
// This changes the fingerprint definition, which is folded into the block-identity
// state-root term — so it is a consensus change and MUST roll out fleet-wide
// together. It is backward-compatible in practice only if no COMMITTED block's
// state contains an empty account (a committed transfer always credits a non-zero
// balance); verify before deploy.
func (a AccountLeaf) isEmptyAccount() bool {
	bal := strings.TrimSpace(a.Balance)
	return (bal == "" || bal == "0") && a.TxNonce == 0 && a.TxCountSent == 0
}

// StateFingerprintV1 computes the canonical fingerprint over the FULL state:
// every account leaf (sorted by normalized address) then every contract leaf
// (sorted by normalized address), under the domain tag. It is a pure function of
// its inputs and independent of the order the slices are given in (it sorts
// internally), so two nodes that hold the same state compute the same hash
// regardless of iteration order.
//
// Use this form for tests and any caller that can materialize the leaves. The
// runtime apply path that streams accounts from the DB in sorted pages should use
// StateFingerprinterV1 instead (same encoding, no full materialization).
func StateFingerprintV1(accounts []AccountLeaf, contracts []ContractLeaf) common.Hash {
	f := NewStateFingerprinterV1()

	acc := make([]AccountLeaf, len(accounts))
	copy(acc, accounts)
	sort.Slice(acc, func(i, j int) bool { return normAddr(acc[i].Address) < normAddr(acc[j].Address) })
	for _, a := range acc {
		f.FoldAccount(a)
	}

	con := make([]ContractLeaf, len(contracts))
	copy(con, contracts)
	sort.Slice(con, func(i, j int) bool { return normAddr(con[i].Address) < normAddr(con[j].Address) })
	for _, c := range con {
		f.FoldContract(c)
	}

	return f.Sum()
}

// StateFingerprinterV1 folds a state fingerprint incrementally. The CALLER MUST
// fold accounts in ascending normalized-address order first, then contracts in
// ascending normalized-address order — the same canonical order StateFingerprintV1
// produces internally. This lets the runtime stream sorted DB pages without
// materializing every account. Length-prefixed / fixed-width framing makes the
// concatenation injective, so no per-section count is needed.
type StateFingerprinterV1 struct{ h hash.Hash }

// NewStateFingerprinterV1 starts a fingerprint with the domain tag already folded.
func NewStateFingerprinterV1() *StateFingerprinterV1 {
	h := sha3.NewLegacyKeccak256()
	h.Write([]byte(StateFingerprintV1Domain))
	return &StateFingerprinterV1{h: h}
}

// writeVar folds a length-prefixed variable-length field (big-endian uint64 len).
func (f *StateFingerprinterV1) writeVar(b []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(b)))
	f.h.Write(n[:])
	f.h.Write(b)
}

func (f *StateFingerprinterV1) writeU64(v uint64) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], v)
	f.h.Write(n[:])
}

// FoldAccount folds one plain-account record. Balance "" is normalized to "0" and
// the address is lowercased so the digest is independent of checksum casing.
func (f *StateFingerprinterV1) FoldAccount(a AccountLeaf) {
	// EIP-161: an empty account (zero balance, zero nonce, zero sent-count) is
	// equivalent to a non-existent one and contributes NOTHING to the fingerprint.
	// This is the single skip point shared by the batch (StateFingerprintV1) and
	// streaming (ComputeAccountStateFingerprintV1) paths, so both agree exactly.
	// See isEmptyAccount for the consensus rationale and rollout constraints.
	if a.isEmptyAccount() {
		return
	}
	bal := a.Balance
	if bal == "" {
		bal = "0"
	}
	f.h.Write([]byte{accountRecordTag})
	f.writeVar([]byte(normAddr(a.Address)))
	f.writeVar([]byte(bal))
	f.writeU64(a.TxNonce)
	f.writeU64(a.TxCountSent)
}

// FoldContract folds one contract-account record.
func (f *StateFingerprinterV1) FoldContract(c ContractLeaf) {
	f.h.Write([]byte{contractRecordTag})
	f.writeVar([]byte(normAddr(c.Address)))
	f.writeU64(c.Nonce)
	f.h.Write(c.CodeHash[:])
	f.h.Write(c.StorageRoot[:])
}

// Sum returns the accumulated fingerprint.
func (f *StateFingerprinterV1) Sum() common.Hash {
	var out common.Hash
	f.h.Sum(out[:0])
	return out
}
