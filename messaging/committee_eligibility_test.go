package messaging

// Committee eligibility is dynamic (live getBuddy set minus the operator
// block_buddy blocklist) and MUST fail closed.
//
// Design: membership authenticates peer_id, and when the authenticated seed
// snapshot binds a bls_pub to that peer_id the vote's key must match it. A
// legacy source with no snapshot carries no bound key and falls back to
// peer_id-only authentication (logged).
//
// Properties under test:
//   - Fail closed: no eligibility source wired, a source error, or an empty
//     buddy set ⇒ no vote is authorized and consensus is refused.
//   - Unique identity: the same key under two PeerIDs, or one PeerID voting
//     twice, counts once.
//   - block_buddy: a blocklisted peer never counts even if getBuddy returns it.

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"

	blssign "gossipnode/AVC/BLS/bls-sign"
	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// ---- shared BLS test-member helpers (also used by the block harness) ---------

type blsMember struct {
	peerID string
	priv   []byte
	pubHex string
}

// mustMintMember derives a deterministic, distinct BLS keypair per seed. It
// never touches config/bls.json (unlike BLS_Signer's process-global keypair).
// Some SHA-256-derived seeds fall outside the BLS scalar field ("value out of
// range"), so we deterministically retry with a bumped nonce until one lands.
func mustMintMember(peerID string, seed byte) blsMember {
	for nonce := byte(0); nonce < 255; nonce++ {
		raw := make([]byte, 32)
		for i := range raw {
			raw[i] = seed
		}
		raw[0] = nonce
		priv, pub, err := blssign.GenerateBLSKeyPairFromRawPrivKey(raw)
		if err == nil {
			return blsMember{peerID: peerID, priv: priv, pubHex: hex.EncodeToString(pub)}
		}
	}
	panic("mint BLS member: no valid scalar found for seed")
}

// blockVote returns a block-bound BLSresponse signed with THIS member's key
// (canonical message identical to BLS_Signer.SignMessageForBlock).
// blockVote signs a v3 block-bound vote at height 0 — the default for tests whose
// block carries an unset BlockNumber. Use blockVoteAt when the block has a height,
// so the signature verifies at that block's BlockNumber.
func (m blsMember) blockVote(t *testing.T, blockHashHex string, vote int8) BLS_Signer.BLSresponse {
	return m.blockVoteAt(t, blockHashHex, vote, 0)
}

// blockVoteAt signs a v3 block-bound vote at an explicit height.
func (m blsMember) blockVoteAt(t *testing.T, blockHashHex string, vote int8, height uint64) BLS_Signer.BLSresponse {
	t.Helper()
	msg, err := BLS_Signer.CanonicalVoteMessageV3(BLS_Signer.DomainChainID(), height, blockHashHex, vote)
	if err != nil {
		t.Fatalf("canonical vote message: %v", err)
	}
	sig, err := blssign.BLSSign(m.priv, msg)
	if err != nil {
		t.Fatalf("bls sign: %v", err)
	}
	return BLS_Signer.BLSresponse{
		Signature: hex.EncodeToString(sig),
		Agree:     vote == 1,
		PubKey:    m.pubHex,
		PeerID:    m.peerID,
	}
}

// legacyVote returns a legacy (unbound) "vote:<v>" BLSresponse from this member.
func (m blsMember) legacyVote(t *testing.T, vote int8) BLS_Signer.BLSresponse {
	t.Helper()
	msg := []byte("vote:1")
	if vote == -1 {
		msg = []byte("vote:-1")
	}
	sig, err := blssign.BLSSign(m.priv, msg)
	if err != nil {
		t.Fatalf("bls sign legacy: %v", err)
	}
	return BLS_Signer.BLSresponse{
		Signature: hex.EncodeToString(sig),
		Agree:     vote == 1,
		PubKey:    m.pubHex,
		PeerID:    m.peerID,
	}
}

func certData(t *testing.T, votes ...BLS_Signer.BLSresponse) map[string]string {
	t.Helper()
	b, err := json.Marshal(votes)
	if err != nil {
		t.Fatalf("marshal cert: %v", err)
	}
	return map[string]string{"bls_results": string(b)}
}

func blockMsg(hash common.Hash, data map[string]string) config.BlockMessage {
	return config.BlockMessage{Block: &config.ZKBlock{BlockHash: hash}, Data: data}
}

// ---- eligibility-source fixtures ---------------------------------------------

// useEligible installs an eligibility source returning exactly these peer_ids,
// and clears it on cleanup so tests do not leak state into one another.
// useEligible declares peer_ids eligible with NO bound bls_pub (empty value), so
// only membership is enforced — used by tests that exercise membership/quorum/
// dedup independent of the key binding.
func useEligible(t *testing.T, peerIDs ...string) {
	t.Helper()
	SetCommitteeEligibilitySource(func() (map[string]string, error) {
		set := make(map[string]string, len(peerIDs))
		for _, p := range peerIDs {
			set[p] = ""
		}
		return set, nil
	})
	t.Cleanup(func() { SetCommitteeEligibilitySource(defaultTestEligibility) })
}

// useEligibleBound declares an authenticated committee binding each member's
// peer_id to its bls_pub (as the seed snapshot does), so the key binding is
// enforced: a vote's pubkey must match the bound key.
func useEligibleBound(t *testing.T, members ...blsMember) {
	t.Helper()
	SetCommitteeEligibilitySource(func() (map[string]string, error) {
		m := make(map[string]string, len(members))
		for _, mem := range members {
			m[mem.peerID] = mem.pubHex
		}
		return m, nil
	})
	t.Cleanup(func() { SetCommitteeEligibilitySource(defaultTestEligibility) })
}

// useEligibleErr installs an eligibility source that fails, and restores the
// default on cleanup.
func useEligibleErr(t *testing.T, err error) {
	t.Helper()
	SetCommitteeEligibilitySource(func() (map[string]string, error) { return nil, err })
	t.Cleanup(func() { SetCommitteeEligibilitySource(defaultTestEligibility) })
}

// useNoSource clears the eligibility source (nil) and restores on cleanup.
func useNoSource(t *testing.T) {
	t.Helper()
	SetCommitteeEligibilitySource(nil)
	t.Cleanup(func() { SetCommitteeEligibilitySource(defaultTestEligibility) })
}

// nonMemberCert returns a quorum-sized certificate of block-bound +1 votes from
// three independently generated keys under caller-chosen PeerIDs. Signatures are
// VALID BLS signatures over the correct block-bound message — only eligibility
// can stop them.
func nonMemberCert(t *testing.T, blockHashHex string, peerIDs ...string) map[string]string {
	t.Helper()
	if len(peerIDs) == 0 {
		peerIDs = []string{"nonmember-1", "nonmember-2", "nonmember-3"}
	}
	var votes []BLS_Signer.BLSresponse
	for i, pid := range peerIDs {
		a := mustMintMember(pid, byte(0xA0+i))
		votes = append(votes, a.blockVote(t, blockHashHex, 1))
	}
	return certData(t, votes...)
}

// ---- fail-closed: no/failed/empty eligibility source ⇒ refuse -----------------

// A certificate with three keys under ineligible PeerIDs (valid signatures)
// must be REJECTED when the eligibility source is absent, errors, or is empty.
func TestEligibilityDefect_InvalidCertRejected(t *testing.T) {
	hash := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000051")

	cases := []struct {
		name  string
		setup func(t *testing.T)
	}{
		{"no source wired", func(t *testing.T) { useNoSource(t) }},
		{"source errors", func(t *testing.T) { useEligibleErr(t, fmt.Errorf("seed unreachable")) }},
		{"empty buddy set", func(t *testing.T) { useEligible(t) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup(t)
			rej := verifyBlockCertificate(blockMsg(hash, nonMemberCert(t, hash.Hex())))
			if rej == nil {
				t.Fatalf("FAIL-OPEN: invalid certificate accepted with defective eligibility source (%s)", tc.name)
			}
			if rej.reason != "committee_source_invalid" {
				t.Fatalf("want reason committee_source_invalid naming the defect, got %q (%v)", rej.reason, rej.err)
			}
		})
	}
}

// keyAuthorized itself must fail closed: no source, source error, empty set each
// authorize NOBODY.
func TestKeyAuthorized_FailsClosed(t *testing.T) {
	m := mustMintMember("peerX", 0x31)

	t.Run("no source", func(t *testing.T) {
		useNoSource(t)
		if keyAuthorized(m.peerID, m.pubHex) {
			t.Fatal("FAIL-OPEN: keyAuthorized=true with no eligibility source")
		}
	})
	t.Run("source error", func(t *testing.T) {
		useEligibleErr(t, fmt.Errorf("boom"))
		if keyAuthorized(m.peerID, m.pubHex) {
			t.Fatal("FAIL-OPEN: keyAuthorized=true on eligibility source error")
		}
	})
	t.Run("empty set", func(t *testing.T) {
		useEligible(t)
		if keyAuthorized(m.peerID, m.pubHex) {
			t.Fatal("FAIL-OPEN: keyAuthorized=true on empty buddy set")
		}
	})
}

// ValidateCommitteeSource must name the defect.
func TestValidateCommitteeSource_NamesDefect(t *testing.T) {
	cases := []struct {
		name, wantSub string
		setup         func(t *testing.T)
	}{
		{"no source", "not configured", func(t *testing.T) { useNoSource(t) }},
		{"error", "source failed", func(t *testing.T) { useEligibleErr(t, fmt.Errorf("x")) }},
		{"empty", "empty buddy set", func(t *testing.T) { useEligible(t) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup(t)
			err := ValidateCommitteeSource()
			if err == nil {
				t.Fatalf("defective source (%s) must be invalid", tc.name)
			}
			if !contains(err.Error(), tc.wantSub) {
				t.Fatalf("error must name defect: want %q in %q", tc.wantSub, err.Error())
			}
		})
	}
	t.Run("valid source passes", func(t *testing.T) {
		useEligible(t, "peerA", "peerB", "peerC")
		if err := ValidateCommitteeSource(); err != nil {
			t.Fatalf("valid source must validate, got %v", err)
		}
	})
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// ---- eligibility gating with a valid source -----------------------------------

// Keys under PeerIDs that are NOT in the buddy set never reach quorum.
func TestValidSource_IneligiblePeerIDsRejected(t *testing.T) {
	useEligible(t, "peerA", "peerB", "peerC", "peerD", "peerE") // n=5, quorum 4
	hash := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000055")
	rej := verifyBlockCertificate(blockMsg(hash, nonMemberCert(t, hash.Hex(), "outsider-1", "outsider-2", "outsider-3")))
	if rej == nil || rej.reason != "quorum_not_met" {
		t.Fatalf("peer_ids not in buddy set must not reach quorum, got %v", rej)
	}
}

// One key presented under three eligible PeerIDs counts at most once (dedup by
// BLS key), so it cannot alone satisfy a 3-of-N quorum.
func TestValidSource_SameKeyUnderMultiplePeerIDs_CountsOnce(t *testing.T) {
	useEligible(t, "peerA", "peerB", "peerC", "peerD", "peerE") // n=5, quorum 4
	a := mustMintMember("peerA", 0x51)
	asB := blsMember{peerID: "peerB", priv: a.priv, pubHex: a.pubHex}
	asC := blsMember{peerID: "peerC", priv: a.priv, pubHex: a.pubHex}
	hash := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000056")
	cert := certData(t,
		a.blockVote(t, hash.Hex(), 1),
		asB.blockVote(t, hash.Hex(), 1),
		asC.blockVote(t, hash.Hex(), 1),
	)
	rej := verifyBlockCertificate(blockMsg(hash, cert))
	if rej == nil || rej.reason != "quorum_not_met" {
		t.Fatalf("one key under three PeerIDs must count once, got %v", rej)
	}
}

// A legitimate quorum from eligible members is accepted (fail-closed must not
// brick the honest path).
func TestValidSource_LegitimateQuorumAccepted(t *testing.T) {
	useEligible(t, "peerA", "peerB", "peerC", "peerD", "peerE") // n=5, quorum ceil(2*5/3)=4
	a := mustMintMember("peerA", 0x51)
	b := mustMintMember("peerB", 0x52)
	c := mustMintMember("peerC", 0x53)
	d := mustMintMember("peerD", 0x54)
	hash := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000057")
	cert := certData(t,
		a.blockVote(t, hash.Hex(), 1),
		b.blockVote(t, hash.Hex(), 1),
		c.blockVote(t, hash.Hex(), 1),
		d.blockVote(t, hash.Hex(), 1),
	)
	if rej := verifyBlockCertificate(blockMsg(hash, cert)); rej != nil {
		t.Fatalf("legitimate eligible quorum must be accepted, got %s: %v", rej.reason, rej.err)
	}
}

// ---- block_buddy blocklist ----------------------------------------------------

// A blocklisted buddy is excluded even though the eligibility source (getBuddy)
// returns it: with one of three members blocked, the honest cert drops below
// quorum.
func TestBlockBuddy_ExcludesEvenIfReturnedByGetBuddy(t *testing.T) {
	// getBuddy returns A..E (n=5, quorum 4); operator blocks C.
	useEligible(t, "peerA", "peerB", "peerC", "peerD", "peerE")
	withBlockBuddy(t, "peerC")

	a := mustMintMember("peerA", 0x71)
	b := mustMintMember("peerB", 0x72)
	c := mustMintMember("peerC", 0x73)
	hash := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000058")

	// C is blocked → only A and B count → 2 < quorum 4.
	cert := certData(t,
		a.blockVote(t, hash.Hex(), 1),
		b.blockVote(t, hash.Hex(), 1),
		c.blockVote(t, hash.Hex(), 1),
	)
	if rej := verifyBlockCertificate(blockMsg(hash, cert)); rej == nil || rej.reason != "quorum_not_met" {
		t.Fatalf("blocklisted buddy must not count toward quorum, got %v", rej)
	}

	// keyAuthorized directly: C blocked, A allowed.
	if keyAuthorized("peerC", c.pubHex) {
		t.Fatal("blocklisted peer must not be authorized")
	}
	if !keyAuthorized("peerA", a.pubHex) {
		t.Fatal("non-blocked eligible peer must be authorized")
	}
}

// If the blocklist empties the committee entirely, the node fails closed.
func TestBlockBuddy_EmptiesCommittee_FailsClosed(t *testing.T) {
	useEligible(t, "peerA", "peerB")
	withBlockBuddy(t, "peerA", "peerB")
	if err := ValidateCommitteeSource(); err == nil {
		t.Fatal("committee emptied by block_buddy must fail closed")
	}
}

// ---- key-binding behavior (pinned so a change is visible) ---------------------

// With the authenticated snapshot binding peer_id to bls_pub, a vote under an
// eligible peer_id but with a non-matching key is rejected.
func TestBinding_KeyUnderEligiblePeerID_Rejected(t *testing.T) {
	// Authenticated committee: peer_ids bound to the LEGIT members' keys.
	legitA := mustMintMember("peerA", 0x91)
	legitB := mustMintMember("peerB", 0x92)
	legitC := mustMintMember("peerC", 0x93)
	legitD := mustMintMember("peerD", 0x94)
	legitE := mustMintMember("peerE", 0x95)
	useEligibleBound(t, legitA, legitB, legitC, legitD, legitE) // n=5, quorum 4

	hash := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000059")

	// Votes under the eligible peer_ids but with different keys.
	fakeA := mustMintMember("peerA", 0xE1)
	fakeB := mustMintMember("peerB", 0xE2)
	fakeC := mustMintMember("peerC", 0xE3)
	cert := certData(t,
		fakeA.blockVote(t, hash.Hex(), 1),
		fakeB.blockVote(t, hash.Hex(), 1),
		fakeC.blockVote(t, hash.Hex(), 1),
	)
	if rej := verifyBlockCertificate(blockMsg(hash, cert)); rej == nil || rej.reason != "quorum_not_met" {
		t.Fatalf("votes under eligible peer_ids with non-matching keys must be rejected by the bls_pub binding, got %v", rej)
	}

	// Direct: keyAuthorized rejects the non-matching key, accepts the bound key.
	if keyAuthorized("peerA", fakeA.pubHex) {
		t.Fatal("non-matching key under an eligible peer_id must not be authorized")
	}
	if !keyAuthorized("peerA", legitA.pubHex) {
		t.Fatal("the snapshot-bound key must be authorized")
	}
}

// A legitimate quorum whose vote keys match the snapshot-bound keys is accepted
// (binding must not brick the honest path).
func TestBinding_LegitBoundQuorumAccepted(t *testing.T) {
	a := mustMintMember("peerA", 0x91)
	b := mustMintMember("peerB", 0x92)
	c := mustMintMember("peerC", 0x93)
	d := mustMintMember("peerD", 0x94)
	e := mustMintMember("peerE", 0x95)
	useEligibleBound(t, a, b, c, d, e) // n=5, quorum ceil(2*5/3)=4

	hash := common.HexToHash("0x000000000000000000000000000000000000000000000000000000000000005a")
	cert := certData(t,
		a.blockVote(t, hash.Hex(), 1),
		b.blockVote(t, hash.Hex(), 1),
		c.blockVote(t, hash.Hex(), 1),
		d.blockVote(t, hash.Hex(), 1),
	)
	if rej := verifyBlockCertificate(blockMsg(hash, cert)); rej != nil {
		t.Fatalf("bound-key legitimate quorum must be accepted, got %s: %v", rej.reason, rej.err)
	}
}
