package messaging

// P1 adversarial tests — committee eligibility is DYNAMIC (live getBuddy set
// minus the operator block_buddy blocklist) and MUST fail closed.
//
// Design (per operator decision): membership authenticates peer_id only; the
// BLS public key a vote carries is self-reported and not yet bound to the
// peer_id (seedNode ListBuddy does not return bls_pub yet). The accepted,
// temporary forgery window this creates is pinned by TestP1_ForgeryWindow_* so
// it is visible and will flip to a rejection once bls_pub binding lands.
//
// Invariants under test:
//   1. Fail closed: no eligibility source wired, a source error, or an empty
//      buddy set ⇒ no vote is authorized and consensus is refused.
//   4. Unique identity: the same key under two PeerIDs, or one PeerID voting
//      twice, counts once.
//   + block_buddy: a blocklisted peer never counts even if getBuddy returns it.

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"

	blssign "gossipnode/AVC/BLS/bls-sign"
	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	BLS_Verifier "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Verifier"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// ---- shared BLS test-member helpers (also used by the JMDN-001 harness) ------

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
func (m blsMember) blockVote(t *testing.T, blockHashHex string, vote int8) BLS_Signer.BLSresponse {
	t.Helper()
	msg, err := BLS_Verifier.CanonicalBlockVoteMessage(blockHashHex, vote)
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
func useEligible(t *testing.T, peerIDs ...string) {
	t.Helper()
	SetCommitteeEligibilitySource(func() (map[string]struct{}, error) {
		set := make(map[string]struct{}, len(peerIDs))
		for _, p := range peerIDs {
			set[p] = struct{}{}
		}
		return set, nil
	})
	t.Cleanup(func() { SetCommitteeEligibilitySource(defaultTestEligibility) })
}

// useEligibleErr installs an eligibility source that fails, and restores the
// default on cleanup.
func useEligibleErr(t *testing.T, err error) {
	t.Helper()
	SetCommitteeEligibilitySource(func() (map[string]struct{}, error) { return nil, err })
	t.Cleanup(func() { SetCommitteeEligibilitySource(defaultTestEligibility) })
}

// useNoSource clears the eligibility source (nil) and restores on cleanup.
func useNoSource(t *testing.T) {
	t.Helper()
	SetCommitteeEligibilitySource(nil)
	t.Cleanup(func() { SetCommitteeEligibilitySource(defaultTestEligibility) })
}

// attackerCert returns a quorum-sized certificate of block-bound +1 votes from
// three attacker-generated keys under attacker-chosen PeerIDs. Signatures are
// VALID BLS signatures over the correct block-bound message — only eligibility
// can stop them.
func attackerCert(t *testing.T, blockHashHex string, peerIDs ...string) map[string]string {
	t.Helper()
	if len(peerIDs) == 0 {
		peerIDs = []string{"attacker-1", "attacker-2", "attacker-3"}
	}
	var votes []BLS_Signer.BLSresponse
	for i, pid := range peerIDs {
		a := mustMintMember(pid, byte(0xA0+i))
		votes = append(votes, a.blockVote(t, blockHashHex, 1))
	}
	return certData(t, votes...)
}

// ---- fail-closed: no/failed/empty eligibility source ⇒ refuse -----------------

// A forged certificate (3 attacker keys under fake PeerIDs, valid signatures)
// must be REJECTED when the eligibility source is absent, errors, or is empty.
func TestP1_EligibilityDefect_ForgedCertRejected(t *testing.T) {
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
			rej := verifyBlockCertificate(blockMsg(hash, attackerCert(t, hash.Hex())))
			if rej == nil {
				t.Fatalf("FAIL-OPEN: forged certificate accepted with defective eligibility source (%s)", tc.name)
			}
			if rej.reason != "committee_source_invalid" {
				t.Fatalf("want reason committee_source_invalid naming the defect, got %q (%v)", rej.reason, rej.err)
			}
		})
	}
}

// keyAuthorized itself must fail closed: no source, source error, empty set each
// authorize NOBODY.
func TestP1_KeyAuthorized_FailsClosed(t *testing.T) {
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
func TestP1_ValidateCommitteeSource_NamesDefect(t *testing.T) {
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

// Attacker keys under PeerIDs that are NOT in the buddy set never reach quorum.
func TestP1_ValidSource_AttackerPeerIDsRejected(t *testing.T) {
	useEligible(t, "peerA", "peerB", "peerC")
	hash := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000055")
	rej := verifyBlockCertificate(blockMsg(hash, attackerCert(t, hash.Hex(), "evil-1", "evil-2", "evil-3")))
	if rej == nil || rej.reason != "quorum_not_met" {
		t.Fatalf("attacker peer_ids not in buddy set must not reach quorum, got %v", rej)
	}
}

// One key presented under three eligible PeerIDs counts at most once (dedup by
// BLS key), so it cannot alone satisfy a 3-of-N quorum.
func TestP1_ValidSource_SameKeyUnderMultiplePeerIDs_CountsOnce(t *testing.T) {
	useEligible(t, "peerA", "peerB", "peerC")
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
func TestP1_ValidSource_LegitimateQuorumAccepted(t *testing.T) {
	useEligible(t, "peerA", "peerB", "peerC")
	a := mustMintMember("peerA", 0x51)
	b := mustMintMember("peerB", 0x52)
	c := mustMintMember("peerC", 0x53)
	hash := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000057")
	cert := certData(t,
		a.blockVote(t, hash.Hex(), 1),
		b.blockVote(t, hash.Hex(), 1),
		c.blockVote(t, hash.Hex(), 1),
	)
	if rej := verifyBlockCertificate(blockMsg(hash, cert)); rej != nil {
		t.Fatalf("legitimate eligible quorum must be accepted, got %s: %v", rej.reason, rej.err)
	}
}

// ---- block_buddy blocklist ----------------------------------------------------

// A blocklisted buddy is excluded even though the eligibility source (getBuddy)
// returns it: with one of three members blocked, the honest cert drops below
// quorum.
func TestP1_BlockBuddy_ExcludesEvenIfReturnedByGetBuddy(t *testing.T) {
	// getBuddy returns A, B, C; operator blocks C.
	useEligible(t, "peerA", "peerB", "peerC")
	withBlockBuddy(t, "peerC")

	a := mustMintMember("peerA", 0x71)
	b := mustMintMember("peerB", 0x72)
	c := mustMintMember("peerC", 0x73)
	hash := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000058")

	// C is blocked → only A and B count → 2 < quorum(3).
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
func TestP1_BlockBuddy_EmptiesCommittee_FailsClosed(t *testing.T) {
	useEligible(t, "peerA", "peerB")
	withBlockBuddy(t, "peerA", "peerB")
	if err := ValidateCommitteeSource(); err == nil {
		t.Fatal("committee emptied by block_buddy must fail closed")
	}
}

// ---- accepted interim forgery window (pinned so it is visible) ----------------

// SECURITY NOTE: until seedNode returns bls_pub, an attacker who knows an
// eligible buddy's peer_id can vote under that peer_id with their OWN BLS key
// and it counts. This test PINS that accepted interim behavior. When bls_pub
// binding lands, flip the expectation to a rejection (and this test documents
// exactly where).
func TestP1_ForgeryWindow_AttackerKeyUnderEligiblePeerID_CurrentlyCounts(t *testing.T) {
	useEligible(t, "peerA", "peerB", "peerC")
	hash := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000059")

	// Attacker owns none of the real keys, but knows the eligible peer_ids.
	fakeA := mustMintMember("peerA", 0xE1)
	fakeB := mustMintMember("peerB", 0xE2)
	fakeC := mustMintMember("peerC", 0xE3)
	cert := certData(t,
		fakeA.blockVote(t, hash.Hex(), 1),
		fakeB.blockVote(t, hash.Hex(), 1),
		fakeC.blockVote(t, hash.Hex(), 1),
	)
	rej := verifyBlockCertificate(blockMsg(hash, cert))
	if rej != nil {
		t.Fatalf("interim model authenticates peer_id only; forged-key cert under eligible peer_ids is expected to COUNT until bls_pub binding lands, but got reject %s. If bls_pub binding was added, update this test to expect rejection.", rej.reason)
	}
}
