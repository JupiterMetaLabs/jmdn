package Security

// M2b — the consensus-fields digest the committee's v4 vote signs over (§8).
//
// WIRED AND LOAD-BEARING. attachAVCConsensusFields (Block/consensus_fields.go)
// sets block.ConsensusHash from this function on every proposed block, and
// checkConsensusBinding (messaging/consensus_hardening.go, called from
// blockPropagation.go's receive path) recomputes it and REJECTS the block on a
// mismatch. Changing anything hashed here — the domain tag, the field order, an
// added field — is a CONSENSUS CHANGE: nodes computing different preimages
// reject each other with consensus_hash_mismatch and the chain forks.
//
// Why: both existing hash functions cover transactions only, so the six fields
// don't affect BlockHash at all. Rewriting Period alone changes the committee
// seed (§5), letting a block claim a committee that never held quorum.

import (
	"bytes"

	"github.com/JupiterMetaLabs/avc/committee"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"gossipnode/config"
)

// blockHashDomain is THE consensus-hash preimage tag. One format, fleet-wide.
//
// The format this replaced (same fields, no BlockNumber, no PrevHash) is audit
// finding D-28 / JMDN-V3-002, Critical: it bound what a block CONTAINS but not
// WHERE IN THE CHAIN it sits. Two blocks on different forks at the same height
// with the same transactions, slot and period produced the SAME ConsensusHash —
// and since BlockHash is transactions-only they collided too, making
// CanonicalVoteMessageV4 byte-identical for both. A certificate gathered for one
// fork verified against the other, and checkEquivocation (keyed on BlockHash,
// blockPropagation.go:677) saw one block where there were two.
//
// The old branch and its JMDN_CONSENSUS_HASH_V3 flag were removed before testnet
// launch: with no live fleet and no history to re-verify, one unconditional
// format is strictly safer than a switch that can be set two ways. The tag keeps
// the "v3" name so a stale binary emitting the old preimage produces a
// DIAGNOSABLE mismatch rather than an ambiguous one.
//
// DO NOT change this string or the preimage below without a coordinated
// fleet-wide cutover — see this file's header.
const blockHashDomain = "jmdn/block-hash/v3"

// RecomputeBlockHashWithConsensusFields computes the M2b block hash: the six
// AVC consensus fields plus the existing transaction-content binding.
//
//	H = Keccak256(
//	      len:domain
//	   || u64:BlockNumber
//	   || len:PrevHash
//	   || u64:Slot
//	   || u64:Period
//	   || len:encodeReveals(RandaoReveals)
//	   || len:VdfProof
//	   || u64:SeedEpoch
//	   || u64:VotingSnapshotEpoch
//	   || len:encodeCertSigners(PrevAggCert)
//	   || len:CommitteeSnapshotHash
//	   || len:concat(txContentHash_i)
//	    )
//
// Encoding reuses the codebase's existing convention (committee.WriteField /
// WriteU64, pinned by TestSeedConcatenationAmbiguity): variable-length fields
// get an 8-byte length prefix, fixed-width values are 8 bytes. That makes the
// preimage injective, so "[A, BC]" can't collide with "[AB, C]".
//
// Transactions bind by CONTENT hash, same rule as the legacy function.
//
// DIFFERS FROM v1: the legacy function returns the zero hash for a block with
// no transactions. This one doesn't — an empty block still has a slot and
// period worth binding, and two empty blocks at different slots must not share
// a hash.
func RecomputeBlockHashWithConsensusFields(block *config.ZKBlock) common.Hash {
	if block == nil {
		return common.Hash{}
	}

	var buf bytes.Buffer
	// Bind the block's POSITION in the chain FIRST. PrevHash is what actually
	// closes D-28 — it is the field that distinguishes two forks. BlockNumber is
	// bound alongside so the statement is explicit rather than implied.
	//
	// Both are populated before this runs: convertProtoToZKBlock fills them from
	// the orchestrator's block, and attachAVCConsensusFields (the only caller that
	// SETS ConsensusHash) runs afterwards. A zero PrevHash on a genesis-shaped
	// block hashes normally; it is a value, not a missing field.
	committee.WriteField(&buf, []byte(blockHashDomain))
	committee.WriteU64(&buf, block.BlockNumber)
	committee.WriteField(&buf, block.PrevHash.Bytes())
	committee.WriteU64(&buf, block.Slot)
	committee.WriteU64(&buf, block.Period)
	committee.WriteField(&buf, EncodeReveals(block.RandaoReveals))
	committee.WriteField(&buf, block.VdfProof)
	committee.WriteU64(&buf, block.SeedEpoch)
	committee.WriteU64(&buf, block.VotingSnapshotEpoch)
	// PrevAggCert (added 2026-08-20, blocker B1). Hash-covered for the same
	// reason every other field here is: it feeds the fallback seed, so a relay
	// that could rewrite it post-commit could steer the next epoch's entire
	// committee draw. Empty on ~90% of blocks (fold-window slots only), and
	// EncodeCertSigners renders empty as a zero count, so this adds a fixed 8
	// bytes to the preimage of an ordinary block and changes no existing field.
	committee.WriteField(&buf, EncodeCertSigners(block.PrevAggCert))
	// CommitteeSnapshotHash (added 2026-08-24, docs/COMMITTEE-SNAPSHOT-FREEZE-TODO.md
	// items 1/6/8). Hash-covered so a relay cannot substitute a different
	// eligible-set hash post-commit and have a rejoining node verify a
	// tampered snapshot body against it. Empty on every block until
	// JMDN_COMMITTEE_SNAPSHOT_ANCHOR is on, same "zero is honest" rule as
	// VdfProof/SeedEpoch above.
	committee.WriteField(&buf, block.CommitteeSnapshotHash)
	committee.WriteField(&buf, txContentConcat(block.Transactions))

	return common.BytesToHash(crypto.Keccak256(buf.Bytes()))
}

// EncodeReveals produces the canonical byte encoding of a reveal list.
//
//	u64:count || ( len:ProposerID || len:Secret )*
//
// Uses the block's array order, not a sorted one: the hash must bind the exact
// bytes carried, so reordering changes the hash and gets caught. Sorting here
// would make reordering invisible. Requiring canonical order is a separate,
// validation-time job — see RevealsAreCanonical.
//
// nil and empty both encode as count zero: "no reveals" has one form.
func EncodeReveals(reveals []config.Reveal) []byte {
	var buf bytes.Buffer
	committee.WriteU64(&buf, uint64(len(reveals)))
	for i := range reveals {
		committee.WriteField(&buf, []byte(reveals[i].ProposerID))
		committee.WriteField(&buf, reveals[i].Secret)
	}
	return buf.Bytes()
}

// RevealsAreCanonical reports whether reveals are strictly increasing by
// ProposerID, which also rules out duplicates. Rejecting non-canonical lists at
// validation keeps the hash binding exact while denying a proposer a menu of
// differently-hashed encodings of the same reveal set.
func RevealsAreCanonical(reveals []config.Reveal) bool {
	for i := 1; i < len(reveals); i++ {
		if reveals[i-1].ProposerID >= reveals[i].ProposerID {
			return false
		}
	}
	return true
}

// txContentConcat mirrors the legacy transaction binding: each transaction's
// content hash concatenated in block order. Separate helper so the v2 preimage
// provably reuses the same rule. Returns nil for an empty list, which
// WriteField encodes as zero length.
func txContentConcat(txs []config.Transaction) []byte {
	if len(txs) == 0 {
		return nil
	}
	buf := make([]byte, 0, len(txs)*32)
	for i := range txs {
		h := ethTxFromConfig(&txs[i]).Hash()
		buf = append(buf, h.Bytes()...)
	}
	return buf
}

// EncodeCertSigners produces the canonical byte encoding of a commit
// certificate.
//
//	u64:count || ( len:PeerID || len:PubKey || len:Signature )*
//
// Same length-prefixed convention as EncodeReveals, so the preimage stays
// injective — "[A, BC]" cannot collide with "[AB, C]".
//
// Uses the slice's own order, not a sorted one: the hash must bind the exact
// list the block declares, so that reordering is itself a detectable change.
// Producers should emit a deterministic order (messaging sorts by peer ID)
// precisely so two honest nodes assembling the same certificate agree.
func EncodeCertSigners(cert []config.CertSigner) []byte {
	var buf bytes.Buffer
	committee.WriteU64(&buf, uint64(len(cert)))
	for _, s := range cert {
		committee.WriteField(&buf, []byte(s.PeerID))
		committee.WriteField(&buf, []byte(s.PubKey))
		committee.WriteField(&buf, []byte(s.Signature))
	}
	return buf.Bytes()
}
