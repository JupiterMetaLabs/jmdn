package messaging

// A test entropy committee whose members hold real BLS keys, installed through
// revealCommitteeSource. Tests of the inbox, fold, produce and replay
// mechanics use it; the anchored committee path itself is tested end to end in
// entropy_reveal_bls_test.go.

import (
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/JupiterMetaLabs/avc/committee"
	"github.com/JupiterMetaLabs/avc/randao"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
)

type revealKeyring struct {
	t    *testing.T
	mu   sync.Mutex
	keys map[string]testAuthority
	ids  []string
	// only, when set for an epoch, is that epoch's committee; otherwise every
	// registered peer is a member of every epoch.
	only map[uint64][]string
}

// withEntropyCutover places the entropy cut-over at epoch for the test (tests
// run on the testnet chain ID, whose real cut-over is listed in
// entropyCutoverByChain) and restores it afterwards.
func withEntropyCutover(t *testing.T, epoch uint64) {
	t.Helper()
	prev := entropyCutoverFn
	entropyCutoverFn = func() uint64 { return epoch }
	pruneBLSRevealCommitteesBelow(^uint64(0))
	t.Cleanup(func() {
		entropyCutoverFn = prev
		pruneBLSRevealCommitteesBelow(^uint64(0))
	})
}

// withRevealKeyring installs an empty keyring as the committee source and
// restores the production source afterwards. Every epoch is a BLS epoch
// (cut-over 0) unless the test places the cut-over itself afterwards.
func withRevealKeyring(t *testing.T) *revealKeyring {
	t.Helper()
	withEntropyCutover(t, 0)
	kr := &revealKeyring{t: t, keys: map[string]testAuthority{}, only: map[uint64][]string{}}
	prev := revealCommitteeSource
	revealCommitteeSource = kr.committee
	pruneBLSRevealCommitteesBelow(^uint64(0))
	t.Cleanup(func() {
		revealCommitteeSource = prev
		pruneBLSRevealCommitteesBelow(^uint64(0))
	})
	return kr
}

func (kr *revealKeyring) committee(epoch uint64) ([]committee.Member, error) {
	kr.mu.Lock()
	defer kr.mu.Unlock()
	ids, ok := kr.only[epoch]
	if !ok {
		ids = kr.ids
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: epoch %d (test keyring has no members)", ErrBLSRevealNeedsAnchoredPool, epoch)
	}
	out := make([]committee.Member, 0, len(ids))
	for _, id := range ids {
		pub, err := hex.DecodeString(kr.keys[id].pubHex)
		if err != nil {
			return nil, err
		}
		out = append(out, committee.Member{PeerID: id, BLSPub: pub, Weight: 1})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PeerID < out[j].PeerID })
	return out, nil
}

// add registers id with a fresh BLS key and makes it a member of every epoch
// not restricted by setMembers. The committee cache is dropped so the new
// member is seen at once.
func (kr *revealKeyring) add(id string) {
	kr.t.Helper()
	k := newTestAuthority(kr.t)
	kr.mu.Lock()
	if _, dup := kr.keys[id]; !dup {
		kr.ids = append(kr.ids, id)
	}
	kr.keys[id] = k
	kr.mu.Unlock()
	pruneBLSRevealCommitteesBelow(^uint64(0))
}

// setMembers restricts epoch's committee to ids (each must be added).
func (kr *revealKeyring) setMembers(epoch uint64, ids ...string) {
	kr.mu.Lock()
	kr.only[epoch] = append([]string(nil), ids...)
	kr.mu.Unlock()
	pruneBLSRevealCommitteesBelow(^uint64(0))
}

// reveal is id's BLS reveal for epoch.
func (kr *revealKeyring) reveal(id string, epoch uint64) []byte {
	kr.t.Helper()
	kr.mu.Lock()
	k, ok := kr.keys[id]
	kr.mu.Unlock()
	if !ok {
		kr.t.Fatalf("reveal: %s has no key", id)
	}
	sig, err := randao.SignRevealBLS(k.priv, BLS_Signer.DomainChainID(), epoch, id)
	if err != nil {
		kr.t.Fatal(err)
	}
	return sig
}

// useKeyOf makes this node's BLS key id's key (what loadRevealBLSKey returns),
// restoring the previous loader afterwards.
func (kr *revealKeyring) useKeyOf(t *testing.T, id string) {
	t.Helper()
	kr.mu.Lock()
	k, ok := kr.keys[id]
	kr.mu.Unlock()
	if !ok {
		t.Fatalf("useKeyOf: %s has no key", id)
	}
	pub, err := hex.DecodeString(k.pubHex)
	if err != nil {
		t.Fatal(err)
	}
	prev := loadRevealBLSKey
	loadRevealBLSKey = func() ([]byte, []byte, error) { return k.priv, pub, nil }
	t.Cleanup(func() { loadRevealBLSKey = prev })
}
