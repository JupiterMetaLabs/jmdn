package messaging

// Certified blocks and the "finalized" head, for external consumers (the
// bridge relayer, explorers) over RPC. See docs/finality.md.
//
// Everything here is READ-ONLY and STRICT: a certificate counts only when it
// verifies against the chain-anchored committee seated for the block's own
// (height, period), with no trusted-sequencer relaxation and no legacy or
// pool-wide fallback. Anything this node cannot verify that way is not
// certified, so "finalized" fails closed: it lags, it never overstates.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/DB_OPs"
	"gossipnode/config"
)

var (
	// ErrNoCertificate: the stored block carries no committee certificate.
	ErrNoCertificate = errors.New("finality: block has no committee certificate")
	// ErrFinalityNotAnchored: the block's committee is not chain-anchored, so
	// its certificate cannot be checked against a committee this node can prove.
	ErrFinalityNotAnchored = errors.New("finality: the block's committee is not chain-anchored; its certificate cannot be verified strictly")
	// ErrNotFinalized: no block within the search window has a verified
	// certificate and a verified, linked child.
	ErrNotFinalized = errors.New("finality: no finalized block within the search window")
)

// FinalizedSearchWindow bounds how far below the tip FinalizedHeight looks.
// Normally the answer is tip-1; a deeper gap means certificates are failing
// to verify, and the caller should see an error rather than wait on a scan.
const FinalizedSearchWindow = 64

// Seams for tests: the block read and the certificate check.
var (
	finalityBlockFn = func(height uint64) (*config.ZKBlock, error) {
		return DB_OPs.GetZKBlockHeaderByNumber(height)
	}
	finalityVerifyFn = VerifyFinalityCertificate
	finalityTipFn    = func() (uint64, error) {
		return DB_OPs.GetLatestBlockNumber(context.Background(), nil)
	}
)

// VerifyFinalityCertificate verifies block's stored certificate strictly:
// against the anchored committee seated for the block's (height, period)
// only. It never relaxes to the trusted-sequencer threshold and never falls
// back to the legacy or pool-wide tallies VerifySyncedBlockCertificate allows
// on unanchored periods.
func VerifyFinalityCertificate(block *config.ZKBlock) (CertificateResult, error) {
	if block == nil {
		return CertificateResult{}, errors.New("finality: nil block")
	}
	raw := strings.TrimSpace(block.CommitteeCertificate)
	if raw == "" {
		return CertificateResult{}, ErrNoCertificate
	}
	if !CommitteeV2Enabled || !periodIsAnchored(EpochForHeight(block.BlockNumber)) {
		return CertificateResult{}, ErrFinalityNotAnchored
	}
	if IsCompactCertificate(raw) {
		return verifyCompactCertificate(block, raw)
	}
	var responses []BLS_Signer.BLSresponse
	if err := json.Unmarshal([]byte(raw), &responses); err != nil {
		return CertificateResult{}, fmt.Errorf("finality: malformed certificate: %w", err)
	}
	return VerifyCertificateForRound(responses, block.BlockHash.Hex(), block.ConsensusHashHex(), block.BlockNumber, RoundContextForBlock(block))
}

// CertifiedBlock is a stored block with the outcome of its strict
// certificate check.
type CertifiedBlock struct {
	Block     *config.ZKBlock
	Result    CertificateResult
	Certified bool  // Result.Reached with no error
	VerifyErr error // why it is not certified, when it is not
}

// certifiedCache remembers blocks whose certificate verified strictly. A
// stored block and its certificate do not change, so a (height, BlockHash)
// that verified once stays verified; only positive results are kept, because
// a failure can be transient (an anchor or committee source not yet
// available). Cleared wholesale when full: it is a cost bound, not state.
var certifiedCache = struct {
	sync.Mutex
	m map[certKey]CertificateResult
}{m: make(map[certKey]CertificateResult)}

type certKey struct {
	height uint64
	hash   common.Hash
}

const certifiedCacheMax = 4096

// finalizedAtTip caches FinalizedHeight for the tip it was computed at, so a
// client polling "finalized" costs one computation per new block (see
// notFinalizedTTL for the negative answer).
var finalizedAtTip struct {
	sync.Mutex
	valid  bool
	tip    uint64
	height uint64
	err    error
	at     time.Time
}

// notFinalizedTTL is how long a "nothing finalized" answer is reused at an
// unchanged tip. A finalized height cannot change without a new block, so a
// success is kept for the whole tip; a negative can turn positive at the same
// tip once a committee source or anchor that was missing becomes available
// (the reason certifiedCache keeps positives only), so it is reused only
// briefly, enough to spare a busy poller the window walk on every call.
var notFinalizedTTL = time.Second

// verifyFinalityCached is finalityVerifyFn behind certifiedCache.
func verifyFinalityCached(blk *config.ZKBlock) (CertificateResult, error) {
	k := certKey{blk.BlockNumber, blk.BlockHash}
	certifiedCache.Lock()
	res, ok := certifiedCache.m[k]
	certifiedCache.Unlock()
	if ok {
		return res, nil
	}
	res, err := finalityVerifyFn(blk)
	if err == nil && res.Reached {
		certifiedCache.Lock()
		if len(certifiedCache.m) >= certifiedCacheMax {
			certifiedCache.m = make(map[certKey]CertificateResult)
		}
		certifiedCache.m[k] = res
		certifiedCache.Unlock()
	}
	return res, err
}

// LoadCertifiedBlock reads the block at height and verifies its certificate
// strictly. A missing block is an error; an unverifiable certificate is not
// (Certified is false and VerifyErr says why).
func LoadCertifiedBlock(height uint64) (*CertifiedBlock, error) {
	blk, err := finalityBlockFn(height)
	if err != nil {
		return nil, fmt.Errorf("finality: read block %d: %w", height, err)
	}
	if blk == nil {
		return nil, fmt.Errorf("finality: block %d not found", height)
	}
	res, verr := verifyFinalityCached(blk)
	if verr == nil && !res.Reached {
		verr = fmt.Errorf("finality: certificate below quorum (%d of %d required)", res.YesVotes, res.Threshold)
	}
	return &CertifiedBlock{Block: blk, Result: res, Certified: verr == nil, VerifyErr: verr}, nil
}

// IsFinalized reports whether the block at height is finalized on this node:
// it has a verified certificate, and the stored block at height+1 has a
// verified certificate and names it as parent (PrevHash).
func IsFinalized(height uint64) (bool, error) {
	parent, err := LoadCertifiedBlock(height)
	if err != nil || !parent.Certified {
		return false, err
	}
	child, err := LoadCertifiedBlock(height + 1)
	if err != nil {
		// No child yet is not an error for the question asked.
		return false, nil //nolint:nilerr // absence of a child means "not finalized", not a failure.
	}
	return child.Certified && child.Block.PrevHash == parent.Block.BlockHash, nil
}

// FinalizedHeight returns the highest height this node holds that is
// finalized in the sense of IsFinalized, searching at most
// FinalizedSearchWindow heights below the tip. ErrNotFinalized when none is.
// The answer is cached per tip, and the search stops when ctx is done.
func FinalizedHeight(ctx context.Context) (uint64, error) {
	tip, err := finalityTipFn()
	if err != nil {
		return 0, fmt.Errorf("finality: read tip: %w", err)
	}
	finalizedAtTip.Lock()
	if finalizedAtTip.valid && finalizedAtTip.tip == tip &&
		(finalizedAtTip.err == nil || time.Since(finalizedAtTip.at) < notFinalizedTTL) {
		h, ferr := finalizedAtTip.height, finalizedAtTip.err
		finalizedAtTip.Unlock()
		return h, ferr
	}
	finalizedAtTip.Unlock()
	h, ferr := finalizedHeightAt(ctx, tip)
	if ctx.Err() == nil && (ferr == nil || errors.Is(ferr, ErrNotFinalized)) {
		finalizedAtTip.Lock()
		finalizedAtTip.valid, finalizedAtTip.tip, finalizedAtTip.height, finalizedAtTip.err = true, tip, h, ferr
		finalizedAtTip.at = time.Now()
		finalizedAtTip.Unlock()
	}
	return h, ferr
}

func finalizedHeightAt(ctx context.Context, tip uint64) (uint64, error) {
	if tip == 0 {
		return 0, ErrNotFinalized
	}
	// The child of h must be stored, so the highest candidate is tip-1.
	child, cerr := LoadCertifiedBlock(tip)
	for h := tip; h > 0 && tip-h < FinalizedSearchWindow; h-- {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		parent, perr := LoadCertifiedBlock(h - 1)
		if perr != nil {
			return 0, perr
		}
		if cerr == nil && child.Certified && parent.Certified && child.Block.PrevHash == parent.Block.BlockHash {
			return h - 1, nil
		}
		child, cerr = parent, nil
	}
	return 0, ErrNotFinalized
}

// CertifiedBlockReport is what jmdt_getCertifiedBlock returns for one height:
// everything a third party needs to re-verify the certificate itself.
type CertifiedBlockReport struct {
	Number        uint64 `json:"number"`
	BlockHash     string `json:"blockHash"`
	ConsensusHash string `json:"consensusHash"`
	PrevHash      string `json:"prevHash"`
	Period        uint64 `json:"period"`
	Slot          uint64 `json:"slot"`
	// SelectionPeriod and EntropyEpoch identify the committee the certificate
	// was signed under (RoundContextForBlock).
	SelectionPeriod uint64 `json:"selectionPeriod"`
	EntropyEpoch    uint64 `json:"entropyEpoch"`
	ChainID         uint64 `json:"chainId"`
	// VoteMessage is the exact message every +1 signature signs
	// (BLS_Signer.CanonicalVoteMessageV3 over chain id, height, BlockHash and
	// ConsensusHash).
	VoteMessage string `json:"voteMessage,omitempty"`
	// CertificateFormat is "compact" (aggregate signature + signer bitmap over
	// the anchored pool) or "full" (the list of individual responses).
	CertificateFormat string `json:"certificateFormat"`
	Certificate       string `json:"certificate"`
	// Signers are the peer IDs the certificate counts, with the BLS keys bound
	// to them in the anchored pool (compact form) or carried in the responses
	// (full form).
	Signers []CertSignerReport `json:"signers"`
	// Committee is the seated committee for this block's (height, period),
	// empty when it cannot be derived on this node.
	Committee   []CertSignerReport `json:"committee"`
	Threshold   int                `json:"threshold"`
	YesVotes    int                `json:"yesVotes"`
	Certified   bool               `json:"certified"`
	VerifyError string             `json:"verifyError,omitempty"`
	Finalized   bool               `json:"finalized"`
}

// CertSignerReport is one signer or committee seat.
type CertSignerReport struct {
	PeerID string `json:"peerId"`
	BLSPub string `json:"blsPub,omitempty"`
}

// BuildCertifiedBlockReport reads the block at height, verifies its
// certificate strictly and reports it. Certified and Finalized are false, with
// VerifyError set, whenever the certificate cannot be verified against the
// anchored committee: the report is still returned so the caller can see why.
func BuildCertifiedBlockReport(height uint64) (*CertifiedBlockReport, error) {
	cb, err := LoadCertifiedBlock(height)
	if err != nil {
		return nil, err
	}
	b := cb.Block
	rc := RoundContextForBlock(b)
	raw := strings.TrimSpace(b.CommitteeCertificate)
	rep := &CertifiedBlockReport{
		Number:          b.BlockNumber,
		BlockHash:       b.BlockHash.Hex(),
		ConsensusHash:   b.ConsensusHashHex(),
		PrevHash:        b.PrevHash.Hex(),
		Period:          b.Period,
		Slot:            b.Slot,
		SelectionPeriod: uint64(rc.SelectionPeriod),
		EntropyEpoch:    uint64(rc.EntropyEpoch),
		ChainID:         BLS_Signer.DomainChainID(),
		Certificate:     raw,
		Threshold:       cb.Result.Threshold,
		YesVotes:        cb.Result.YesVotes,
		Certified:       cb.Certified,
		Signers:         []CertSignerReport{},
		Committee:       []CertSignerReport{},
	}
	if cb.VerifyErr != nil {
		rep.VerifyError = cb.VerifyErr.Error()
	}
	if msg, merr := BLS_Signer.CanonicalVoteMessageV3(rep.ChainID, b.BlockNumber, rep.BlockHash, rep.ConsensusHash, 1); merr == nil {
		rep.VoteMessage = "0x" + hex.EncodeToString(msg)
	}
	switch {
	case raw == "":
		rep.CertificateFormat = ""
	case IsCompactCertificate(raw):
		rep.CertificateFormat = "compact"
		if d, derr := decodeCompactCert(raw, b.BlockNumber); derr == nil {
			for i, pid := range d.signers {
				rep.Signers = append(rep.Signers, CertSignerReport{PeerID: pid, BLSPub: "0x" + hex.EncodeToString(d.keys[i])})
			}
		}
	default:
		rep.CertificateFormat = "full"
		var responses []BLS_Signer.BLSresponse
		if json.Unmarshal([]byte(raw), &responses) == nil {
			for _, r := range responses {
				if r.Agree {
					rep.Signers = append(rep.Signers, CertSignerReport{PeerID: r.PeerID, BLSPub: r.PubKey})
				}
			}
		}
	}
	if CommitteeV2Enabled {
		if seated, serr := SelectCommittee(rc); serr == nil {
			for _, m := range seated {
				rep.Committee = append(rep.Committee, CertSignerReport{PeerID: m.PeerID, BLSPub: "0x" + hex.EncodeToString(m.BLSPub)})
			}
		}
	}
	if cb.Certified {
		fin, ferr := IsFinalized(height)
		if ferr == nil {
			rep.Finalized = fin
		}
	}
	return rep, nil
}
