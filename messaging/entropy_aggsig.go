package messaging

// Blocker B1, cleared — Architecture §4.2a's fallback input. New 2026-08-20.
//
// # What B1 actually was
//
// §4.2a said the fold input was "the existing per-block commit certificate...
// no new data collected, ~0 bytes new storage". Audited: false. Nothing
// persisted the certificate — the buddy signatures were verified during
// consensus and discarded, so no node could reconstruct a past epoch's
// fallback seed at replay. That was the blocker.
//
// # And a second problem §4.2a did not anticipate
//
// A block's own certificate cannot be carried in that block. The buddies sign
// the block's HASH, so folding the certificate into the same hash is circular.
// Confirmed in code: attachAVCConsensusFields runs "before consensus.Start"
// and the votes are taken over blk.BlockHash. So the certificate lags exactly
// one block — block N carries block N-1's. Everything here is built around
// that lag, and the fold window shifts by one slot to compensate.
//
// # Why the parts, not a pre-aggregated blob
//
// If a block carried only a 64-byte aggregate, a dishonest sequencer could put
// any 64 bytes there: nothing downstream could tell, and it would hand that
// sequencer direct choice of the next epoch's committee — strictly worse than
// the interim formula it replaces. Carrying (peerID, pubkey, signature) per
// signer lets every node re-verify each signature against the previous block's
// canonical vote message and then DERIVE the aggregate locally. The
// sequencer's remaining freedom is only which qualifying subset to include,
// which is §4.2a's already-documented residual, not a new hole.
//
// # What this verifies, and what it does NOT — stated precisely
//
// Verified here: each signature is a real BLS signature over the previous
// block's canonical v3 vote message (chainID, prevHeight, prevHash, vote=+1);
// each signer's peerID-to-pubkey binding matches the epoch's eligible pool;
// duplicates by peerID or pubkey are rejected.
//
// NOT verified here: that the signers were the exact SEATED committee for that
// previous round. Doing so needs the previous block's own RoundContext, which
// needs its parent's hash — not available from the block in hand. The
// eligible-pool check bounds signers to real, registered validators, so a
// forged aggregate is still impossible; what remains possible is a sequencer
// preferring one eligible subset over another, which is the same residual
// named above. Closing it fully needs the round context cached at commit time
// and is a follow-up, not a silent omission.
import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/JupiterMetaLabs/avc/randao"

	blssign "gossipnode/AVC/BLS/bls-sign"
	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/DB_OPs"
	"gossipnode/config"

	"github.com/rs/zerolog/log"
)

// AggCertEnabled gates the whole B1 path. Default OFF.
//
// Same discipline as Security.M2bHashEnabled: this adds a hash-covered field to
// blocks, so turning it on is a coordinated fleet rollout, not a per-node
// choice. With it off, producers attach no certificate and verifiers ignore
// any they receive, so a mixed fleet cannot split.
var AggCertEnabled = os.Getenv("JMDN_AVC_AGG_CERT") == "1"

// certForNextBlock holds the certificate this node will attach to the block it
// proposes next — the certificate of the block it just committed.
//
// Set by RecordCommitCertificate (called from the sequencer's commit path),
// read by CertificateForBlockAssembly (called from attachAVCConsensusFields).
// One value, not a map: only the immediately-previous block's certificate is
// ever attached, so there is nothing to key or evict.
var (
	certForNextBlockMu sync.Mutex
	certForNextBlock   []config.CertSigner
	certForNextHeight  uint64
)

// RecordCommitCertificate stores the certificate that committed `height`, so
// the next block proposed can carry it.
//
// Called with the same []BLSresponse the sequencer already verified. It filters
// to YES votes only and sorts by peer ID, so two nodes holding the same
// responses produce a byte-identical certificate — required, since the list is
// hash-covered in array order.
func RecordCommitCertificate(height uint64, responses []BLS_Signer.BLSresponse) {
	if !AggCertEnabled {
		return
	}
	cert := make([]config.CertSigner, 0, len(responses))
	for _, r := range responses {
		if !r.Agree || r.PeerID == "" || r.PubKey == "" || r.Signature == "" {
			continue
		}
		cert = append(cert, config.CertSigner{
			PeerID: r.PeerID, PubKey: r.PubKey, Signature: r.Signature,
		})
	}
	sort.Slice(cert, func(i, j int) bool { return cert[i].PeerID < cert[j].PeerID })

	certForNextBlockMu.Lock()
	certForNextBlock = cert
	certForNextHeight = height
	certForNextBlockMu.Unlock()
}

// CertificateForBlockAssembly returns the certificate to attach to a block at
// `slot` with parent height `prevHeight`, or nil.
//
// Returns nil unless slot is inside the fallback collection deadline range —
// carrying it on every block would cost far more storage than only the range
// that might need it. The range is shifted by +1 slot to account for the
// one-block certificate lag: a block at slot S carries the certificate for
// its PARENT, so to cover collection slots [K, K+MaxOffset) the certificates
// ride on the blocks that follow them. With no timeouts the carrier sits at
// S+1; after a timeout the parent is period+1 slots back, which
// VerifyAndRecordPrevCert accounts for.
//
// Note this range can be wider than FallbackFoldBufferB slots — collection
// stops as soon as B signers are found, wherever in the range that happens,
// so any block before the deadline might turn out to be one of the B signers
// actually used. A timed-out round simply leaves its slot uncovered, which is
// expected and does not block collection (see entropy_fallback_window.go).
func CertificateForBlockAssembly(slot, prevHeight uint64) []config.CertSigner {
	if !AggCertEnabled {
		return nil
	}
	epoch := EpochForSlot(slot)
	start, deadline, err := randao.FallbackCollectionBounds(epoch, N, RevealCutoffK, FallbackFoldMaxSlotOffset)
	if err != nil {
		return nil
	}
	if slot < start+1 || slot >= deadline+1 {
		return nil // this block's parent is not in the collection range
	}

	certForNextBlockMu.Lock()
	defer certForNextBlockMu.Unlock()
	if certForNextHeight != prevHeight || len(certForNextBlock) == 0 {
		return nil // we do not hold the certificate for this block's parent
	}
	out := make([]config.CertSigner, len(certForNextBlock))
	copy(out, certForNextBlock)
	return out
}

// VerifyAndRecordPrevCert verifies the certificate a block carries for its
// parent, derives the aggregate signature, and records it against the parent's
// slot so FallbackSeedForEpoch can fold it.
//
// Called from the same commit hooks foldBlockDeclaredReveals uses, on every
// node — the aggregate is DERIVED locally from verified parts, never taken on
// the proposer's word.
//
// Silent no-op when the flag is off or the block carries no certificate. A
// certificate that fails verification is logged loudly and NOT recorded: a
// missing window slot makes the fold fail closed, which is the correct outcome,
// whereas recording an unverified value would defeat the entire point.
func VerifyAndRecordPrevCert(block *config.ZKBlock) {
	if !AggCertEnabled || block == nil || len(block.PrevAggCert) == 0 {
		return
	}
	if block.Slot == 0 || block.BlockNumber == 0 {
		return
	}
	// The parent's slot is NOT block.Slot-1. SlotStore.AdvanceOnCommit does
	// `slot += period + 1` (§7.1: a retried round burns a slot per timeout), so
	// a block committed at period P sits P+1 slots after its parent, not one.
	// Using Slot-1 would record the certificate against a slot that never had a
	// block, silently poking a hole in the fold window on exactly the rounds
	// that timed out. Latent while Period is pinned at 0; real the moment M0's
	// timeout certificates start incrementing it.
	if block.Slot < block.Period+1 {
		return // parent predates the slot counter; nothing to attribute
	}
	prevSlot := block.Slot - (block.Period + 1)
	prevHeight := block.BlockNumber - 1

	// EpochForHeight(prevHeight), not EpochForSlot(prevSlot): this resolves
	// WHO WAS ON THE BLOCK-VOTING COMMITTEE at prevHeight (committeeSnapshotFor
	// is SelectionPeriod-keyed, the block-height clock) — a different question
	// from "what entropy epoch was prevSlot in." The two clocks have different
	// divisors; passing the slot-based value here was resolving the wrong
	// committee pool once pinning is live (inert today, same as everywhere
	// else this mismatch was found — see entropy_committee.go's fix).
	aggSig, err := verifyCertAndAggregate(block.PrevAggCert, prevHeight, block.PrevHash.Hex(), EpochForHeight(prevHeight))
	if err != nil {
		log.Error().Err(err).Uint64("height", block.BlockNumber).Uint64("prev_slot", prevSlot).
			Int("signers", len(block.PrevAggCert)).
			Msg("entropy: block's parent certificate failed verification — NOT recorded; if this slot is in a fold window the epoch's fallback will fail closed, which is correct")
		return
	}
	if err := RecordAggSigForFallback(prevSlot, aggSig); err != nil {
		log.Error().Err(err).Uint64("prev_slot", prevSlot).
			Msg("entropy: derived aggregate rejected by the fallback store")
	}
}

// verifyCertAndAggregate checks every signer against the eligible pool and the
// previous block's vote message, then aggregates. Fail-closed throughout.
func verifyCertAndAggregate(cert []config.CertSigner, prevHeight uint64, prevHashHex string, epoch uint64) ([]byte, error) {
	if len(cert) == 0 {
		return nil, fmt.Errorf("entropy: empty certificate")
	}
	snap, err := committeeSnapshotFor(epoch)
	if err != nil {
		return nil, fmt.Errorf("entropy: resolving eligible pool for epoch %d: %w", epoch, err)
	}
	eligible := make(map[string][]byte, len(snap.Members))
	for _, m := range snap.Members {
		eligible[m.PeerID] = m.BLSPub
	}

	// The parent's certifier signatures are over the parent's canonical vote
	// message. When the parent block carried a ConsensusHash (its committee voted
	// v4), that message is v4 (chainID, prevHeight, prevHash, prevConsensusHash,
	// vote=+1); otherwise v3. The whole certificate is one version — every buddy
	// signed the same request's consensus_hash — so building ONE message keeps the
	// BLS aggregate over a single message, which aggregate verification requires.
	// Resolve the version from the parent block itself (its ConsensusHash).
	prevConsensusHashHex := ""
	if pblk, perr := DB_OPs.GetZKBlockByNumber(nil, prevHeight); perr == nil && pblk != nil {
		prevConsensusHashHex = pblk.ConsensusHashHex()
	}
	var msg []byte
	if prevConsensusHashHex != "" {
		msg, err = BLS_Signer.CanonicalVoteMessageV4(BLS_Signer.DomainChainID(), prevHeight, prevHashHex, prevConsensusHashHex, 1)
	} else {
		msg, err = BLS_Signer.CanonicalVoteMessageV3(BLS_Signer.DomainChainID(), prevHeight, prevHashHex, 1)
	}
	if err != nil {
		return nil, fmt.Errorf("entropy: building parent vote message: %w", err)
	}

	seenPeer := make(map[string]struct{}, len(cert))
	seenPub := make(map[string]struct{}, len(cert))
	sigs := make([][]byte, 0, len(cert))

	for _, s := range cert {
		wantPub, ok := eligible[s.PeerID]
		if !ok {
			return nil, fmt.Errorf("entropy: signer %s is not in the eligible pool for epoch %d", s.PeerID, epoch)
		}

		if _, dup := seenPeer[s.PeerID]; dup {
			return nil, fmt.Errorf("entropy: duplicate signer %s", s.PeerID)
		}
		if _, dup := seenPub[s.PubKey]; dup {
			return nil, fmt.Errorf("entropy: duplicate committee key for signer %s", s.PeerID)
		}
		seenPeer[s.PeerID] = struct{}{}
		seenPub[s.PubKey] = struct{}{}

		pubBytes, err := hex.DecodeString(s.PubKey)
		if err != nil {
			return nil, fmt.Errorf("entropy: signer %s pubkey hex: %w", s.PeerID, err)
		}
		// The peerID-to-key binding is what stops an eligible peer voting with
		// a foreign key, exactly as the vote tally already requires.
		if len(wantPub) > 0 && !bytes.Equal(wantPub, pubBytes) {
			return nil, fmt.Errorf("entropy: signer %s declared a pubkey that is not its registered committee key", s.PeerID)
		}
		sigBytes, err := hex.DecodeString(s.Signature)
		if err != nil {
			return nil, fmt.Errorf("entropy: signer %s signature hex: %w", s.PeerID, err)
		}
		if err := blssign.BLSVerify(pubBytes, msg, sigBytes); err != nil {
			return nil, fmt.Errorf("entropy: signer %s signature does not verify over the parent's vote message: %w", s.PeerID, err)
		}
		sigs = append(sigs, sigBytes)
	}

	agg, err := blssign.BLSAggregate(sigs...)
	if err != nil {
		return nil, fmt.Errorf("entropy: aggregating %d signatures: %w", len(sigs), err)
	}
	return agg, nil
}
