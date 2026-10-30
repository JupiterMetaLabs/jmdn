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
	"encoding/json"
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
// This adds a ConsensusHash-covered field to blocks, so turning it on is a
// coordinated fleet rollout, not a per-node
// choice. With it off, producers attach no certificate and verifiers ignore
// any they receive, so a mixed fleet cannot split.
var AggCertEnabled = os.Getenv("JMDN_AVC_AGG_CERT") == "1"

// certForNextBlock holds the certificate this node will attach to the block it
// proposes next — the certificate of the block it just committed.
//
// Set by RecordCommitCertificate (called from the sequencer's commit path),
// read by CertificateForBlockAssembly (called from attachAVCConsensusFields).
// One value, not a map: only the immediately-previous block's certificate is
// ever attached, so there is nothing to key or evict. Lost on restart;
// CertificateForBlockAssembly then rebuilds it from the parent's persisted
// committee certificate.
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
	cert := certificateFromResponses(responses)

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
// ride on the blocks that follow them: the carrier sits at S+1, since a
// block's slot is its block number whatever its Period.
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
	if certForNextHeight == prevHeight && len(certForNextBlock) > 0 {
		out := make([]config.CertSigner, len(certForNextBlock))
		copy(out, certForNextBlock)
		certForNextBlockMu.Unlock()
		return out
	}
	certForNextBlockMu.Unlock()

	// Memory does not hold the parent's certificate: this sequencer restarted,
	// or took over from another one, after the parent committed. Rebuild it
	// from the parent's persisted committee certificate. Without this every
	// window block it proposed carried none, and five missing certificates in
	// a window leave the epoch with no fallback seed, so the chain halted at
	// the next epoch boundary. Every node still verifies what is attached
	// (VerifyAndRecordPrevCert); a read or parse failure attaches nothing,
	// as before.
	raw, err := persistedCommitCertificateFn(prevHeight)
	if err != nil || raw == "" {
		return nil
	}
	var responses []BLS_Signer.BLSresponse
	if err := json.Unmarshal([]byte(raw), &responses); err != nil {
		return nil
	}
	cert := certificateFromResponses(responses)
	if len(cert) == 0 {
		return nil
	}
	return cert
}

// certificateFromResponses filters to YES votes and sorts by peer ID, so two
// nodes holding the same responses produce a byte-identical certificate —
// required, since the list is hash-covered in array order.
func certificateFromResponses(responses []BLS_Signer.BLSresponse) []config.CertSigner {
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
	return cert
}

// persistedCommitCertificateFn returns the committee certificate (the
// verified []BLSresponse as JSON) stored with the block at height, or "" when
// that block is not stored or carries none. A variable so tests need no DB.
var persistedCommitCertificateFn = func(height uint64) (string, error) {
	blk, err := DB_OPs.GetZKBlockHeaderByNumber(height)
	if err != nil {
		if DB_OPs.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	if blk == nil {
		return "", nil
	}
	return blk.CommitteeCertificate, nil
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
	// The parent's slot is block.Slot-1: a block's slot is its block number
	// (slot_store.go), whatever its Period, and verifiers enforce exactly that
	// (linkageDecision, CheckSyncedSlot).
	prevSlot := block.Slot - 1
	prevHeight := block.BlockNumber - 1

	// EpochForHeight(prevHeight), not EpochForSlot(prevSlot): this resolves
	// WHO WAS ON THE BLOCK-VOTING COMMITTEE at prevHeight (fleetCommitteeSnapshotFor
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

// getParentBlockForEntropy resolves the parent block verifyCertAndAggregate
// needs its real ConsensusHash from. A package-level var, not a hardcoded
// call, so tests can substitute a fake chain — the same seam pattern already
// used elsewhere in this package (e.g. authorizedCommitteeFn) — instead of
// requiring a live DB handle just to exercise this fail-closed path.
// Production always uses the real DB_OPs lookup.
var getParentBlockForEntropy = DB_OPs.GetZKBlockByNumber

// verifyCertAndAggregate checks every signer against the eligible pool and the
// previous block's vote message, then aggregates. Fail-closed throughout.
func verifyCertAndAggregate(cert []config.CertSigner, prevHeight uint64, prevHashHex string, epoch uint64) ([]byte, error) {
	if len(cert) == 0 {
		return nil, fmt.Errorf("entropy: empty certificate")
	}
	// D-36: the pool is resolved WITHOUT this node's block_buddy blocklist.
	//
	// The local (filtered) pool would apply it, and this function derives two
	// fleet-agreed quantities from the result: the aggCertQuorum denominator
	// below, and which signers may be counted. With the filtered pool, one
	// operator's blocklist entry gave n=6/quorum=4 where every peer computed
	// n=7/quorum=5, so this node folded a certificate its peers rejected,
	// derived a different fallback seed, and seated a DIFFERENT COMMITTEE —
	// silently, with no error on either side. Blocking made quorum EASIER,
	// the exact inversion VerifyCertificate's CON-12 comment forbids.
	snap, err := fleetCommitteeSnapshotFor(epoch)
	if err != nil {
		return nil, fmt.Errorf("entropy: resolving eligible pool for epoch %d: %w", epoch, err)
	}
	eligible := make(map[string][]byte, len(snap.Members))
	for _, m := range snap.Members {
		eligible[m.PeerID] = m.BLSPub
	}

	// NO local blocklist here, in the numerator either. The previous version
	// deleted block_buddy entries from `eligible`, which made the signer loop
	// below REJECT THE WHOLE CERTIFICATE on this node whenever a blocked peer
	// had signed it ("not in the eligible pool"). That is not "quorum harder",
	// as it claimed: this function does not accept or reject a block, it
	// decides whether a window slot contributes to the fallback SEED. A slot
	// dropped on one node and kept on its peers shifts FallbackSeedForEpoch's
	// lowest-B subset, so that node derives a different mix, seals a different
	// ENTROPY and seats a different committee - the D-36 divergence through
	// the other door. The block_buddy blocklist is an operator's dialing
	// policy; committed certificate data is judged against the fleet pool.

	// The parent's certifier signatures are over the parent's canonical vote
	// message (chainID, prevHeight, prevHash, prevConsensusHash, vote=+1).
	// Every buddy that certified the parent signed over the SAME
	// ConsensusHash — CanonicalVoteMessageV3 requires one — so this node must
	// recompute the identical hash from the stored parent block to rebuild
	// that message; there is no other source for it.
	//
	// The one known case where the parent has no ConsensusHash is the
	// genesis block (DB_OPs/genesis_block.go: written directly, never through
	// attachAVCConsensusFields), and VerifyAndRecordPrevCert already treats a
	// failure here as fail-closed-and-expected (see its doc comment) — this
	// just skips recording that one fallback-entropy contribution rather than
	// signing over a hash the parent never had.
	pblk, perr := getParentBlockForEntropy(nil, prevHeight)
	if perr != nil || pblk == nil {
		return nil, fmt.Errorf("entropy: loading parent block %d: %w", prevHeight, perr)
	}
	prevConsensusHashHex := pblk.ConsensusHashHex()
	if prevConsensusHashHex == "" {
		return nil, fmt.Errorf("entropy: parent block %d has no consensus hash", prevHeight)
	}
	msg, err := BLS_Signer.CanonicalVoteMessageV3(BLS_Signer.DomainChainID(), prevHeight, prevHashHex, prevConsensusHashHex, 1)
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

	// QUORUM FLOOR (added 2026-09-03). Every check above is per-signer: each
	// signature is valid, each signer is eligible, nobody is counted twice.
	// None of them constrains HOW MANY signers the certificate carries, and
	// before this block the only count check in the whole function was the
	// len(cert) == 0 guard at the top — so a single-signer PrevAggCert passed
	// verification and was folded into the epoch's fallback seed.
	//
	// That matters because the sequencer chooses which qualifying subset of
	// the parent's YES votes to put in PrevAggCert. The block's OWN
	// certificate (config.ZKBlock's committee certificate) is tallied against
	// ByzantineQuorum elsewhere, but nothing tied this field to that one, so
	// the entropy fold accepted a threshold the consensus path would have
	// rejected. Architecture §4.2a reasons about a menu of QUALIFYING
	// subsets; without this check the menu included non-qualifying ones.
	//
	// n is the authenticated committee denominator for the parent's epoch, taken
	// from fleetCommitteeSnapshotFor — the capped eligible set with NO local
	// block_buddy blocklist applied, so a blocklist cannot shrink n and diverge
	// this node's threshold from the fleet's.
	//
	// That last sentence was ASPIRATIONAL until D-36 was fixed: this line used
	// committeeSnapshotFor, whose pool runs through
	// eligibleMembersUncappedForEpoch and therefore did subtract the blocklist.
	// The comment named eligibleMembers as the thing to avoid and then reached
	// the same filter through a different door. If you are changing how snap is
	// resolved, that is the trap to avoid re-entering.
	//
	// Numerator and denominator now come from the same fleet pool, so a
	// certificate verifies identically on every node regardless of any local
	// block_buddy entry.
	//
	// Fail-closed: a certificate below threshold is not recorded, so the epoch's
	// fallback seed fails closed rather than folding a value a minority chose.
	if want := aggCertQuorum(len(snap.Members)); len(sigs) < want {
		return nil, fmt.Errorf("entropy: certificate carries %d valid signers, below the Byzantine quorum %d for a pool of %d (epoch %d)",
			len(sigs), want, len(snap.Members), epoch)
	}

	agg, err := blssign.BLSAggregate(sigs...)
	if err != nil {
		return nil, fmt.Errorf("entropy: aggregating %d signatures: %w", len(sigs), err)
	}
	return agg, nil
}

// aggCertQuorum returns how many valid signers a PrevAggCert must carry for a
// given eligible-pool size. It is the single place that rule is written down.
//
// It is deliberately ByzantineQuorum — the SAME ceil(2n/3) the block's own
// committee certificate is tallied against (consensus_hardening.go) — not a
// second, weaker rule invented for the entropy path. Before 2026-09-03 the
// entropy fold had no count rule at all, so it accepted certificates the
// consensus path would have rejected; the two thresholds being equal is the
// invariant this function exists to keep.
//
// n is capped by consensus.max_validators exactly as the certificate
// denominator is, and MUST be taken from the FLEET-AGREED pool — never from a
// locally-filtered set, because a local blocklist that shrinks n drifts this
// node's threshold away from its peers'.
//
// This function cannot enforce that: it receives an int. The caller's choice of
// snapshot is what makes it true, so the invariant lives at the call site —
// verifyCertAndAggregate uses fleetCommitteeSnapshotFor, and D-36 is the record
// of what happens when it does not (n=6/quorum=4 against a fleet computing
// n=7/quorum=5, hence a different fallback seed and a different committee).
func aggCertQuorum(poolSize int) int {
	n := poolSize
	if lim := committeeSizeLimit(); lim > 0 && lim < n {
		n = lim
	}
	return ByzantineQuorum(n)
}
