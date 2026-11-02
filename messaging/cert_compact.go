package messaging

// Compact committee certificate (stored form v2).
//
// A block's CommitteeCertificate is stored with the block and is not part of
// BlockHash or ConsensusHash. Before v2 it was the full vote list
// ([]BLSresponse as JSON, ~0.5 kB per vote). Every YES vote signs the same
// message (CanonicalVoteMessageV3 has no signer-specific input), and every key
// in the chain-anchored committee pool carries a proof of possession (seed
// committee snapshot), so the YES signatures can be stored as one aggregate:
//
//	{"v":2,"signers":"<bitmap over the sorted anchored pool>","agg_sig":"<64 B>"}
//
// About 150 bytes instead of several kB.
//
// WHEN v2 IS WRITTEN (compactCertEligible): from
// consensus.compact_certificate_from_height on, only for blocks whose selection
// period is chain-anchored (the pool must be reproducible later, by any node,
// to turn the bitmap back into keys), and never for the parent blocks of an
// entropy fallback collection window (a restarted sequencer rebuilds
// PrevAggCert from those parents' individual signatures; see
// CertificateForBlockAssembly).
//
// DETERMINISM: every node derives the stored form from the same vote list with
// no local configuration (no block_buddy blocklist, fleet-agreed pool and
// committee), so the sequencer and every receiver store byte-identical
// certificates and derive identical reward splits from them. If the v2 form
// would not reach quorum on its own (for example, some YES votes do not verify),
// the full list is stored instead, as before.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/JupiterMetaLabs/avc/randao"
	"github.com/JupiterMetaLabs/ion"

	blssign "gossipnode/AVC/BLS/bls-sign"
	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"
	"gossipnode/config/settings"
)

// CompactCertVersion is the "v" of a compact stored certificate.
const CompactCertVersion = 2

type compactCert struct {
	V       int    `json:"v"`
	Signers string `json:"signers"` // base64 bitmap, bit i = i-th peer of the sorted anchored pool
	AggSig  string `json:"agg_sig"` // base64 aggregate of the signers' YES signatures
}

var (
	errCompactNotAnchored = errors.New("compact certificate: block's selection period is not chain-anchored")
	errCompactNoQuorum    = errors.New("compact certificate: verified YES votes do not reach quorum")
)

// compactCertPoolFn returns the fleet-agreed anchored pool (peer_id ->
// bls_pub, no local blocklist) for height; handled=false when the period is
// not anchored. A variable so tests need no anchor records.
var compactCertPoolFn = func(height uint64) (map[string]string, bool, error) {
	return AnchoredPoolForHeight(height, false)
}

// compactCertSeatedFn returns the fleet-agreed seated committee for block
// (peer_id -> bls_pub, no local blocklist): the same set the live verifier
// sizes n and the threshold from. A variable so tests can supply one.
var compactCertSeatedFn = func(block *config.ZKBlock) (map[string]string, error) {
	if CommitteeV2Enabled {
		adoptSyncedBoundaryProof(block)
		seated, err := SelectCommittee(RoundContextForBlock(block))
		if err != nil {
			return nil, err
		}
		out := make(map[string]string, len(seated))
		for _, m := range seated {
			out[m.PeerID] = fmt.Sprintf("%x", m.BLSPub)
		}
		return out, nil
	}
	return authenticatedCommitteeForHeight(block.BlockNumber)
}

// compactCertFromHeight is consensus.compact_certificate_from_height (0 =
// off). A variable so tests can set it.
var compactCertFromHeight = func() uint64 {
	if !settings.IsLoaded() {
		return 0
	}
	return settings.Get().Consensus.CompactCertificateFromHeight
}

// isEntropyWindowParent reports whether height's certificate may be needed
// to rebuild PrevAggCert: the block at height+1 sits in an entropy fallback
// collection range (the same bounds CertificateForBlockAssembly uses).
func isEntropyWindowParent(height uint64) bool {
	slot := height + 1
	start, deadline, err := randao.FallbackCollectionBounds(EpochForSlot(slot), N, RevealCutoffK, FallbackFoldMaxSlotOffset)
	if err != nil {
		return true // cannot tell: keep the full list
	}
	return slot >= start+1 && slot < deadline+1
}

// compactCertEligible reports whether the certificate of the block at height
// is stored in compact form. Depends only on fleet-agreed inputs.
func compactCertEligible(height uint64) bool {
	from := compactCertFromHeight()
	if from == 0 || height < from {
		return false
	}
	if isEntropyWindowParent(height) {
		return false
	}
	_, handled, err := compactCertPoolFn(height)
	return handled && err == nil
}

// IsCompactCertificate reports whether a stored certificate is in compact
// form (a JSON object) rather than the full vote list (a JSON array).
func IsCompactCertificate(raw string) bool {
	return strings.HasPrefix(strings.TrimSpace(raw), "{")
}

// StoredCertificate returns the form of the committee certificate to persist
// with block: compact when eligible and derivable, otherwise the full vote
// list as JSON (unchanged). An empty string when there are no votes or the
// list cannot be encoded.
func StoredCertificate(block *config.ZKBlock, responses []BLS_Signer.BLSresponse) string {
	if len(responses) == 0 || block == nil {
		return ""
	}
	if compactCertEligible(block.BlockNumber) {
		c, err := buildCompactCert(block, responses)
		if err == nil {
			return c
		}
		broadcastLogger().Warn(context.Background(), "compact certificate not derivable; storing the full vote list",
			ion.Uint64("block_number", block.BlockNumber), ion.Err(err))
	}
	full, err := json.Marshal(responses)
	if err != nil {
		return ""
	}
	return string(full)
}

// sortedPool returns the pool's peer IDs in canonical (sorted) order.
func sortedPool(pool map[string]string) []string {
	ids := make([]string, 0, len(pool))
	for pid := range pool {
		ids = append(ids, pid)
	}
	sort.Strings(ids)
	return ids
}

// buildCompactCert derives the compact certificate from the full vote list.
// Signers: YES votes from anchored-pool members whose key matches the pool's
// bound key and whose block-bound signature verifies, one per peer. The result
// must reach quorum over the seated committee on its own.
func buildCompactCert(block *config.ZKBlock, responses []BLS_Signer.BLSresponse) (string, error) {
	pool, handled, err := compactCertPoolFn(block.BlockNumber)
	if err != nil {
		return "", err
	}
	if !handled {
		return "", errCompactNotAnchored
	}
	ids := sortedPool(pool)
	index := make(map[string]int, len(ids))
	for i, pid := range ids {
		index[pid] = i
	}

	blockHashHex, chHex := block.BlockHash.Hex(), block.ConsensusHashHex()
	chainID := BLS_Signer.DomainChainID()
	bitmap := make([]byte, (len(ids)+7)/8)
	var sigs [][]byte
	signers := make(map[string]bool)
	for _, r := range responses {
		if !r.Agree {
			continue
		}
		pid := strings.TrimSpace(r.PeerID)
		i, ok := index[pid]
		if !ok || signers[pid] {
			continue
		}
		bound := normalizeBLSPub(pool[pid])
		if bound == "" || normalizeBLSPub(r.PubKey) != bound {
			continue
		}
		if verifyBlockVote(r, chainID, block.BlockNumber, blockHashHex, chHex) != nil {
			continue
		}
		sig := blsKeyBytes(r.Signature) // hex decode, same rules as keys
		if len(sig) == 0 {
			continue
		}
		signers[pid] = true
		bitmap[i/8] |= 1 << (uint(i) % 8)
		sigs = append(sigs, sig)
	}
	if len(sigs) == 0 {
		return "", errCompactNoQuorum
	}

	seated, err := compactCertSeatedFn(block)
	if err != nil {
		return "", err
	}
	if yes := countSeated(signers, seated); yes < ByzantineQuorum(len(seated)) {
		return "", fmt.Errorf("%w: %d of %d needed over committee %d", errCompactNoQuorum, yes, ByzantineQuorum(len(seated)), len(seated))
	}

	agg, err := blssign.BLSAggregate(sigs...)
	if err != nil {
		return "", fmt.Errorf("compact certificate: aggregate: %w", err)
	}
	out, err := json.Marshal(compactCert{
		V:       CompactCertVersion,
		Signers: base64.StdEncoding.EncodeToString(bitmap),
		AggSig:  base64.StdEncoding.EncodeToString(agg),
	})
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// verifyBlockVote checks one block-bound vote; a seam over the BLS verifier.
var verifyBlockVote = func(r BLS_Signer.BLSresponse, chainID, height uint64, blockHashHex, consensusHashHex string) error {
	msg, err := BLS_Signer.CanonicalVoteMessageV3(chainID, height, blockHashHex, consensusHashHex, 1)
	if err != nil {
		return err
	}
	pub, sig := blsKeyBytes(r.PubKey), blsKeyBytes(r.Signature)
	if len(pub) == 0 || len(sig) == 0 {
		return errors.New("empty key or signature")
	}
	return blssign.BLSVerify(pub, msg, sig)
}

// countSeated counts signers that hold a seat (no local blocklist).
func countSeated(signers map[string]bool, seated map[string]string) int {
	n := 0
	for pid := range signers {
		if _, ok := seated[pid]; ok {
			n++
		}
	}
	return n
}

// decodedCompactCert is a parsed compact certificate.
type decodedCompactCert struct {
	signers []string // peer IDs, pool order
	keys    [][]byte // their bound keys
	agg     []byte
}

// decodeCompactCert parses raw and resolves its signers against the anchored
// pool for height. Fails closed on any malformed or unresolvable part.
func decodeCompactCert(raw string, height uint64) (*decodedCompactCert, error) {
	var c compactCert
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, fmt.Errorf("compact certificate: decode: %w", err)
	}
	if c.V != CompactCertVersion {
		return nil, fmt.Errorf("compact certificate: unknown version %d", c.V)
	}
	bitmap, err := base64.StdEncoding.DecodeString(c.Signers)
	if err != nil {
		return nil, fmt.Errorf("compact certificate: signers: %w", err)
	}
	agg, err := base64.StdEncoding.DecodeString(c.AggSig)
	if err != nil || len(agg) == 0 {
		return nil, fmt.Errorf("compact certificate: aggregate signature missing or malformed")
	}
	pool, handled, err := compactCertPoolFn(height)
	if err != nil {
		return nil, err
	}
	if !handled {
		return nil, errCompactNotAnchored
	}
	ids := sortedPool(pool)
	if len(bitmap) != (len(ids)+7)/8 {
		return nil, fmt.Errorf("compact certificate: bitmap is %d bytes, pool of %d needs %d", len(bitmap), len(ids), (len(ids)+7)/8)
	}
	out := &decodedCompactCert{agg: agg}
	for i := 0; i < len(bitmap)*8; i++ {
		if bitmap[i/8]&(1<<(uint(i)%8)) == 0 {
			continue
		}
		if i >= len(ids) {
			return nil, fmt.Errorf("compact certificate: bit %d set beyond the pool of %d", i, len(ids))
		}
		key := blsKeyBytes(pool[ids[i]])
		if len(key) == 0 {
			return nil, fmt.Errorf("compact certificate: signer %s has no bound key", ids[i])
		}
		out.signers = append(out.signers, ids[i])
		out.keys = append(out.keys, key)
	}
	if len(out.signers) == 0 {
		return nil, errors.New("compact certificate: no signers")
	}
	return out, nil
}

// verifyCompactCertificate verifies a compact certificate for block: the
// aggregate signature over every signer's bound key, then quorum over the
// seated committee. Like every tally, signers on this node's block_buddy
// list are not counted (n still includes their seats).
func verifyCompactCertificate(block *config.ZKBlock, raw string) (CertificateResult, error) {
	var res CertificateResult
	d, err := decodeCompactCert(raw, block.BlockNumber)
	if err != nil {
		return res, err
	}
	msg, err := BLS_Signer.CanonicalVoteMessageV3(BLS_Signer.DomainChainID(), block.BlockNumber, block.BlockHash.Hex(), block.ConsensusHashHex(), 1)
	if err != nil {
		return res, err
	}
	ok, err := blssign.BLSFastAggregateVerify(d.keys, msg, d.agg)
	if err != nil {
		return res, fmt.Errorf("compact certificate: aggregate verify: %w", err)
	}
	if !ok {
		return res, errors.New("compact certificate: aggregate signature does not verify")
	}
	seated, err := compactCertSeatedFn(block)
	if err != nil {
		return res, err
	}
	blocked := blockedBuddies()
	signers := make(map[string]bool, len(d.signers))
	for _, pid := range d.signers {
		if _, isBlocked := blocked[pid]; !isBlocked {
			signers[pid] = true
		}
	}
	res.CommitteeSize = len(seated)
	res.Threshold = ByzantineQuorum(len(seated))
	res.YesVotes = countSeated(signers, seated)
	res.Reached = res.YesVotes >= res.Threshold
	return res, nil
}

// VerifyStoredBlockCertificate verifies a block's persisted certificate in
// either stored form, for the sync path.
func VerifyStoredBlockCertificate(block *config.ZKBlock) (CertificateResult, error) {
	raw := strings.TrimSpace(block.CommitteeCertificate)
	if IsCompactCertificate(raw) {
		return verifyCompactCertificate(block, raw)
	}
	var responses []BLS_Signer.BLSresponse
	if err := json.Unmarshal([]byte(raw), &responses); err != nil {
		return CertificateResult{}, fmt.Errorf("malformed certificate: %w", err)
	}
	return VerifySyncedBlockCertificate(block, responses)
}

// certSignersFromStored returns the YES signers of a stored certificate, one
// per peer: the bitmap's signers for a compact certificate, the Agree entries
// of a full list. Signature is empty for compact certificates.
func certSignersFromStored(raw string, height uint64) ([]config.CertSigner, error) {
	if IsCompactCertificate(raw) {
		d, err := decodeCompactCert(raw, height)
		if err != nil {
			return nil, err
		}
		out := make([]config.CertSigner, 0, len(d.signers))
		for i, pid := range d.signers {
			out = append(out, config.CertSigner{PeerID: pid, PubKey: fmt.Sprintf("%x", d.keys[i])})
		}
		return out, nil
	}
	var responses []BLS_Signer.BLSresponse
	if err := json.Unmarshal([]byte(raw), &responses); err != nil {
		return nil, fmt.Errorf("malformed certificate: %w", err)
	}
	seen := make(map[string]bool, len(responses))
	out := make([]config.CertSigner, 0, len(responses))
	for _, r := range responses {
		if !r.Agree {
			continue
		}
		pid := strings.TrimSpace(r.PeerID)
		if pid == "" || seen[pid] {
			continue
		}
		seen[pid] = true
		out = append(out, config.CertSigner{PeerID: pid, PubKey: r.PubKey, Signature: r.Signature})
	}
	return out, nil
}
