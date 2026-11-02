package messaging

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	blssign "gossipnode/AVC/BLS/bls-sign"
	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"
)

type certFixture struct {
	pool  map[string]string // peer -> pub hex
	privs map[string][]byte
	ids   []string
	block *config.ZKBlock
}

// newCertFixture seats n validators in an anchored pool and installs the
// seams: pool and seated committee = all n, compact certs from height 1.
func newCertFixture(t *testing.T, n int, height uint64) *certFixture {
	t.Helper()
	f := &certFixture{pool: map[string]string{}, privs: map[string][]byte{}}
	for i := 0; i < n; i++ {
		pid := fmt.Sprintf("peer-%02d", i)
		priv, pub := testBLSKey(t, "compact-cert-"+pid)
		f.pool[pid], f.privs[pid] = pub, priv
		f.ids = append(f.ids, pid)
	}
	sort.Strings(f.ids)
	f.block = &config.ZKBlock{
		BlockNumber:   height,
		BlockHash:     common.HexToHash("0xabc123"),
		ConsensusHash: common.HexToHash("0xdef456"),
	}

	prevPool, prevSeated, prevFrom := compactCertPoolFn, compactCertSeatedFn, compactCertFromHeight
	compactCertPoolFn = func(uint64) (map[string]string, bool, error) { return f.pool, true, nil }
	compactCertSeatedFn = func(*config.ZKBlock) (map[string]string, error) { return f.pool, nil }
	compactCertFromHeight = func() uint64 { return 1 }
	t.Cleanup(func() {
		compactCertPoolFn, compactCertSeatedFn, compactCertFromHeight = prevPool, prevSeated, prevFrom
	})
	return f
}

// vote returns a correctly signed block-bound vote from pid.
func (f *certFixture) vote(t *testing.T, pid string, agree bool) BLS_Signer.BLSresponse {
	t.Helper()
	v := int8(-1)
	if agree {
		v = 1
	}
	msg, err := BLS_Signer.CanonicalVoteMessageV3(BLS_Signer.DomainChainID(), f.block.BlockNumber, f.block.BlockHash.Hex(), f.block.ConsensusHashHex(), v)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := blssign.BLSSign(f.privs[pid], msg)
	if err != nil {
		t.Fatal(err)
	}
	return BLS_Signer.BLSresponse{PeerID: pid, PubKey: f.pool[pid], Signature: fmt.Sprintf("%x", sig), Agree: agree}
}

func (f *certFixture) votes(t *testing.T, yes, no int) []BLS_Signer.BLSresponse {
	var out []BLS_Signer.BLSresponse
	for i := 0; i < yes; i++ {
		out = append(out, f.vote(t, f.ids[i], true))
	}
	for i := yes; i < yes+no; i++ {
		out = append(out, f.vote(t, f.ids[i], false))
	}
	return out
}

func TestCompactCert_RoundTrip(t *testing.T) {
	f := newCertFixture(t, 7, 20)
	responses := f.votes(t, 6, 1)

	stored := StoredCertificate(f.block, responses)
	if !IsCompactCertificate(stored) {
		t.Fatalf("want compact form, got %.80s", stored)
	}
	full, _ := json.Marshal(responses)
	t.Logf("stored %d bytes; full list %d bytes", len(stored), len(full))
	if len(stored) > 200 {
		t.Fatalf("compact certificate is %d bytes", len(stored))
	}

	f.block.CommitteeCertificate = stored
	res, err := VerifyStoredBlockCertificate(f.block)
	if err != nil || !res.Reached || res.YesVotes != 6 || res.CommitteeSize != 7 || res.Threshold != 5 {
		t.Fatalf("verify = %+v, %v", res, err)
	}

	// Deterministic: the same votes in another order give the same bytes.
	rev := append([]BLS_Signer.BLSresponse(nil), responses...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	if again := StoredCertificate(f.block, rev); again != stored {
		t.Fatal("vote order changed the stored certificate")
	}
}

// The reward split reads the same signers from either stored form.
func TestCompactCert_SignersMatchFullList(t *testing.T) {
	f := newCertFixture(t, 7, 20)
	responses := f.votes(t, 6, 1)
	full, _ := json.Marshal(responses)

	fromFull, err := certSignersFromStored(string(full), f.block)
	if err != nil {
		t.Fatal(err)
	}
	fromCompact, err := certSignersFromStored(StoredCertificate(f.block, responses), f.block)
	if err != nil {
		t.Fatal(err)
	}
	peers := func(s []config.CertSigner) string {
		var ids []string
		for _, c := range s {
			ids = append(ids, c.PeerID)
		}
		sort.Strings(ids)
		return strings.Join(ids, ",")
	}
	if peers(fromFull) != peers(fromCompact) {
		t.Fatalf("signers differ: full %s, compact %s", peers(fromFull), peers(fromCompact))
	}
	for _, s := range fromCompact {
		if s.PubKey != f.pool[s.PeerID] {
			t.Fatalf("signer %s key %s, want the pool key", s.PeerID, s.PubKey)
		}
	}
}

func TestCompactCert_TamperRejected(t *testing.T) {
	f := newCertFixture(t, 7, 20)
	stored := StoredCertificate(f.block, f.votes(t, 6, 1))
	var c compactCert
	if err := json.Unmarshal([]byte(stored), &c); err != nil {
		t.Fatal(err)
	}
	bitmap, _ := base64.StdEncoding.DecodeString(c.Signers)

	mutate := func(m func(b []byte)) string {
		b := append([]byte(nil), bitmap...)
		m(b)
		out, _ := json.Marshal(compactCert{V: c.V, Signers: base64.StdEncoding.EncodeToString(b), AggSig: c.AggSig})
		return string(out)
	}
	cases := map[string]string{
		"signer removed": mutate(func(b []byte) { b[0] &^= 1 }),
		"signer added":   mutate(func(b []byte) { b[0] |= 1 << 6 }),
		"bit past pool":  mutate(func(b []byte) { b[0] |= 1 << 7 }),
		"bitmap length":  `{"v":2,"signers":"` + base64.StdEncoding.EncodeToString(append(bitmap, 0)) + `","agg_sig":"` + c.AggSig + `"}`,
		"version":        `{"v":3,"signers":"` + c.Signers + `","agg_sig":"` + c.AggSig + `"}`,
		"no signature":   `{"v":2,"signers":"` + c.Signers + `","agg_sig":""}`,
	}
	for name, raw := range cases {
		f.block.CommitteeCertificate = raw
		if res, err := VerifyStoredBlockCertificate(f.block); err == nil && res.Reached {
			t.Errorf("%s: accepted", name)
		}
	}

	// The right certificate for a different block.
	f.block.CommitteeCertificate = stored
	f.block.BlockHash = common.HexToHash("0x999")
	if res, err := VerifyStoredBlockCertificate(f.block); err == nil && res.Reached {
		t.Error("certificate verified for another block")
	}
}

// YES votes that do not verify, or carry a key other than the pool's, are
// left out; if that leaves less than quorum the full list is stored.
func TestCompactCert_BadVotesExcludedOrFullList(t *testing.T) {
	f := newCertFixture(t, 7, 20)
	responses := f.votes(t, 6, 0)
	responses[0].Signature = responses[1].Signature                                        // does not verify for peer 0
	responses[1].PubKey = f.pool[f.ids[6]]                                                 // foreign key
	responses = append(responses, BLS_Signer.BLSresponse{PeerID: "outsider", Agree: true}) // not in pool

	stored := StoredCertificate(f.block, responses)
	if IsCompactCertificate(stored) {
		t.Fatal("4 valid votes cannot reach quorum 5; want the full list")
	}

	responses = append(responses, f.vote(t, f.ids[6], true)) // 5 valid now
	stored = StoredCertificate(f.block, responses)
	if !IsCompactCertificate(stored) {
		t.Fatal("5 valid votes reach quorum; want the compact form")
	}
	signers, err := certSignersFromStored(stored, f.block)
	if err != nil || len(signers) != 5 {
		t.Fatalf("signers = %d, %v", len(signers), err)
	}
	for _, s := range signers {
		if s.PeerID == f.ids[0] || s.PeerID == f.ids[1] || s.PeerID == "outsider" {
			t.Fatalf("invalid vote from %s kept", s.PeerID)
		}
	}
}

func TestCompactCert_EligibilityRules(t *testing.T) {
	f := newCertFixture(t, 7, 20)
	responses := f.votes(t, 7, 0)

	compactCertFromHeight = func() uint64 { return 0 }
	if IsCompactCertificate(StoredCertificate(f.block, responses)) {
		t.Error("switch off: want the full list")
	}
	compactCertFromHeight = func() uint64 { return 21 }
	if IsCompactCertificate(StoredCertificate(f.block, responses)) {
		t.Error("below the switch height: want the full list")
	}
	compactCertFromHeight = func() uint64 { return 1 }

	// Parent of an entropy collection window (epoch 0 window slots 4..10).
	if !isEntropyWindowParent(5) || isEntropyWindowParent(20) {
		t.Fatal("window bounds changed; update this test")
	}
	f.block.BlockNumber = 5
	if IsCompactCertificate(StoredCertificate(f.block, f.votes(t, 7, 0))) {
		t.Error("entropy window parent: want the full list")
	}
	f.block.BlockNumber = 20

	compactCertPoolFn = func(uint64) (map[string]string, bool, error) { return nil, false, nil }
	if IsCompactCertificate(StoredCertificate(f.block, responses)) {
		t.Error("unanchored period: want the full list")
	}
	f.block.CommitteeCertificate = `{"v":2,"signers":"fw==","agg_sig":"AA=="}`
	if _, err := VerifyStoredBlockCertificate(f.block); err == nil {
		t.Error("compact certificate verified without an anchored pool")
	}
}

// The full list still goes through the existing verifier unchanged.
func TestVerifyStoredBlockCertificate_FullListMalformed(t *testing.T) {
	b := &config.ZKBlock{BlockNumber: 3, CommitteeCertificate: "[not json"}
	if _, err := VerifyStoredBlockCertificate(b); err == nil {
		t.Fatal("malformed full list accepted")
	}
}

// A compact parent certificate has no individual signatures: the entropy
// rebuild attaches nothing rather than failing.
func TestCertificateForBlockAssembly_SkipsCompactParent(t *testing.T) {
	prevAgg, prevFn := AggCertEnabled, persistedCommitCertificateFn
	t.Cleanup(func() { AggCertEnabled, persistedCommitCertificateFn = prevAgg, prevFn })
	AggCertEnabled = true
	certForNextBlockMu.Lock()
	certForNextHeight = 0
	certForNextBlockMu.Unlock()

	persistedCommitCertificateFn = func(uint64) (string, error) { return `{"v":2,"signers":"fw==","agg_sig":"AA=="}`, nil }
	if got := CertificateForBlockAssembly(5, 4); got != nil {
		t.Fatalf("compact parent: got %d signers, want none", len(got))
	}

	f := newCertFixture(t, 3, 4)
	full, _ := json.Marshal(f.votes(t, 3, 0))
	persistedCommitCertificateFn = func(uint64) (string, error) { return string(full), nil }
	if got := CertificateForBlockAssembly(5, 4); len(got) != 3 {
		t.Fatalf("full parent: got %d signers, want 3", len(got))
	}
}

// The reward rule: from the switch height in anchored periods both stored
// forms pay only verified pool members; before it, or unanchored, the full
// list pays every Agree entry as before.
func TestCertSignersFromStored_RewardRule(t *testing.T) {
	f := newCertFixture(t, 7, 20)
	responses := f.votes(t, 6, 1)
	responses[0].Signature = responses[1].Signature // peer-00: YES that does not verify
	responses = append(responses, BLS_Signer.BLSresponse{PeerID: "outsider", Agree: true, PubKey: "aa", Signature: "bb"})
	full, _ := json.Marshal(responses)

	peers := func(s []config.CertSigner) string {
		var ids []string
		for _, c := range s {
			ids = append(ids, c.PeerID)
		}
		sort.Strings(ids)
		return strings.Join(ids, ",")
	}
	strictWant := strings.Join(f.ids[1:6], ",") // verified YES voters only
	lenient := append(append([]string{}, f.ids[:6]...), "outsider")
	sort.Strings(lenient)
	lenientWant := strings.Join(lenient, ",")

	// Switch on, anchored: the full list is filtered.
	got, err := certSignersFromStored(string(full), f.block)
	if err != nil || peers(got) != strictWant {
		t.Fatalf("strict full list = %s, %v; want %s", peers(got), err, strictWant)
	}
	// The compact form of the same votes pays the same set.
	compact := StoredCertificate(f.block, responses)
	if !IsCompactCertificate(compact) {
		t.Fatal("want compact form (5 verified votes reach quorum)")
	}
	got, err = certSignersFromStored(compact, f.block)
	if err != nil || peers(got) != strictWant {
		t.Fatalf("compact = %s, %v; want %s", peers(got), err, strictWant)
	}

	// Before the switch height: unchanged lenient rule.
	compactCertFromHeight = func() uint64 { return 21 }
	got, err = certSignersFromStored(string(full), f.block)
	if err != nil || peers(got) != lenientWant {
		t.Fatalf("before switch = %s, %v; want %s", peers(got), err, lenientWant)
	}
	compactCertFromHeight = func() uint64 { return 1 }

	// Unanchored period: unchanged lenient rule.
	compactCertPoolFn = func(uint64) (map[string]string, bool, error) { return nil, false, nil }
	got, err = certSignersFromStored(string(full), f.block)
	if err != nil || peers(got) != lenientWant {
		t.Fatalf("unanchored = %s, %v; want %s", peers(got), err, lenientWant)
	}
}
