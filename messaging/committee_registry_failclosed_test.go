package messaging

// P1 adversarial tests — committee registry MUST fail closed (JMDN re-review,
// consensus blocker P1).
//
// Written to FAIL on fix/BLS (where keyAuthorized returns true on load error /
// empty registry / missing file, and loadCommitteeKeys silently tolerates
// duplicate peer_ids, duplicate bls_pubs, and empty fields) and to PASS once
// registry loading is fail-closed.
//
// Invariants under test:
//   1. Fail closed: missing, empty, unreadable, or malformed registry ⇒ no vote
//      is authorized and the certificate path refuses consensus participation.
//   4. Unique identity: duplicate PeerIDs or duplicate BLS keys in the registry
//      make the registry invalid; the same BLS key under two PeerIDs in a
//      certificate can never count twice.

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// ---- registry fixtures --------------------------------------------------------

func resetCommitteeRegistryForTest() {
	committeeOnce = sync.Once{}
	committeeKeys = nil
	committeeErr = nil
}

// useRegistryPath points the loader at path and resets the once-cache; the
// previous path is restored (and the cache reset) on test cleanup.
func useRegistryPath(t *testing.T, path string) {
	t.Helper()
	prev := committeeKeysFile
	committeeKeysFile = path
	resetCommitteeRegistryForTest()
	t.Cleanup(func() {
		committeeKeysFile = prev
		resetCommitteeRegistryForTest()
	})
}

// useRegistryJSON writes content as the registry file and points the loader at it.
func useRegistryJSON(t *testing.T, content string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "committee_keys.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	useRegistryPath(t, p)
}

func registryJSON(t *testing.T, entries []committeeEntry) string {
	t.Helper()
	b, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}
	return string(b)
}

// attackerCert returns a quorum-sized certificate of block-bound +1 votes from
// three attacker-generated keys under fake PeerIDs. Signatures are VALID BLS
// signatures over the correct block-bound message — only membership can stop them.
func attackerCert(t *testing.T, blockHashHex string) map[string]string {
	t.Helper()
	a1 := mustMintMember("attacker-1", 0xA1)
	a2 := mustMintMember("attacker-2", 0xA2)
	a3 := mustMintMember("attacker-3", 0xA3)
	return certData(t,
		a1.blockVote(t, blockHashHex, 1),
		a2.blockVote(t, blockHashHex, 1),
		a3.blockVote(t, blockHashHex, 1),
	)
}

func blockMsg(hash common.Hash, data map[string]string) config.BlockMessage {
	return config.BlockMessage{Block: &config.ZKBlock{BlockHash: hash}, Data: data}
}

// ---- fail-closed: registry absent/defective ⇒ refuse ---------------------------

// A forged certificate (3 attacker keys, fake PeerIDs, valid signatures) must be
// REJECTED when the registry is missing/empty/malformed/unreadable. On fix/BLS
// every one of these cases authorizes everyone (fail open) and the certificate
// is ACCEPTED — so each subtest fails before the P1 fix.
func TestP1_RegistryDefect_ForgedCertRejected(t *testing.T) {
	hash := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000051")

	cases := []struct {
		name  string
		setup func(t *testing.T)
	}{
		{"missing registry file", func(t *testing.T) {
			useRegistryPath(t, filepath.Join(t.TempDir(), "does_not_exist.json"))
		}},
		{"empty registry array", func(t *testing.T) {
			useRegistryJSON(t, `[]`)
		}},
		{"malformed registry json", func(t *testing.T) {
			useRegistryJSON(t, `{not json`)
		}},
		{"unreadable registry (path is a directory)", func(t *testing.T) {
			useRegistryPath(t, t.TempDir()) // os.ReadFile on a directory errors (non-IsNotExist)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup(t)
			rej := verifyBlockCertificate(blockMsg(hash, attackerCert(t, hash.Hex())))
			if rej == nil {
				t.Fatalf("FAIL-OPEN: forged certificate accepted with defective committee registry (%s)", tc.name)
			}
			if rej.reason != "committee_registry_invalid" {
				t.Fatalf("want reason committee_registry_invalid naming the defect, got %q (%v)", rej.reason, rej.err)
			}
		})
	}
}

// keyAuthorized itself must fail closed: load error, missing file, and empty
// registry each authorize NOBODY. On fix/BLS all three return true.
func TestP1_KeyAuthorized_FailsClosed(t *testing.T) {
	m := mustMintMember("peerX", 0x31)

	t.Run("missing file", func(t *testing.T) {
		useRegistryPath(t, filepath.Join(t.TempDir(), "nope.json"))
		if keyAuthorized(m.peerID, m.pubHex) {
			t.Fatal("FAIL-OPEN: keyAuthorized=true with no registry file")
		}
	})
	t.Run("load error", func(t *testing.T) {
		useRegistryPath(t, t.TempDir())
		if keyAuthorized(m.peerID, m.pubHex) {
			t.Fatal("FAIL-OPEN: keyAuthorized=true on registry load error")
		}
	})
	t.Run("empty registry", func(t *testing.T) {
		useRegistryJSON(t, `[]`)
		if keyAuthorized(m.peerID, m.pubHex) {
			t.Fatal("FAIL-OPEN: keyAuthorized=true on empty registry")
		}
	})
	t.Run("malformed registry", func(t *testing.T) {
		useRegistryJSON(t, `{not json`)
		if keyAuthorized(m.peerID, m.pubHex) {
			t.Fatal("FAIL-OPEN: keyAuthorized=true on malformed registry")
		}
	})
}

// ---- registry integrity: duplicates / incomplete entries invalidate it --------

// Duplicate PeerID entries: on fix/BLS the map silently keeps the LAST key for
// the duplicated peer and the certificate below is accepted. Post-fix the whole
// registry is invalid and consensus participation is refused.
func TestP1_RegistryDuplicatePeerID_Invalid(t *testing.T) {
	k1 := mustMintMember("peerA", 0x41)
	k2 := mustMintMember("peerA", 0x42) // same PeerID, different key
	k3 := mustMintMember("peerB", 0x43)
	k4 := mustMintMember("peerC", 0x44)
	useRegistryJSON(t, registryJSON(t, []committeeEntry{
		{PeerID: k1.peerID, BLSPub: k1.pubHex},
		{PeerID: k2.peerID, BLSPub: k2.pubHex},
		{PeerID: k3.peerID, BLSPub: k3.pubHex},
		{PeerID: k4.peerID, BLSPub: k4.pubHex},
	}))

	hash := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000052")
	// Pre-fix this cert reaches quorum (peerA authenticated by the overwriting key k2).
	cert := certData(t,
		k2.blockVote(t, hash.Hex(), 1),
		k3.blockVote(t, hash.Hex(), 1),
		k4.blockVote(t, hash.Hex(), 1),
	)
	rej := verifyBlockCertificate(blockMsg(hash, cert))
	if rej == nil {
		t.Fatal("registry with duplicate peer_id entries must be invalid (fail closed); cert was accepted")
	}
	if rej.reason != "committee_registry_invalid" {
		t.Fatalf("want committee_registry_invalid, got %q (%v)", rej.reason, rej.err)
	}
}

// Duplicate BLS keys under different PeerIDs: on fix/BLS both entries load and
// the SAME key counts twice toward quorum. Post-fix the registry is invalid.
func TestP1_RegistryDuplicateBLSKey_Invalid(t *testing.T) {
	shared := mustMintMember("peerA", 0x45)
	other := mustMintMember("peerC", 0x46)
	useRegistryJSON(t, registryJSON(t, []committeeEntry{
		{PeerID: "peerA", BLSPub: shared.pubHex},
		{PeerID: "peerB", BLSPub: shared.pubHex}, // same key, second identity
		{PeerID: "peerC", BLSPub: other.pubHex},
	}))

	hash := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000053")
	sharedAsB := blsMember{peerID: "peerB", priv: shared.priv, pubHex: shared.pubHex}
	cert := certData(t,
		shared.blockVote(t, hash.Hex(), 1),   // key under peerA
		sharedAsB.blockVote(t, hash.Hex(), 1), // SAME key under peerB
		other.blockVote(t, hash.Hex(), 1),
	)
	rej := verifyBlockCertificate(blockMsg(hash, cert))
	if rej == nil {
		t.Fatal("one BLS key under two PeerIDs reached quorum: duplicate-key registry must be invalid")
	}
	if rej.reason != "committee_registry_invalid" {
		t.Fatalf("want committee_registry_invalid, got %q (%v)", rej.reason, rej.err)
	}
}

// Entries with empty fields: on fix/BLS they are silently skipped (a truncated
// or corrupted registry shrinks the committee without anyone noticing).
// Post-fix an incomplete entry invalidates the registry.
func TestP1_RegistryIncompleteEntry_Invalid(t *testing.T) {
	k1 := mustMintMember("peerA", 0x47)
	k2 := mustMintMember("peerB", 0x48)
	k3 := mustMintMember("peerC", 0x49)
	useRegistryJSON(t, registryJSON(t, []committeeEntry{
		{PeerID: k1.peerID, BLSPub: k1.pubHex},
		{PeerID: k2.peerID, BLSPub: k2.pubHex},
		{PeerID: k3.peerID, BLSPub: k3.pubHex},
		{PeerID: "peerD", BLSPub: ""}, // incomplete — silently dropped pre-fix
	}))

	hash := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000054")
	cert := certData(t,
		k1.blockVote(t, hash.Hex(), 1),
		k2.blockVote(t, hash.Hex(), 1),
		k3.blockVote(t, hash.Hex(), 1),
	)
	rej := verifyBlockCertificate(blockMsg(hash, cert))
	if rej == nil {
		t.Fatal("registry with an incomplete entry must be invalid (fail closed); cert was accepted")
	}
	if rej.reason != "committee_registry_invalid" {
		t.Fatalf("want committee_registry_invalid, got %q (%v)", rej.reason, rej.err)
	}
}

// ValidateCommitteeRegistry (added by the P1 fix, for boot-time refusal) must
// name the exact defect so the operator can fix it.
func TestP1_ValidateCommitteeRegistry_NamesDefect(t *testing.T) {
	k1 := mustMintMember("peerA", 0x61)
	k2 := mustMintMember("peerB", 0x62)

	cases := []struct {
		name    string
		setup   func(t *testing.T)
		wantSub string
	}{
		{"missing file", func(t *testing.T) {
			useRegistryPath(t, filepath.Join(t.TempDir(), "nope.json"))
		}, "not configured"},
		{"unreadable", func(t *testing.T) {
			useRegistryPath(t, t.TempDir())
		}, "unreadable"},
		{"malformed", func(t *testing.T) {
			useRegistryJSON(t, `{not json`)
		}, "malformed"},
		{"empty", func(t *testing.T) {
			useRegistryJSON(t, `[]`)
		}, "empty"},
		{"incomplete entry", func(t *testing.T) {
			useRegistryJSON(t, registryJSON(t, []committeeEntry{{PeerID: "peerA", BLSPub: ""}}))
		}, "incomplete"},
		{"non-hex key", func(t *testing.T) {
			useRegistryJSON(t, registryJSON(t, []committeeEntry{{PeerID: "peerA", BLSPub: "zz-not-hex"}}))
		}, "not valid hex"},
		{"duplicate peer_id", func(t *testing.T) {
			useRegistryJSON(t, registryJSON(t, []committeeEntry{
				{PeerID: "peerA", BLSPub: k1.pubHex},
				{PeerID: "peerA", BLSPub: k2.pubHex},
			}))
		}, "duplicate peer_id"},
		{"duplicate bls_pub", func(t *testing.T) {
			useRegistryJSON(t, registryJSON(t, []committeeEntry{
				{PeerID: "peerA", BLSPub: k1.pubHex},
				{PeerID: "peerB", BLSPub: k1.pubHex},
			}))
		}, "duplicate bls_pub"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup(t)
			err := ValidateCommitteeRegistry()
			if err == nil {
				t.Fatalf("defective registry (%s) must be invalid", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error must name the defect: want substring %q in %q", tc.wantSub, err.Error())
			}
		})
	}

	t.Run("valid registry passes", func(t *testing.T) {
		useRegistryJSON(t, registryJSON(t, []committeeEntry{
			{PeerID: "peerA", BLSPub: k1.pubHex},
			{PeerID: "peerB", BLSPub: k2.pubHex},
		}))
		if err := ValidateCommitteeRegistry(); err != nil {
			t.Fatalf("valid registry must validate, got %v", err)
		}
	})
}

// ---- with a VALID registry, membership still gates correctly ------------------
// (Regression guards: these already pass pre-fix when a registry is configured;
// they pin the behavior so the fail-closed rework cannot loosen it.)

func validTestRegistry(t *testing.T) (a, b, c blsMember) {
	t.Helper()
	a = mustMintMember("peerA", 0x51)
	b = mustMintMember("peerB", 0x52)
	c = mustMintMember("peerC", 0x53)
	useRegistryJSON(t, registryJSON(t, []committeeEntry{
		{PeerID: a.peerID, BLSPub: a.pubHex},
		{PeerID: b.peerID, BLSPub: b.pubHex},
		{PeerID: c.peerID, BLSPub: c.pubHex},
	}))
	return a, b, c
}

func TestP1_ValidRegistry_AttackerKeysRejected(t *testing.T) {
	validTestRegistry(t)
	hash := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000055")
	rej := verifyBlockCertificate(blockMsg(hash, attackerCert(t, hash.Hex())))
	if rej == nil || rej.reason != "quorum_not_met" {
		t.Fatalf("attacker keys under fake PeerIDs must not reach quorum, got %v", rej)
	}
}

func TestP1_ValidRegistry_SameKeyUnderMultiplePeerIDs_CountsOnce(t *testing.T) {
	a, b, c := validTestRegistry(t)
	_ = b
	_ = c
	hash := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000056")
	// peerA's registered key presented under peerA, peerB and peerC.
	asB := blsMember{peerID: "peerB", priv: a.priv, pubHex: a.pubHex}
	asC := blsMember{peerID: "peerC", priv: a.priv, pubHex: a.pubHex}
	cert := certData(t,
		a.blockVote(t, hash.Hex(), 1),
		asB.blockVote(t, hash.Hex(), 1),
		asC.blockVote(t, hash.Hex(), 1),
	)
	rej := verifyBlockCertificate(blockMsg(hash, cert))
	if rej == nil || rej.reason != "quorum_not_met" {
		t.Fatalf("one key under three PeerIDs must count at most once, got %v", rej)
	}
}

func TestP1_ValidRegistry_LegitimateQuorumAccepted(t *testing.T) {
	a, b, c := validTestRegistry(t)
	hash := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000057")
	cert := certData(t,
		a.blockVote(t, hash.Hex(), 1),
		b.blockVote(t, hash.Hex(), 1),
		c.blockVote(t, hash.Hex(), 1),
	)
	if rej := verifyBlockCertificate(blockMsg(hash, cert)); rej != nil {
		t.Fatalf("legitimate registered quorum must be accepted (fail-closed must not brick the honest path), got %s: %v", rej.reason, rej.err)
	}
}
