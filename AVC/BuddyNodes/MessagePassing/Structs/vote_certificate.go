package Structs

import (
	"encoding/hex"
	"fmt"
	"sort"

	blssign "gossipnode/AVC/BLS/bls-sign"

	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
)

// VoteCertificate is Phase 1.5 of docs/VALIDATOR-SCALE-VOTE-AGGREGATION-LLD.md
// §12.5: a buddy-side aggregate BLS signature over the YES voters already
// counted in this block's tally, carried alongside the buddy's own existing
// result signature as additional evidence for a later phase. The sequencer
// does not verify this yet — deliberately deferred until the electorate
// expands past today's committee (§12.5's exit list: SnapshotOrder, the
// pinned-committee rollout, and VerifyVoteCertificate all remain unbuilt).
// Building it now costs one aggregation over signatures already
// Stage-5-verified; it changes nothing about today's accept/reject decision
// or the sequencer's existing verification.
type VoteCertificate struct {
	AggSig  string   `json:"agg_sig"` // hex-encoded BLS aggregate signature over Signers' YES votes
	Signers []string `json:"signers"` // peer IDs whose YES vote is included, sorted for determinism
}

// buildVoteCertificate aggregates the BLS signatures backing every YES vote
// in single (avcvotes.BlockTally.SingleVotePeers()'s output — already
// authorized, already equivocation-filtered, already Stage-5 signature
// verified by the caller). NO voters are excluded, matching
// avc/committee/tally.go's own rule that only YES is ever aggregated or
// counted toward a certificate — a NO vote is verified and discarded, never
// weaponized against the block.
//
// Uses blssign (gossipnode/AVC/BLS/bls-sign) to aggregate, not avc's own bls
// package — VoteRecord.BLSSignature is produced by BLS_Signer.SignMessageForBlock,
// which signs via this same local package (confirmed: BLS_Signer/Signer.go's
// SignMessageForBlock calls blssign.BLSSign), so aggregation must use the
// matching library. Aggregating signatures across two different BLS
// implementations, even on the same curve, is not guaranteed to produce a
// verifiable result.
//
// Returns (nil, nil) — not an error — when there are zero YES voters. A
// missing certificate is not itself a fault: ProcessVotesFromCRDT's existing
// result/rejectionReasons/error return values remain the sole source of
// truth for the actual accept/reject decision, unchanged by this function.
func buildVoteCertificate(tally avcvotes.BlockTally, single map[string]int8) (*VoteCertificate, error) {
	var sigs [][]byte
	var signers []string

	for peerID, vote := range single {
		if vote != 1 {
			continue
		}
		recs := tally.Signatures[peerID]
		if len(recs) == 0 {
			// Defensive: SingleVotePeers implies a verified signature exists
			// for this peer's one counted vote. If it's somehow absent,
			// exclude this peer from the certificate rather than fail the
			// whole thing — this is unverified evidence, not the decision.
			continue
		}
		sigBytes, err := hex.DecodeString(recs[0].BLSSignature)
		if err != nil {
			// Same posture: one malformed signature excludes that peer from
			// the certificate, it does not fail certificate-building overall.
			continue
		}
		sigs = append(sigs, sigBytes)
		signers = append(signers, peerID)
	}

	if len(sigs) == 0 {
		return nil, nil
	}
	sort.Strings(signers)

	aggSig, err := blssign.BLSAggregate(sigs...)
	if err != nil {
		return nil, fmt.Errorf("aggregate YES-voter signatures: %w", err)
	}

	return &VoteCertificate{
		AggSig:  hex.EncodeToString(aggSig),
		Signers: signers,
	}, nil
}
