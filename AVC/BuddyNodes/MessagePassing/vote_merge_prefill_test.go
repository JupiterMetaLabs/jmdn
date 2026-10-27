package MessagePassing

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"gossipnode/AVC/BuddyNodes/DataLayer"
	"gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/AVC/BuddyNodes/MessagePassing/Structs"
	"gossipnode/config"
	AVCStruct "gossipnode/config/PubSubMessages"

	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
	avctypes "github.com/JupiterMetaLabs/avc/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/libp2p/go-libp2p/core/peer"
)

// prefillEnv is one block's vote CRDT on a buddy, a victim validator with a
// genuine signed vote, and a relay that merges into it.
type prefillEnv struct {
	node          *AVCStruct.BuddyNode
	victim, relay peer.ID
	height        uint64
	hash          string
	consensusHash string
	genuine       avcvotes.VoteRecord
}

// newPrefillEnv sets up the block. seen=false leaves the block out of this
// node's consensus-message cache, so votesig: records cannot be verified yet.
func newPrefillEnv(t *testing.T, height uint64, seen bool) *prefillEnv {
	t.Helper()
	saved := avcvotes.DefaultWatermark
	avcvotes.DefaultWatermark = avcvotes.NewWatermark()
	t.Cleanup(func() { avcvotes.DefaultWatermark = saved })

	e := &prefillEnv{
		height:        height,
		hash:          common.HexToHash(fmt.Sprintf("0x%x", height)).String(),
		consensusHash: common.HexToHash(fmt.Sprintf("0xc0%x", height)).String(),
		victim:        mergeTestPeer(t),
		relay:         mergeTestPeer(t),
	}
	if seen {
		e.see()
	}

	t.Setenv("JMDN_BLS_AUTOGEN", "1")
	resp, ok, err := BLS_Signer.SignMessageForBlock(1, BLS_Signer.DomainChainID(), height, e.hash, e.consensusHash)
	if err != nil || !ok {
		t.Fatalf("sign: ok=%v err=%v", ok, err)
	}
	e.genuine = avcvotes.VoteRecord{PeerID: e.victim.String(), Vote: 1, BlockHash: e.hash, Height: height,
		BLSSignature: resp.Signature, BLSPubKeyHex: resp.PubKey}

	auth := map[string]string{e.victim.String(): strings.ToLower(e.genuine.BLSPubKeyHex)}
	Structs.SetAuthorizedVotersForHeightFn(func(uint64) (map[string]string, error) { return auth, nil })
	t.Cleanup(func() { Structs.SetAuthorizedVotersForHeightFn(nil) })

	e.node = &AVCStruct.BuddyNode{VoteCRDTLayer: &avctypes.Controller{CRDTLayer: DataLayer.NewVoteCRDTLayer(nil).CRDTLayer}}
	return e
}

// see puts the block in this node's consensus-message cache.
func (e *prefillEnv) see() {
	(&AVCStruct.ConsensusMessage{ZKBlock: &config.ZKBlock{
		BlockNumber:   e.height,
		BlockHash:     common.HexToHash(e.hash),
		ConsensusHash: common.HexToHash(e.consensusHash),
	}}).SetGloalVarCacheConsensusMessage()
}

func (e *prefillEnv) merge(t *testing.T, key string, elems ...string) {
	t.Helper()
	adds := make(map[string]interface{}, len(elems))
	for _, el := range elems {
		adds[el] = map[string]uint64{}
	}
	raw, _ := json.Marshal(rawLWWSet{Key: key, Adds: adds})
	if _, err := mergeVoteCRDTElement(e.node, e.relay, key, raw); err != nil {
		t.Fatal(err)
	}
}

func (e *prefillEnv) stored(key string) []string {
	s, _ := e.node.VoteCRDTLayer.CRDTLayer.GetSet(key)
	return s
}

func sigElem(r avcvotes.VoteRecord) string {
	b, _ := json.Marshal(r)
	return r.PeerID + ":" + string(b)
}

func (e *prefillEnv) junkSig(t *testing.T) string {
	t.Helper()
	r := e.genuine
	b := make([]byte, 64)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	r.BLSSignature = hex.EncodeToString(b)
	return sigElem(r)
}

// A-3 votes: pre-fill. The votes: key used to be merged with no check, so a
// relay could sync three junk elements under the victim's prefix. avc's
// AddVote counts every element with that prefix toward its cap, so the
// victim's own vote was then refused.
func TestMergeVoteCRDTElement_VotesKeyJunkCannotFillVictimCap(t *testing.T) {
	e := newPrefillEnv(t, 4500, true)
	key := avcvotes.BlockVoteKey(e.height, e.hash)
	v := e.victim.String()

	// Malformed values, plus spellings of 1 that ParseInt accepts but that
	// are distinct elements.
	e.merge(t, key, v+":x", v+":y", v+":z", v+":01", v+":+1", v+":1 ")

	for _, el := range e.stored(key) {
		t.Errorf("malformed element stored: %q", el)
	}
	if err := avcvotes.AddVote(e.node.VoteCRDTLayer, e.victim, e.genuine); err != nil {
		t.Fatalf("the victim's own vote must still be accepted: %v", err)
	}
}

// H7: an element without a colon has no peer prefix, so it skipped the
// per-peer cap entirely; invented peer IDs each got a cap of their own.
func TestMergeVoteCRDTElement_NoColonAndInventedPeersRefused(t *testing.T) {
	e := newPrefillEnv(t, 4600, true)
	voteKey := avcvotes.BlockVoteKey(e.height, e.hash)
	sigKey := avcvotes.BlockSigKey(e.height, e.hash)

	var junk []string
	for i := 0; i < 50; i++ {
		junk = append(junk, fmt.Sprintf("nocolon%d", i), fmt.Sprintf("invented-%d:1", i))
	}
	e.merge(t, voteKey, junk...)
	e.merge(t, sigKey, junk...)

	if got := e.stored(voteKey); len(got) != 0 {
		t.Fatalf("votes: key must refuse non-members and colon-less elements, stored %d", len(got))
	}
	if got := e.stored(sigKey); len(got) != 0 {
		t.Fatalf("votesig: key must refuse non-members and colon-less elements, stored %d", len(got))
	}

	// Members' genuine elements still go in.
	e.merge(t, voteKey, e.victim.String()+":1")
	e.merge(t, sigKey, sigElem(e.genuine))
	if len(e.stored(voteKey)) != 1 || len(e.stored(sigKey)) != 1 {
		t.Fatalf("genuine elements refused: votes=%d sigs=%d", len(e.stored(voteKey)), len(e.stored(sigKey)))
	}
}

// With no voter set, membership cannot be checked; the key is still bounded.
func TestMergeVoteCRDTElement_UnknownVoterSetIsBounded(t *testing.T) {
	e := newPrefillEnv(t, 4700, true)
	Structs.SetAuthorizedVotersForHeightFn(func(uint64) (map[string]string, error) {
		return nil, fmt.Errorf("voter set unavailable")
	})
	voteKey := avcvotes.BlockVoteKey(e.height, e.hash)

	var junk []string
	for i := 0; i < 500; i++ {
		junk = append(junk, fmt.Sprintf("invented-%d:1", i))
	}
	e.merge(t, voteKey, junk...)

	limit := config.MaxMainPeers * avcvotes.MaxElementsPerPeerPerBlock
	if got := len(e.stored(voteKey)); got > limit {
		t.Fatalf("an unverifiable key must stay bounded: stored %d > %d", got, limit)
	}
}

// While the voter set is unknown, invented peers can fill a key up to its
// bound. Once the set is known, those stored non-voters must not count against
// it, or a real voter's element is refused for good: a votes: element is never
// verifiable, so nothing else would let it in.
func TestMergeVoteCRDTElement_FakesFromUnknownVoterSetDoNotBlockLater(t *testing.T) {
	e := newPrefillEnv(t, 5000, true)
	auth := map[string]string{e.victim.String(): strings.ToLower(e.genuine.BLSPubKeyHex)}
	Structs.SetAuthorizedVotersForHeightFn(func(uint64) (map[string]string, error) {
		return nil, fmt.Errorf("voter set unavailable")
	})
	voteKey := avcvotes.BlockVoteKey(e.height, e.hash)

	var fakes []string
	for i := 0; i < config.MaxMainPeers*avcvotes.MaxElementsPerPeerPerBlock; i++ {
		fakes = append(fakes, fmt.Sprintf("invented-%d:1", i))
	}
	e.merge(t, voteKey, fakes...)

	Structs.SetAuthorizedVotersForHeightFn(func(uint64) (map[string]string, error) { return auth, nil })
	e.merge(t, voteKey, e.victim.String()+":1")

	for _, el := range e.stored(voteKey) {
		if el == e.victim.String()+":1" {
			return
		}
	}
	t.Fatal("a real voter's vote must be stored once the voter set is known, however many fakes came before")
}

// Gap 2: before this node has the block it cannot verify votesig: records.
// Junk stored in that window must not lock out the genuine record once the
// block is known.
func TestMergeVoteCRDTElement_UnverifiedJunkDoesNotLockOutGenuine(t *testing.T) {
	e := newPrefillEnv(t, 4800, false)
	key := avcvotes.BlockSigKey(e.height, e.hash)

	var junk []string
	for i := 0; i < 2*avcvotes.MaxElementsPerPeerPerBlock; i++ {
		junk = append(junk, e.junkSig(t))
	}
	e.merge(t, key, junk...)
	if got := len(e.stored(key)); got > avcvotes.MaxElementsPerPeerPerBlock {
		t.Fatalf("unverified records for one peer must stay under the cap, stored %d", got)
	}

	e.see()
	e.merge(t, key, sigElem(e.genuine))

	found := false
	for _, el := range e.stored(key) {
		if el == sigElem(e.genuine) {
			found = true
		}
	}
	if !found {
		t.Fatal("the genuine record must be stored once the block is known")
	}
}

// A signature does not cover RejectionReason or JSON spelling, so one genuine
// record has many verifying variants. Only one per (peer, vote) is kept, so
// variants cannot crowd out the same peer's other vote (equivocation evidence).
func TestMergeVoteCRDTElement_OneVerifiedRecordPerPeerVote(t *testing.T) {
	e := newPrefillEnv(t, 4900, true)
	key := avcvotes.BlockSigKey(e.height, e.hash)

	var variants []string
	for i := 0; i < 5; i++ {
		r := e.genuine
		r.RejectionReason = fmt.Sprintf("variant %d", i)
		variants = append(variants, sigElem(r))
	}
	e.merge(t, key, variants...)
	if got := len(e.stored(key)); got != 1 {
		t.Fatalf("want one verified record for (victim, +1), stored %d", got)
	}

	resp, ok, err := BLS_Signer.SignMessageForBlock(-1, BLS_Signer.DomainChainID(), e.height, e.hash, e.consensusHash)
	if err != nil || !ok {
		t.Fatalf("sign -1: ok=%v err=%v", ok, err)
	}
	against := e.genuine
	against.Vote, against.BLSSignature = -1, resp.Signature
	e.merge(t, key, sigElem(against))
	if got := len(e.stored(key)); got != 2 {
		t.Fatalf("the peer's other vote must still be stored, have %d records", got)
	}
}
