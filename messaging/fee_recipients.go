package messaging

// Buddy staking-reward fee distribution — the SINGLE source of truth shared by
// the sequencer's block build (R4) and every node's receive-path validation
// (R5), so the two can never drift. See docs/STAKING-REWARDS-DESIGN.md.
//
// A block's FeeRecipients is a PURE FUNCTION of already-agreed inputs:
//   - the block's PrevAggCert signers (the buddies who certified the parent),
//   - the authenticated peer_id -> reward-address map from the seed-signed
//     committee snapshot (RewardAddrByPeer), and
//   - each reward address's balance at the CURRENT committed tip (the parent
//     N-1 state, deterministic on both build and admit because both run before
//     block N is applied),
//   - the fleet-uniform config.StakeWeight constants.
//
// Because it is pure, the sequencer has no freedom over the split: a cheating
// sequencer that points fees at itself is rejected fleet-wide by R5's recompute
// (blockPropagation.go). EVERYTHING here is gated OFF by default
// (settings.Consensus.RewardSplitEnabled); with the flag down these functions
// are never invoked on the hot paths and behavior is byte-identical to today.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/config/settings"

	"github.com/ethereum/go-ethereum/common"
)

// rewardSplitEnabled reports whether buddy staking-reward fee distribution is
// on. Reads settings only if they have been loaded (robust to init order,
// mirroring blockedBuddies); before Load() it returns false, so the receive
// path keeps its historical FeeRecipients interlock and the build path never
// populates the field.
func rewardSplitEnabled() bool {
	if !settings.IsLoaded() {
		return false
	}
	return settings.Get().Consensus.RewardSplitEnabled
}

// ---- Reward-address source (peer_id -> reward-address) -----------------------
//
// A PARALLEL seam to SetCommitteeEligibilitySource: the eligibility source
// exposes peer -> bls_pub for vote authentication; this one exposes peer ->
// reward-address for the fee split, resolved from the SAME authenticated,
// seed-signed committee snapshot (committee.CommitteeSnapshot.RewardAddrByPeer).
// Kept a separate seam so seednode need not know about the fee split and so a
// node can wire one without the other.

var (
	rewardAddrSourceMu sync.RWMutex
	rewardAddrSource   func() (map[string]string, error)
)

// SetRewardAddressSource wires the live reward-address source: peer_id ->
// lowercase reward-address hex, from the authenticated committee snapshot
// (RewardAddrByPeer omits peers with no bound address). Pass nil to clear
// (forces fail-closed when reward-split is enabled). Safe to call concurrently.
// Wire it everywhere the eligibility source is wired, from the SAME verified
// snapshot (see main.go and Sequencer/consensus_statemachine.go).
func SetRewardAddressSource(fn func() (map[string]string, error)) {
	rewardAddrSourceMu.Lock()
	rewardAddrSource = fn
	rewardAddrSourceMu.Unlock()
}

// rewardAddressesForBlock resolves the authenticated peer_id -> reward-address
// map. FAIL CLOSED: if no source is wired or it errors, it returns an error so
// the build (R4) and validate (R5) paths abort rather than compute a wrong or
// half split. Callers gate on rewardSplitEnabled() before reaching here, so
// with reward-split OFF an unset source is harmless.
func rewardAddressesForBlock() (map[string]string, error) {
	rewardAddrSourceMu.RLock()
	fn := rewardAddrSource
	rewardAddrSourceMu.RUnlock()

	if fn == nil {
		return nil, fmt.Errorf("reward-address source not configured (fail closed): call messaging.SetRewardAddressSource at startup")
	}
	m, err := fn()
	if err != nil {
		return nil, fmt.Errorf("reward-address source failed: %w", err)
	}
	return m, nil
}

// ---- Parent-state balance read (fail-closed) ---------------------------------

// parentStateBalanceOf reads addr's balance from the CURRENT committed state —
// which is the parent (N-1) state on BOTH the build and the admit paths, since
// both run before block N is applied, so it is deterministic. Used by BOTH R4
// and R5 via ExpectedFeeRecipients so the two paths can never read balances
// differently.
//
// A brand-new address that has never been funded is NOT an error: GetAccount
// reports it as not-found, and a never-funded reward address is a legitimate
// ZERO balance (config.StakeWeight(0) == BaselineWeight, so a zero-balance buddy
// still earns the baseline). Any OTHER read error is returned unchanged (FAIL
// CLOSED — a transient DB failure must never silently become a zero balance).
// This mirrors GetBalanceAtBlock's own not-found handling.
func parentStateBalanceOf(addr common.Address) (*big.Int, error) {
	acct, err := DB_OPs.GetAccount(nil, addr)
	if err != nil {
		if DB_OPs.IsNotFound(err) {
			return new(big.Int), nil // never funded => zero balance => BaselineWeight
		}
		return nil, err // real read error => fail closed
	}
	bal := new(big.Int)
	if s := strings.TrimSpace(acct.Balance); s != "" {
		if _, ok := bal.SetString(s, 10); !ok {
			return nil, fmt.Errorf("parentStateBalanceOf: unparseable balance %q for %s", acct.Balance, addr.Hex())
		}
	}
	return bal, nil
}

// ---- Derivation (pure) -------------------------------------------------------

// DeriveFeeRecipients builds the canonical FeeRecipients for a block from its
// parent certifiers, the authenticated reward-address map, and a balance
// reader. It is a pure, deterministic function of its inputs — the property
// that lets the split be recomputed and validated fleet-wide.
//
// Rules (docs/STAKING-REWARDS-DESIGN.md §4/§5):
//   - a signer whose peer_id has NO bound reward address is OMITTED (its share
//     redistributes among the address-having signers — native SplitFee behavior);
//   - each bound address's weight is config.StakeWeight(parent-state balance);
//   - weights AGGREGATE by address (one address backing several signers sums
//     their weights);
//   - the result is sorted by address bytes (canonical, matching SplitFee's own
//     ordering);
//   - a balance-read error or an unparseable/invalid bound address returns the
//     error (FAIL CLOSED — never treated as zero);
//   - if NO signer has a bound address, an EMPTY slice is returned, so SplitFee
//     falls back to the single coinbase credit (historical behavior).
func DeriveFeeRecipients(
	signers []config.CertSigner,
	rewardByPeer map[string]string,
	balanceOf func(common.Address) (*big.Int, error),
) ([]config.FeeRecipient, error) {
	weightByAddr := make(map[common.Address]uint64)

	for _, s := range signers {
		peer := strings.TrimSpace(s.PeerID)
		if peer == "" {
			continue
		}
		addrHex, ok := rewardByPeer[peer]
		if !ok {
			continue // no bound address => omit; share redistributes
		}
		addrHex = strings.TrimSpace(addrHex)
		if addrHex == "" {
			continue // treat an empty binding exactly like an absent one
		}
		if !common.IsHexAddress(addrHex) {
			return nil, fmt.Errorf("DeriveFeeRecipients: peer %s has invalid bound reward address %q", peer, addrHex)
		}
		addr := common.HexToAddress(addrHex)

		bal, err := balanceOf(addr)
		if err != nil {
			return nil, fmt.Errorf("DeriveFeeRecipients: balance read for %s (peer %s) failed: %w", addr.Hex(), peer, err)
		}
		// StakeWeight is deterministic; two signers backed by the same address
		// read the same balance and their weights sum here.
		weightByAddr[addr] += config.StakeWeight(bal)
	}

	if len(weightByAddr) == 0 {
		// No address-having signer: empty recipients => single coinbase credit.
		return []config.FeeRecipient{}, nil
	}

	out := make([]config.FeeRecipient, 0, len(weightByAddr))
	for addr, w := range weightByAddr {
		out = append(out, config.FeeRecipient{Addr: addr, Weight: w})
	}
	sort.Slice(out, func(i, j int) bool {
		return bytes.Compare(out[i].Addr.Bytes(), out[j].Addr.Bytes()) < 0
	})
	return out, nil
}

// FeeRecipientsEqual reports canonical equality: same length and, after both are
// sorted by address bytes, every (Addr, Weight) matches. Used by R5 so a block's
// FeeRecipients ordering cannot let a mismatched split slip through.
func FeeRecipientsEqual(a, b []config.FeeRecipient) bool {
	if len(a) != len(b) {
		return false
	}
	as := make([]config.FeeRecipient, len(a))
	copy(as, a)
	bs := make([]config.FeeRecipient, len(b))
	copy(bs, b)
	sort.Slice(as, func(i, j int) bool {
		return bytes.Compare(as[i].Addr.Bytes(), as[j].Addr.Bytes()) < 0
	})
	sort.Slice(bs, func(i, j int) bool {
		return bytes.Compare(bs[i].Addr.Bytes(), bs[j].Addr.Bytes()) < 0
	})
	for i := range as {
		if as[i].Addr != bs[i].Addr || as[i].Weight != bs[i].Weight {
			return false
		}
	}
	return true
}

// ExpectedFeeRecipients is the SINGLE entry point both the sequencer (R4, build)
// and the receive-path validator (R5) call, so build and validate cannot drift.
// It resolves the authenticated reward-address map and derives the canonical
// recipients from the given signers using the shared parent-state balance
// reader. Callers MUST have checked rewardSplitEnabled() first. FAIL CLOSED on a
// missing reward source, a balance-read error, or an invalid bound address.
func ExpectedFeeRecipients(signers []config.CertSigner) ([]config.FeeRecipient, error) {
	rewardMap, err := rewardAddressesForBlock()
	if err != nil {
		return nil, err
	}
	return DeriveFeeRecipients(signers, rewardMap, parentStateBalanceOf)
}

// PrevBlockCertSigners returns the YES-voters of block prevNumber's committee
// certificate as a CertSigner list — the buddies who certified the PREVIOUS
// block, which the reward split pays. Sourced from that block's PERSISTED
// CommitteeCertificate (P-cert), which exists on every certified block — NOT
// block.PrevAggCert, which is populated only inside the entropy fold window and
// only when JMDN_AVC_AGG_CERT is on. Because every node reads the same persisted,
// hash-covered certificate for prevNumber, R4 (build) and R5 (validate) derive
// identical recipients, so the sequencer still has no freedom over the split.
//
// A genesis parent (prevNumber == 0), a not-yet-persisted parent, or a cert-less
// (legacy-prefix) parent yields an EMPTY signer set — no split for that block,
// not an error. A real DB read error or a malformed certificate fails closed.
func PrevBlockCertSigners(prevNumber uint64) ([]config.CertSigner, error) {
	if prevNumber == 0 {
		return nil, nil
	}
	blk, err := DB_OPs.GetZKBlockByNumber(nil, prevNumber)
	if err != nil {
		if DB_OPs.IsNotFound(err) {
			return nil, nil // parent not persisted yet => no certifiers to reward
		}
		return nil, fmt.Errorf("PrevBlockCertSigners(%d): %w", prevNumber, err)
	}
	cert := strings.TrimSpace(blk.CommitteeCertificate)
	if cert == "" {
		return nil, nil // legacy/cert-less parent => nobody to reward
	}
	var responses []BLS_Signer.BLSresponse
	if uerr := json.Unmarshal([]byte(cert), &responses); uerr != nil {
		return nil, fmt.Errorf("PrevBlockCertSigners(%d): malformed certificate: %w", prevNumber, uerr)
	}
	seen := make(map[string]bool, len(responses))
	out := make([]config.CertSigner, 0, len(responses))
	for _, r := range responses {
		if !r.Agree { // reward only the YES certifiers
			continue
		}
		pid := strings.TrimSpace(r.PeerID)
		if pid == "" || seen[pid] {
			continue // dedupe by peer_id: one stake weight per certifier
		}
		seen[pid] = true
		out = append(out, config.CertSigner{PeerID: pid, PubKey: r.PubKey, Signature: r.Signature})
	}
	return out, nil
}

// checkFeeRecipients recomputes a received block's expected FeeRecipients from
// the PREVIOUS block's persisted committee certificate (PrevBlockCertSigners)
// and rejects any mismatch. Called from validateRemoteBlock AFTER
// verifyBlockCertificate, only when reward-split is enabled. FAIL CLOSED: a
// derive error is a rejection, not a pass.
func checkFeeRecipients(b *config.ZKBlock) *blockRejection {
	signers, serr := PrevBlockCertSigners(b.BlockNumber - 1)
	if serr != nil {
		return reject("feerecipients_prevcert",
			"block %s: cannot read previous block's certifiers (fail closed): %v", b.BlockHash.Hex(), serr)
	}
	expected, err := ExpectedFeeRecipients(signers)
	if err != nil {
		return reject("feerecipients_underivable",
			"block %s: cannot derive expected fee recipients (fail closed): %v", b.BlockHash.Hex(), err)
	}
	if !FeeRecipientsEqual(b.FeeRecipients, expected) {
		return reject("feerecipients_mismatch",
			"block %s: FeeRecipients do not match the recomputed split (redirect attempt?)", b.BlockHash.Hex())
	}
	return nil
}
