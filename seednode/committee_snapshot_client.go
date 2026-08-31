package seednode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gossipnode/seednode/committee"
	peerpb "gossipnode/seednode/proto"

	"github.com/rs/zerolog/log"
)

// FetchCommitteeSnapshot calls GetCommitteeSnapshot(epoch) on the seed
// (epoch 0 = current) and converts the response into the committee mirror
// struct. It does NOT verify — callers MUST run committee.VerifyCommitteeSnapshot
// against the pinned authority key before trusting the result.
func (c *Client) FetchCommitteeSnapshot(ctx context.Context, epoch uint64) (*committee.CommitteeSnapshot, error) {
	resp, err := c.client.GetCommitteeSnapshot(ctx, &peerpb.GetCommitteeSnapshotRequest{Epoch: epoch})
	if err != nil {
		return nil, fmt.Errorf("get committee snapshot: %w", err)
	}
	if resp == nil || resp.Snapshot == nil {
		return nil, fmt.Errorf("empty committee snapshot response")
	}
	s := resp.Snapshot
	entries := make([]committee.CommitteeEntry, 0, len(s.Entries))
	for _, e := range s.Entries {
		entries = append(entries, committee.CommitteeEntry{PeerID: e.PeerId, BLSPub: e.BlsPub, RewardAddress: e.RewardAddress})
	}
	return &committee.CommitteeSnapshot{
		Epoch:           s.Epoch,
		Entries:         entries,
		Seed:            s.Seed,
		AuthorityPubHex: s.AuthorityPubkey,
		Signature:       s.Signature,
	}, nil
}

// CommitteeEligibility returns a fail-closed eligibility source for
// messaging.SetCommitteeEligibilitySource. Each call fetches the current
// epoch's committee snapshot, verifies its authority signature against the
// PINNED authority key, and returns the eligible peer_id set. Because the
// returned set IS the committee, VerifyCertificate then counts only snapshot
// members — enforcing committee ⊆ snapshot at the tally.
//
// Fail-closed: an unset pin, a fetch error, a signature/pin-mismatch, or an
// empty snapshot all return an error (never "allow all"), so a node with no
// authenticated committee refuses consensus participation.
// (epoch, pinned): pinned=false is the live read and behaves exactly as before.
// pinned=true asks for one specific selection epoch (W1 pool pinning), reached
// only when consensus.require_pinned_committee is set.
func (c *Client) CommitteeEligibility(pinnedAuthorityHex string, epochSeconds int64) func(uint64, bool) (map[string]string, error) {
	return func(epoch uint64, pinned bool) (map[string]string, error) {
		if pinnedAuthorityHex == "" {
			return nil, fmt.Errorf("committee source disabled: no pinned seed authority key (fail closed)")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		want := uint64(0) // 0 = current epoch, per the seed's wire convention
		if pinned {
			want = epoch
		}
		snap, err := c.FetchCommitteeSnapshot(ctx, want)
		if err != nil {
			return nil, err
		}
		if err := committee.VerifyCommitteeSnapshot(snap, pinnedAuthorityHex); err != nil {
			return nil, fmt.Errorf("committee snapshot rejected: %w", err)
		}
		if pinned {
			// The freshness carve-out for historical reads.
			//
			// CheckSnapshotEpochFresh compares against time.Now(), so it rejects
			// every past epoch (a week-old snapshot misses by ~168). But a pinned
			// read NEEDS that snapshot: re-deriving block 95's committee means
			// resolving epoch 9 exactly, whenever we happen to ask.
			//
			// An EXACT epoch match is strictly stronger here, not weaker.
			// Freshness stops an old signed snapshot being re-presented in place
			// of the CURRENT one; an exact match stops ANY snapshot being
			// substituted for the one requested — including the current one. The
			// seed cannot satisfy "epoch 9" with epoch 10.
			if snap.Epoch != epoch {
				return nil, fmt.Errorf(
					"committee snapshot rejected: asked for epoch %d, seed served epoch %d (fail closed)",
					epoch, snap.Epoch)
			}
		} else {
			// Freshness: a valid signature is not enough — reject a stale but
			// authority-signed snapshot (an old, pre-rotation/revocation committee)
			// that could be re-presented over the unauthenticated read.
			if err := committee.CheckSnapshotEpochFresh(snap.Epoch, time.Now().Unix(), epochSeconds); err != nil {
				return nil, fmt.Errorf("committee snapshot rejected: %w", err)
			}
		}
		// peer_id -> authenticated bls_pub, so the verifier can enforce the
		// peer_id↔bls_pub binding, not just membership.
		return snap.BLSPubByPeer(), nil
	}
}

// RewardAddresses is the pinned-authority sibling of CommitteeEligibility for the
// buddy staking-reward fee split: it returns a fail-closed source of the current
// epoch's authenticated peer_id -> reward-address map (RewardAddrByPeer), from
// the SAME seed snapshot, verified against the SAME pinned authority key, with
// the SAME freshness check. Wire it into messaging.SetRewardAddressSource on the
// sequencer (Sequencer/consensus_statemachine.go) alongside CommitteeEligibility,
// so R4 build and R5 validate derive the split from the same authenticated
// source. Fail-closed: an unset pin, a fetch error, a signature/pin-mismatch, or
// a stale snapshot all return an error.
func (c *Client) RewardAddresses(pinnedAuthorityHex string, epochSeconds int64) func() (map[string]string, error) {
	return func() (map[string]string, error) {
		if pinnedAuthorityHex == "" {
			return nil, fmt.Errorf("reward-address source disabled: no pinned seed authority key (fail closed)")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		snap, err := c.FetchCommitteeSnapshot(ctx, 0) // 0 = current epoch
		if err != nil {
			return nil, err
		}
		if err := committee.VerifyCommitteeSnapshot(snap, pinnedAuthorityHex); err != nil {
			return nil, fmt.Errorf("committee snapshot rejected: %w", err)
		}
		if err := committee.CheckSnapshotEpochFresh(snap.Epoch, time.Now().Unix(), epochSeconds); err != nil {
			return nil, fmt.Errorf("committee snapshot rejected: %w", err)
		}
		return snap.RewardAddrByPeer(), nil
	}
}

// ---- Auto (pin-or-TOFU) committee source with caching ------------------------
//
// CommitteeEligibility above requires an operator to PIN the authority key. On
// nodes that are not operated by the network owner (open-source validators) we
// resolve the authority key without a manual pin, using trust-on-first-use:
// adopt the key the seed's first snapshot carries, persist it, and enforce it on
// every later snapshot. A configured pin always overrides TOFU. The verified
// snapshot is cached with a short TTL so the seed is queried roughly once per
// refresh window (not once per certificate check), and a still-fresh cached
// snapshot is served if the seed is briefly unreachable — otherwise fail closed.

// defaultSeedAuthFile is the relative TOFU pin path, mirroring the relative
// config/bls.json convention (env-overridable for systemd working-dir setups).
const defaultSeedAuthFile = "./config/seedAuth.json"

// SeedAuthPinPath returns the TOFU authority pin file path: $JMDN_SEED_AUTH_FILE
// if set, else ./config/seedAuth.json.
func SeedAuthPinPath() string {
	if p := strings.TrimSpace(os.Getenv("JMDN_SEED_AUTH_FILE")); p != "" {
		return p
	}
	return defaultSeedAuthFile
}

// persistedAuthority is the on-disk TOFU record. Only a PUBLIC key is stored;
// it is not a secret. `seed` and `adopted_at` are informational.
type persistedAuthority struct {
	AuthorityPubHex string `json:"authority_pubkey"`
	Seed            string `json:"seed,omitempty"`
	AdoptedAt       int64  `json:"adopted_at"`
}

func normAuthorityHex(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func loadPersistedAuthority(path string) (persistedAuthority, error) {
	var rec persistedAuthority
	b, err := os.ReadFile(path)
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return rec, err
	}
	return rec, nil
}

func savePersistedAuthority(path string, rec persistedAuthority) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// committeeSource holds the resolver + cache state for one auto eligibility
// source. Its eligible method is what SetCommitteeEligibilitySource receives.
type committeeSource struct {
	fetch        func(ctx context.Context, epoch uint64) (*committee.CommitteeSnapshot, error)
	configPin    string // operator pin; overrides TOFU when non-empty
	epochSeconds int64
	pinFile      string // TOFU persistence path ("" disables persistence)
	seedURL      string // informational, recorded in the pin file
	ttl          time.Duration

	// strictBoundary controls serveLastGoodOr across an epoch change. See
	// consensus.committee_strict_boundary and serveLastGoodOr.
	strictBoundary bool

	mu         sync.Mutex
	authority  string                       // resolved authority pubkey (lowercased)
	cachedSnap *committee.CommitteeSnapshot // last authenticated + fresh snapshot
	cachedAt   time.Time
}

// CommitteeEligibilityAuto returns a fail-closed eligibility source that resolves
// the authority key by pin-or-TOFU (see the block comment above) and caches the
// verified snapshot for ttl. Use on non-sequencer nodes so the mandatory block
// certificate check has a committee source instead of failing closed.
// strictBoundary=false preserves today's behaviour exactly.
func (c *Client) CommitteeEligibilityAuto(configPin string, epochSeconds int64, pinFile, seedURL string, ttl time.Duration, strictBoundary bool) func(uint64, bool) (map[string]string, error) {
	eligibility, _ := c.CommitteeSourcesAuto(configPin, epochSeconds, pinFile, seedURL, ttl, strictBoundary)
	return eligibility
}

// CommitteeSourcesAuto is CommitteeEligibilityAuto plus a sibling reward-address
// accessor bound to the SAME committeeSource (the SAME pin-or-TOFU authority and
// the SAME cached, verified snapshot). Wire the first closure into
// messaging.SetCommitteeEligibilitySource and the second into
// messaging.SetRewardAddressSource, so buddy staking-reward fee distribution
// (R4 build + R5 validate) is derived from the identical authenticated snapshot
// as vote eligibility and never drifts from it. strictBoundary=false preserves
// today's behaviour exactly.
func (c *Client) CommitteeSourcesAuto(configPin string, epochSeconds int64, pinFile, seedURL string, ttl time.Duration, strictBoundary bool) (
	eligibility func(uint64, bool) (map[string]string, error),
	rewardAddresses func() (map[string]string, error),
) {
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	s := &committeeSource{
		fetch:          c.FetchCommitteeSnapshot,
		configPin:      configPin,
		epochSeconds:   epochSeconds,
		pinFile:        pinFile,
		seedURL:        seedURL,
		ttl:            ttl,
		strictBoundary: strictBoundary,
	}
	return s.eligible, s.rewardAddresses
}

// resolveAuthority resolves the authority pubkey once (config pin, else persisted
// TOFU pin, else adopt-and-persist the seed's first snapshot key). Caller holds
// s.mu.
func (s *committeeSource) resolveAuthority(ctx context.Context) (string, error) {
	if s.authority != "" {
		return s.authority, nil
	}
	// 1) operator pin always wins (strongest; protects first boot too).
	if p := normAuthorityHex(s.configPin); p != "" {
		s.authority = p
		return p, nil
	}
	// 2) previously-adopted TOFU key from disk.
	if s.pinFile != "" {
		if rec, err := loadPersistedAuthority(s.pinFile); err == nil {
			if k := normAuthorityHex(rec.AuthorityPubHex); k != "" {
				s.authority = k
				return k, nil
			}
		}
	}
	// 3) first use — adopt the key the seed's snapshot carries. Self-verify the
	// snapshot signs against its own claimed key, then persist that key.
	snap, err := s.fetch(ctx, 0)
	if err != nil {
		return "", fmt.Errorf("TOFU: first committee snapshot fetch failed: %w", err)
	}
	if err := committee.VerifyCommitteeSnapshot(snap, ""); err != nil {
		return "", fmt.Errorf("TOFU: first committee snapshot self-verify failed: %w", err)
	}
	adopted := normAuthorityHex(snap.AuthorityPubHex)
	if adopted == "" {
		return "", fmt.Errorf("TOFU: committee snapshot carries no authority key")
	}
	if s.pinFile != "" {
		if err := savePersistedAuthority(s.pinFile, persistedAuthority{
			AuthorityPubHex: adopted, Seed: s.seedURL, AdoptedAt: time.Now().Unix(),
		}); err != nil {
			// Non-fatal: adopt in-memory this run, re-adopt next start.
			log.Warn().Err(err).Str("file", s.pinFile).
				Msg("committee TOFU: failed to persist adopted authority key (will re-adopt on restart)")
		} else {
			log.Warn().Str("authority", adopted).Str("file", s.pinFile).
				Msg("committee TOFU: adopted seed authority key on first use and persisted it")
		}
	}
	s.authority = adopted
	return adopted, nil
}

// eligible fetches (or serves cached) the authenticated, epoch-fresh committee as
// a peer_id -> bls_pub map. FAIL CLOSED: a fetch/verify/freshness failure returns
// an error unless a still-epoch-fresh cached snapshot can bridge a transient seed
// outage.
func (s *committeeSource) eligible(epoch uint64, pinned bool) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if pinned {
		// PINNED (W1) read: one specific selection epoch.
		//
		// No TTL cache and no serveLastGoodOr on this path, deliberately. The
		// cache holds the CURRENT snapshot; serving it for a request that named a
		// past epoch is exactly the substitution this whole change exists to
		// prevent, and it would be invisible. A pinned read either resolves the
		// epoch it asked for, or fails.
		authority, err := s.resolveAuthority(ctx)
		if err != nil {
			return nil, err
		}
		snap, err := s.fetch(ctx, epoch)
		if err != nil {
			return nil, err
		}
		if err := committee.VerifyCommitteeSnapshot(snap, authority); err != nil {
			return nil, fmt.Errorf("committee snapshot rejected: %w", err)
		}
		// Exact match replaces wall-clock freshness for historical reads — see
		// the same carve-out in CommitteeEligibility for why it is stronger.
		if snap.Epoch != epoch {
			return nil, fmt.Errorf(
				"committee snapshot rejected: asked for epoch %d, seed served epoch %d (fail closed)",
				epoch, snap.Epoch)
		}
		return snap.BLSPubByPeer(), nil
	}

	// Fast path: a recently-verified snapshot within the TTL.
	if s.cachedSnap != nil && time.Since(s.cachedAt) < s.ttl {
		return s.cachedSnap.BLSPubByPeer(), nil
	}

	authority, err := s.resolveAuthority(ctx)
	if err != nil {
		return s.serveLastGoodOr(err)
	}
	snap, err := s.fetch(ctx, 0) // 0 = current epoch
	if err != nil {
		return s.serveLastGoodOr(err)
	}
	if err := committee.VerifyCommitteeSnapshot(snap, authority); err != nil {
		return s.serveLastGoodOr(fmt.Errorf("committee snapshot rejected: %w", err))
	}
	if err := committee.CheckSnapshotEpochFresh(snap.Epoch, time.Now().Unix(), s.epochSeconds); err != nil {
		return s.serveLastGoodOr(fmt.Errorf("committee snapshot rejected: %w", err))
	}
	s.cachedSnap = snap
	s.cachedAt = time.Now()
	return snap.BLSPubByPeer(), nil
}

// serveLastGoodOr returns the last cached snapshot if it is still epoch-fresh,
// else err. Lets a brief seed outage not drop blocks while the previously
// authenticated committee remains valid; fails closed once it goes stale. Caller
// holds s.mu.
// THE BOUNDARY SPLIT, and what strictBoundary does about it.
//
// The freshness window is ±1 epoch, so just after an epoch 9->10 change a node
// that cannot reach the seed serves its cached EPOCH 9 set and passes freshness
// (|9-10| = 1), while a node that fetched successfully uses EPOCH 10. Both
// believe they are correct. Different sets => different n => different
// T = ceil(2n/3). One finalises a certificate the other rejects.
//
// That is tolerable while the pool is unpinned and static; it is not tolerable
// once membership actually rotates per epoch. strictBoundary=true refuses to
// bridge ACROSS an epoch change (it still bridges a seed blip WITHIN the same
// epoch, which is what this helper was written for).
//
// The trade is deliberate: a node that stalls rejoins cleanly, a node that
// splits does not. Default false = today's behaviour.
func (s *committeeSource) serveLastGoodOr(err error) (map[string]string, error) {
	if s.cachedSnap != nil &&
		committee.CheckSnapshotEpochFresh(s.cachedSnap.Epoch, time.Now().Unix(), s.epochSeconds) == nil {
		if s.strictBoundary {
			if cur := committee.EpochForTime(time.Now().Unix(), s.epochSeconds); cur != s.cachedSnap.Epoch {
				log.Error().Err(err).
					Uint64("cached_epoch", s.cachedSnap.Epoch).Uint64("current_epoch", cur).
					Msg("committee source: refusing to bridge an epoch boundary with a cached snapshot (strict; fail closed)")
				return nil, fmt.Errorf(
					"committee source: cached snapshot is epoch %d but current epoch is %d (strict boundary; fail closed): %w",
					s.cachedSnap.Epoch, cur, err)
			}
		}
		log.Warn().Err(err).
			Msg("committee source: serving last-good epoch-fresh snapshot (seed unreachable/invalid)")
		return s.cachedSnap.BLSPubByPeer(), nil
	}
	return nil, err
}

// ---- Reward-address accessor (buddy staking rewards) -------------------------
//
// currentSnapshot / serveLastGoodSnapshotOr mirror eligible / serveLastGoodOr
// but return the whole verified snapshot, so the reward-address accessor derives
// the peer -> reward-address map from the SAME authenticated, TTL-cached snapshot
// that vote eligibility uses (they share s.cachedSnap under s.mu). eligible is
// left untouched deliberately — the vote-eligibility hot path is not modified.

// currentSnapshot returns the authenticated, epoch-fresh CURRENT snapshot, using
// the TTL cache, with the same fail-closed + serve-last-good bridge as eligible.
// Caller holds s.mu.
func (s *committeeSource) currentSnapshot(ctx context.Context) (*committee.CommitteeSnapshot, error) {
	// Fast path: a recently-verified snapshot within the TTL (the SAME cache
	// eligible warms/reads).
	if s.cachedSnap != nil && time.Since(s.cachedAt) < s.ttl {
		return s.cachedSnap, nil
	}
	authority, err := s.resolveAuthority(ctx)
	if err != nil {
		return s.serveLastGoodSnapshotOr(err)
	}
	snap, err := s.fetch(ctx, 0) // 0 = current epoch
	if err != nil {
		return s.serveLastGoodSnapshotOr(err)
	}
	if err := committee.VerifyCommitteeSnapshot(snap, authority); err != nil {
		return s.serveLastGoodSnapshotOr(fmt.Errorf("committee snapshot rejected: %w", err))
	}
	if err := committee.CheckSnapshotEpochFresh(snap.Epoch, time.Now().Unix(), s.epochSeconds); err != nil {
		return s.serveLastGoodSnapshotOr(fmt.Errorf("committee snapshot rejected: %w", err))
	}
	s.cachedSnap = snap
	s.cachedAt = time.Now()
	return snap, nil
}

// serveLastGoodSnapshotOr is serveLastGoodOr returning the whole snapshot (see
// that method for the boundary-split reasoning and strictBoundary). Caller holds
// s.mu.
func (s *committeeSource) serveLastGoodSnapshotOr(err error) (*committee.CommitteeSnapshot, error) {
	if s.cachedSnap != nil &&
		committee.CheckSnapshotEpochFresh(s.cachedSnap.Epoch, time.Now().Unix(), s.epochSeconds) == nil {
		if s.strictBoundary {
			if cur := committee.EpochForTime(time.Now().Unix(), s.epochSeconds); cur != s.cachedSnap.Epoch {
				log.Error().Err(err).
					Uint64("cached_epoch", s.cachedSnap.Epoch).Uint64("current_epoch", cur).
					Msg("committee source: refusing to bridge an epoch boundary with a cached snapshot (strict; fail closed)")
				return nil, fmt.Errorf(
					"committee source: cached snapshot is epoch %d but current epoch is %d (strict boundary; fail closed): %w",
					s.cachedSnap.Epoch, cur, err)
			}
		}
		log.Warn().Err(err).
			Msg("committee source: serving last-good epoch-fresh snapshot for reward addresses (seed unreachable/invalid)")
		return s.cachedSnap, nil
	}
	return nil, err
}

// rewardAddresses returns the authenticated peer_id -> reward-address map from
// the SAME verified/cached CURRENT snapshot eligible uses, so the fee split (R4
// build + R5 validate) is derived from one authenticated source. FAIL CLOSED:
// a fetch/verify/freshness failure returns an error unless a still-epoch-fresh
// cached snapshot can bridge a transient seed outage.
func (s *committeeSource) rewardAddresses() (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	snap, err := s.currentSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return snap.RewardAddrByPeer(), nil
}
