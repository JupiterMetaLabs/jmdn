package Structs

// Tests for IngestValidatorVote ("Count every validator's vote in the buddy
// tally again" LLD, §8 Unit (Structs/vote_ingest_test.go)). Each test drives
// IngestValidatorVote with the exact wire format Vote/Trigger.go's SubmitVote
// now produces (a JSON-encoded PubSubMessages.Vote carrying BLSSignature/
// BLSPubKeyHex, change C2) and checks the result through avcvotes.TallyBlock
// — the same function the real tally (processVotesFromCRDT_v2) calls — rather
// than reaching into the CRDT's internal keys directly.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"gossipnode/AVC/BuddyNodes/DataLayer"
	"gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	PubSubMessages "gossipnode/config/PubSubMessages"

	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
	avctypes "github.com/JupiterMetaLabs/avc/types"
	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	ingestChainID       = uint64(7100700)
	ingestHeight        = uint64(9500)
	ingestBlockHash     = "0xingest"
	ingestConsensusHash = "0xingest-consensus-hash"
)

func ingestTestPeer(t *testing.T) peer.ID {
	t.Helper()
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 0)
	if err != nil {
		t.Fatalf("generating test identity: %v", err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatalf("deriving peer ID: %v", err)
	}
	return id
}

func newIngestLayer() *avctypes.Controller {
	return &avctypes.Controller{CRDTLayer: DataLayer.NewVoteCRDTLayer(nil).CRDTLayer}
}

// ingestWireVote builds the raw JSON payload IngestValidatorVote decodes —
// identical in shape to what SubmitVote sends on the wire. Pass sign=false to
// produce an unsigned payload (pre-C2 shape / a node that failed to sign).
//
// Sets JMDN_BLS_AUTOGEN=1 itself (not left to each calling test) before
// signing: BLS_Signer.getBLSKeypair() caches its keypair once per process
// (sync.Once) from whatever JMDN_BLS_AUTOGEN read wins that first race, so
// this must not depend on an individual test remembering to set it, or on a
// provisioned AVC/BuddyNodes/MessagePassing/Structs/config/bls.json existing
// on the machine running the suite — it is the single place every signing
// call in this file goes through.
func ingestWireVote(t *testing.T, vote int8, height uint64, blockHash string, sign bool) (raw string, pubKeyHex string) {
	t.Helper()
	v := PubSubMessages.Vote{Vote: vote, BlockHash: blockHash, Height: height}
	if sign {
		t.Setenv("JMDN_BLS_AUTOGEN", "1")
		blsResp, signed, err := BLS_Signer.SignMessageForBlock(vote, ingestChainID, height, blockHash, ingestConsensusHash)
		if err != nil || !signed {
			t.Fatalf("SignMessageForBlock: signed=%v err=%v", signed, err)
		}
		v.BLSSignature = blsResp.Signature
		v.BLSPubKeyHex = blsResp.PubKey
		pubKeyHex = blsResp.PubKey
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal wire vote: %v", err)
	}
	return string(b), pubKeyHex
}

// withFreshWatermark isolates avcvotes.DefaultWatermark for one test, the
// same swap-and-restore pattern already used in
// AVC/BuddyNodes/MessagePassing/crdt_sync_a8_1_test.go — required because it
// is process-wide global state other tests also depend on.
func withFreshWatermark(t *testing.T) {
	t.Helper()
	saved := avcvotes.DefaultWatermark
	avcvotes.DefaultWatermark = avcvotes.NewWatermark()
	t.Cleanup(func() { avcvotes.DefaultWatermark = saved })
}

// 1. A valid signed vote is written, and TallyBlock with V containing the
// voter counts it.
func TestIngestValidatorVote_ValidSignedVote_CountedAtTally(t *testing.T) {
	withFreshWatermark(t)
	sender := ingestTestPeer(t)
	layer := newIngestLayer()

	raw, pubKeyHex := ingestWireVote(t, 1, ingestHeight, ingestBlockHash, true)
	if err := IngestValidatorVote(layer, sender, raw, "test"); err != nil {
		t.Fatalf("IngestValidatorVote: %v", err)
	}

	authorized := map[string]string{sender.String(): pubKeyHex}
	tally, err := avcvotes.TallyBlock(layer, ingestHeight, ingestBlockHash, authorized)
	if err != nil {
		t.Fatalf("TallyBlock: %v", err)
	}
	single := tally.SingleVotePeers()
	if got, ok := single[sender.String()]; !ok || got != 1 {
		t.Fatalf("expected the ingested vote to be counted as 1 for %s, got %+v", sender, single)
	}
}

// 2. A missing signature returns ErrUnsignedVote and nothing is written.
func TestIngestValidatorVote_MissingSignature_ReturnsErrUnsignedVote(t *testing.T) {
	withFreshWatermark(t)
	sender := ingestTestPeer(t)
	layer := newIngestLayer()

	raw, _ := ingestWireVote(t, 1, ingestHeight, ingestBlockHash, false)
	err := IngestValidatorVote(layer, sender, raw, "test")
	if !errors.Is(err, ErrUnsignedVote) {
		t.Fatalf("expected ErrUnsignedVote, got %v", err)
	}

	tally, tErr := avcvotes.TallyBlock(layer, ingestHeight, ingestBlockHash, map[string]string{sender.String(): "anything"})
	if tErr != nil {
		t.Fatalf("TallyBlock: %v", tErr)
	}
	if len(tally.AuthorizedVotesByPeer[sender.String()]) != 0 {
		t.Fatalf("an unsigned vote must not be written, found %+v", tally.AuthorizedVotesByPeer)
	}
}

// 3. Height 0 or an empty hash is rejected.
func TestIngestValidatorVote_MissingHeightOrBlockHash_Rejected(t *testing.T) {
	withFreshWatermark(t)
	layer := newIngestLayer()

	t.Run("height zero", func(t *testing.T) {
		sender := ingestTestPeer(t)
		raw, _ := ingestWireVote(t, 1, 0, ingestBlockHash, true)
		if err := IngestValidatorVote(layer, sender, raw, "test"); err == nil {
			t.Fatal("expected an error for height==0")
		}
	})

	t.Run("empty block hash", func(t *testing.T) {
		sender := ingestTestPeer(t)
		// Built directly (not via ingestWireVote/SignMessageForBlock): the
		// real signer itself refuses to sign an empty bindings string, so
		// this cannot arise from a genuine wire vote — but the check in
		// IngestValidatorVote must still catch it before ever reaching the
		// signature fields, which is what this pins. Non-empty but
		// otherwise-meaningless signature material, so this test cannot
		// pass merely because ErrUnsignedVote was returned instead.
		v := PubSubMessages.Vote{Vote: 1, BlockHash: "", Height: ingestHeight, BLSSignature: "deadbeef", BLSPubKeyHex: "deadbeef"}
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := IngestValidatorVote(layer, sender, string(b), "test"); err == nil {
			t.Fatal("expected an error for an empty block hash")
		} else if errors.Is(err, ErrUnsignedVote) {
			t.Fatalf("expected a missing-field error, not ErrUnsignedVote: %v", err)
		}
	})
}

// 4. Two different senders sending their own votes for the same block are
// each counted under their OWN transport-authenticated identity — not
// conflated, and not dependent on anything the payload itself claims (the
// wire Vote carries no peer_id field at all; identity comes entirely from
// the sender argument, per D2).
func TestIngestValidatorVote_DistinctSendersEachCountedUnderOwnIdentity(t *testing.T) {
	withFreshWatermark(t)
	layer := newIngestLayer()
	senderA := ingestTestPeer(t)
	senderB := ingestTestPeer(t)

	rawYes, pubKeyHex := ingestWireVote(t, 1, ingestHeight, ingestBlockHash, true)
	rawNo, _ := ingestWireVote(t, -1, ingestHeight, ingestBlockHash, true)

	if err := IngestValidatorVote(layer, senderA, rawYes, "test"); err != nil {
		t.Fatalf("IngestValidatorVote senderA: %v", err)
	}
	if err := IngestValidatorVote(layer, senderB, rawNo, "test"); err != nil {
		t.Fatalf("IngestValidatorVote senderB: %v", err)
	}

	authorized := map[string]string{senderA.String(): pubKeyHex, senderB.String(): pubKeyHex}
	tally, err := avcvotes.TallyBlock(layer, ingestHeight, ingestBlockHash, authorized)
	if err != nil {
		t.Fatalf("TallyBlock: %v", err)
	}
	single := tally.SingleVotePeers()
	if single[senderA.String()] != 1 {
		t.Fatalf("senderA: expected 1, got %+v", single)
	}
	if single[senderB.String()] != -1 {
		t.Fatalf("senderB: expected -1, got %+v", single)
	}
}

// 5. Duplicate delivery (the same wire vote arriving via both pubsub and the
// direct send) gives one counted vote, not an error and not a double-count.
func TestIngestValidatorVote_DuplicateDeliveryCountsOnce(t *testing.T) {
	withFreshWatermark(t)
	sender := ingestTestPeer(t)
	layer := newIngestLayer()

	raw, pubKeyHex := ingestWireVote(t, 1, ingestHeight, ingestBlockHash, true)
	if err := IngestValidatorVote(layer, sender, raw, "test"); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if err := IngestValidatorVote(layer, sender, raw, "test"); err != nil {
		t.Fatalf("duplicate ingest (identical redelivery) must not error: %v", err)
	}

	authorized := map[string]string{sender.String(): pubKeyHex}
	tally, err := avcvotes.TallyBlock(layer, ingestHeight, ingestBlockHash, authorized)
	if err != nil {
		t.Fatalf("TallyBlock: %v", err)
	}
	if votes := tally.AuthorizedVotesByPeer[sender.String()]; len(votes) != 1 || votes[0] != 1 {
		t.Fatalf("expected exactly one counted vote after duplicate delivery, got %+v", votes)
	}
}

// 6. A peer that casts two genuinely different, genuinely signed values for
// the same block (equivocation) has BOTH recorded — TallyBlock's own
// contract (see avc's BlockTally doc comment: it must not collapse this).
// This is also this code's natural analogue of the LLD's "cap" test: AddVote
// validates vote ∈ {1,-1} before the per-peer element cap is ever consulted,
// so a genuine 3rd DISTINCT vote value can never reach it through this path
// — the only two reachable distinct values are exercised here instead.
func TestIngestValidatorVote_EquivocatingVotesBothRecorded(t *testing.T) {
	withFreshWatermark(t)
	sender := ingestTestPeer(t)
	layer := newIngestLayer()

	rawYes, pubKeyHex := ingestWireVote(t, 1, ingestHeight, ingestBlockHash, true)
	rawNo, _ := ingestWireVote(t, -1, ingestHeight, ingestBlockHash, true)

	if err := IngestValidatorVote(layer, sender, rawYes, "test"); err != nil {
		t.Fatalf("ingest yes: %v", err)
	}
	if err := IngestValidatorVote(layer, sender, rawNo, "test"); err != nil {
		t.Fatalf("ingest no: %v", err)
	}

	authorized := map[string]string{sender.String(): pubKeyHex}
	tally, err := avcvotes.TallyBlock(layer, ingestHeight, ingestBlockHash, authorized)
	if err != nil {
		t.Fatalf("TallyBlock: %v", err)
	}
	if len(tally.EquivocatingPeers()) != 1 || tally.EquivocatingPeers()[0] != sender.String() {
		t.Fatalf("expected %s flagged as equivocating, got %v", sender, tally.EquivocatingPeers())
	}
}

// 7. A vote for an already-compacted height returns nil (not an error — an
// expected, harmless outcome) and writes nothing.
func TestIngestValidatorVote_CompactedHeight_ReturnsNilAndWritesNothing(t *testing.T) {
	withFreshWatermark(t)
	if err := avcvotes.DefaultWatermark.Set(ingestHeight+1000, 1); err != nil {
		t.Fatalf("Set watermark: %v", err)
	}
	sender := ingestTestPeer(t)
	layer := newIngestLayer()

	raw, _ := ingestWireVote(t, 1, ingestHeight, ingestBlockHash, true)
	if err := IngestValidatorVote(layer, sender, raw, "test"); err != nil {
		t.Fatalf("a compacted-height vote must return nil (expected/harmless), got: %v", err)
	}

	tally, tErr := avcvotes.TallyBlock(layer, ingestHeight, ingestBlockHash, map[string]string{sender.String(): "anything"})
	if tErr != nil {
		t.Fatalf("TallyBlock: %v", tErr)
	}
	if len(tally.AuthorizedVotesByPeer[sender.String()]) != 0 {
		t.Fatalf("a compacted-height vote must not be written, found %+v", tally.AuthorizedVotesByPeer)
	}
}

// 8. With the rollback flag off, ingest is a silent no-op: nil error, nothing
// written — the pre-LLD behaviour (received votes land only in the legacy
// CRDT).
func TestIngestValidatorVote_FlagOff_WritesNothing(t *testing.T) {
	withFreshWatermark(t)
	origEnabled := ValidatorVoteIngestEnabled
	ValidatorVoteIngestEnabled = false
	t.Cleanup(func() { ValidatorVoteIngestEnabled = origEnabled })

	sender := ingestTestPeer(t)
	layer := newIngestLayer()

	raw, _ := ingestWireVote(t, 1, ingestHeight, ingestBlockHash, true)
	if err := IngestValidatorVote(layer, sender, raw, "test"); err != nil {
		t.Fatalf("flag off must be a silent no-op, got: %v", err)
	}

	tally, tErr := avcvotes.TallyBlock(layer, ingestHeight, ingestBlockHash, map[string]string{sender.String(): "anything"})
	if tErr != nil {
		t.Fatalf("TallyBlock: %v", tErr)
	}
	if len(tally.AuthorizedVotesByPeer[sender.String()]) != 0 {
		t.Fatalf("flag off must write nothing, found %+v", tally.AuthorizedVotesByPeer)
	}
}

// The bounds in vote_ingest_limits.go. Before them, one peer in no committee
// stored 500 signature elements (~0.5 MB) under one key and opened 300
// far-future keys (PoC #2): avcvotes' per-peer cap counts the vote key, which
// holds at most "peer:1" and "peer:-1", so it never fired.

// withIngestLimits installs an ingest voter set (nil = source not installed)
// and a committed tip (ok=false = unknown), with fresh limit state, for one
// test. The tally's voter source is left unset; tests that need it set it.
func withIngestLimits(t *testing.T, voters []peer.ID, tip uint64, tipKnown bool) {
	t.Helper()
	withFreshWatermark(t)
	prevVoters, prevCommittee, prevCommitteeH := authorizedVotersForHeightFn, authorizedCommitteeFn, authorizedCommitteeForHeightFn
	prevIngest := ingestVotersForHeightFn
	prevTip, prevFlag, prevSet := committedTipFn, VoteIngestLimitsEnabled, ValidatorVoterSetEnabled
	prevBlocks := voterBlocks
	t.Cleanup(func() {
		authorizedVotersForHeightFn, authorizedCommitteeFn, authorizedCommitteeForHeightFn = prevVoters, prevCommittee, prevCommitteeH
		ingestVotersForHeightFn = prevIngest
		committedTipFn, VoteIngestLimitsEnabled, ValidatorVoterSetEnabled = prevTip, prevFlag, prevSet
		voterBlocks = prevBlocks
	})
	voterBlocks = expirable.NewLRU[string, []string](16384, nil, time.Hour)
	VoteIngestLimitsEnabled, ValidatorVoterSetEnabled = true, true
	authorizedCommitteeFn, authorizedCommitteeForHeightFn = nil, nil
	authorizedVotersForHeightFn, ingestVotersForHeightFn = nil, nil
	if voters != nil {
		set := make(map[string]string, len(voters))
		for _, v := range voters {
			set[v.String()] = ""
		}
		ingestVotersForHeightFn = func(uint64) (map[string]string, error) { return set, nil }
	}
	committedTipFn = func() (uint64, bool) { return tip, tipKnown }
}

// junkWireVote is a well-formed, "signed"-looking vote whose signature is
// junk: ingest does not verify signatures (the tally does), so it is stored.
func junkWireVote(t *testing.T, height uint64, block string, i int) string {
	t.Helper()
	b, err := json.Marshal(PubSubMessages.Vote{
		Vote: -1, BlockHash: block, Height: height,
		BLSSignature:    fmt.Sprintf("%0600x", i),
		BLSPubKeyHex:    "00",
		RejectionReason: strings.Repeat("x", 150),
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func sigElems(layer *avctypes.Controller, height uint64, block string, p peer.ID) int {
	return avcvotes.CountElementsForPeer(layer, avcvotes.BlockSigKey(height, block), p.String())
}

func TestVoteIngestLimits_NonVoterRefused(t *testing.T) {
	member, outsider := ingestTestPeer(t), ingestTestPeer(t)
	withIngestLimits(t, []peer.ID{member}, 100, true)
	layer := newIngestLayer()

	err := IngestValidatorVote(layer, outsider, junkWireVote(t, 101, "0xb", 0), "direct")
	if !errors.Is(err, ErrVoteIngestRefused) || !strings.Contains(err.Error(), "not_voter") {
		t.Fatalf("a peer outside the voter set must be refused, got %v", err)
	}
	if n := sigElems(layer, 101, "0xb", outsider); n != 0 {
		t.Fatalf("refused vote still wrote %d element(s)", n)
	}
	if err := IngestValidatorVote(layer, member, junkWireVote(t, 101, "0xb", 0), "direct"); err != nil {
		t.Fatalf("a voter's vote must be stored: %v", err)
	}
}

// When the voter set cannot be resolved, ingest stays lossless (LLD D5); the
// per-peer bounds still apply.
func TestVoteIngestLimits_VoterSetUnavailableStaysLossless(t *testing.T) {
	p := ingestTestPeer(t)
	withIngestLimits(t, nil, 100, true) // no voter source installed -> error
	if err := IngestValidatorVote(newIngestLayer(), p, junkWireVote(t, 101, "0xb", 0), "direct"); err != nil {
		t.Fatalf("with no voter source the vote must still be stored: %v", err)
	}
}

// A peer this node blocklists is still in the fleet-agreed pool: its vote is
// STORED (so CRDT sync republishes it to peers that count it) while this
// node's tally, which applies the blocklist, does not count it.
func TestVoteIngestLimits_BlocklistedPeerStoredButNotCounted(t *testing.T) {
	member, blocked := ingestTestPeer(t), ingestTestPeer(t)
	withIngestLimits(t, []peer.ID{member, blocked}, 100, true)
	layer := newIngestLayer()

	raw, pubKeyHex := ingestWireVote(t, 1, ingestHeight, ingestBlockHash, true)
	committedTipFn = func() (uint64, bool) { return ingestHeight - 1, true }
	// This node's tally set, as main.go wires it: the pool minus its blocklist.
	tallySet := map[string]string{member.String(): pubKeyHex}
	authorizedVotersForHeightFn = func(uint64) (map[string]string, error) { return tallySet, nil }
	if err := IngestValidatorVote(layer, blocked, raw, "pubsub"); err != nil {
		t.Fatalf("a blocklisted pool member's vote must be stored for relay: %v", err)
	}
	if n := sigElems(layer, ingestHeight, ingestBlockHash, blocked); n == 0 {
		t.Fatal("the blocklisted peer's vote was not stored")
	}

	tally, err := avcvotes.TallyBlock(layer, ingestHeight, ingestBlockHash, tallySet)
	if err != nil {
		t.Fatalf("TallyBlock: %v", err)
	}
	if _, counted := tally.SingleVotePeers()[blocked.String()]; counted {
		t.Fatal("the tally counted a peer outside its own (blocklist-filtered) set")
	}
}

// The ingest voter set does not follow JMDN_VALIDATOR_VOTER_SET: that flag
// governs who a node counts, not what it stores and relays.
func TestVoteIngestLimits_IngestIgnoresValidatorVoterSetFlag(t *testing.T) {
	member, outsider := ingestTestPeer(t), ingestTestPeer(t)
	withIngestLimits(t, []peer.ID{member}, 100, true)
	ValidatorVoterSetEnabled = false
	layer := newIngestLayer()

	if err := IngestValidatorVote(layer, member, junkWireVote(t, 101, "0xb", 0), "direct"); err != nil {
		t.Fatalf("a pool member's vote must be stored with the flag off: %v", err)
	}
	if err := IngestValidatorVote(layer, outsider, junkWireVote(t, 101, "0xb", 0), "direct"); !errors.Is(err, ErrVoteIngestRefused) {
		t.Fatalf("a peer outside the pool must still be refused with the flag off, got %v", err)
	}
}

func TestVoteIngestLimits_HeightWindow(t *testing.T) {
	p := ingestTestPeer(t)
	withIngestLimits(t, []peer.ID{p}, 1000, true)
	layer := newIngestLayer()

	if err := IngestValidatorVote(layer, p, junkWireVote(t, 1000+MaxVoteHeightAhead, "0xb", 0), "direct"); err != nil {
		t.Fatalf("a vote at the edge of the window must be stored: %v", err)
	}
	err := IngestValidatorVote(layer, p, junkWireVote(t, 1000+MaxVoteHeightAhead+1, "0xb", 0), "direct")
	if !errors.Is(err, ErrVoteIngestRefused) || !strings.Contains(err.Error(), "height_ahead") {
		t.Fatalf("a vote past the window must be refused, got %v", err)
	}
	if err := IngestValidatorVote(layer, p, junkWireVote(t, 9_000_000, "0xb", 0), "direct"); !errors.Is(err, ErrVoteIngestRefused) {
		t.Fatalf("a far-future vote must be refused, got %v", err)
	}
}

// Before the first commit the tip is unknown; the window is not applied.
func TestVoteIngestLimits_UnknownTipSkipsWindow(t *testing.T) {
	p := ingestTestPeer(t)
	withIngestLimits(t, []peer.ID{p}, 0, false)
	if err := IngestValidatorVote(newIngestLayer(), p, junkWireVote(t, 5_000, "0xb", 0), "direct"); err != nil {
		t.Fatalf("unknown tip: vote must be stored: %v", err)
	}
}

// The PoC itself, from a voter: 500 junk payloads for one block now store 3.
func TestVoteIngestLimits_SigKeyCappedPerPeer(t *testing.T) {
	p := ingestTestPeer(t)
	withIngestLimits(t, []peer.ID{p}, 100, true)
	layer := newIngestLayer()
	refused := 0
	for i := 0; i < 500; i++ {
		if err := IngestValidatorVote(layer, p, junkWireVote(t, 101, "0xb", i), "direct"); errors.Is(err, ErrVoteIngestRefused) {
			refused++
		}
	}
	if n := sigElems(layer, 101, "0xb", p); n != avcvotes.MaxElementsPerPeerPerBlock {
		t.Fatalf("sig elements stored = %d, want %d", n, avcvotes.MaxElementsPerPeerPerBlock)
	}
	if refused != 500-avcvotes.MaxElementsPerPeerPerBlock {
		t.Fatalf("refused %d, want %d", refused, 500-avcvotes.MaxElementsPerPeerPerBlock)
	}
}

// An honest voter that votes +1 then -1 on one block (equivocation, which the
// tally must see) still has both stored.
func TestVoteIngestLimits_EquivocationStillRecorded(t *testing.T) {
	p := ingestTestPeer(t)
	withIngestLimits(t, []peer.ID{p}, 100, true)
	layer := newIngestLayer()
	for _, v := range []int8{1, -1} {
		b, _ := json.Marshal(PubSubMessages.Vote{Vote: v, BlockHash: "0xb", Height: 101, BLSSignature: "aa", BLSPubKeyHex: "bb"})
		if err := IngestValidatorVote(layer, p, string(b), "direct"); err != nil {
			t.Fatalf("vote %d: %v", v, err)
		}
	}
	if n := sigElems(layer, 101, "0xb", p); n != 2 {
		t.Fatalf("both equivocating votes must be stored, got %d", n)
	}
}

func TestVoteIngestLimits_DistinctBlocksPerHeightCapped(t *testing.T) {
	p := ingestTestPeer(t)
	withIngestLimits(t, []peer.ID{p}, 100, true)
	layer := newIngestLayer()
	for i := 0; i < 20; i++ {
		err := IngestValidatorVote(layer, p, junkWireVote(t, 101, fmt.Sprintf("0xb%d", i), 0), "direct")
		if i < MaxBlocksPerVoterPerHeight && err != nil {
			t.Fatalf("block %d of %d must be stored: %v", i+1, MaxBlocksPerVoterPerHeight, err)
		}
		if i >= MaxBlocksPerVoterPerHeight && (!errors.Is(err, ErrVoteIngestRefused) || !strings.Contains(err.Error(), "block_cap")) {
			t.Fatalf("block %d must be refused (block_cap), got %v", i+1, err)
		}
	}
	// A repeat for an already-stored block is not a new block.
	if err := IngestValidatorVote(layer, p, junkWireVote(t, 101, "0xb0", 1), "direct"); err != nil {
		t.Fatalf("a second vote on an already-seen block must not hit block_cap: %v", err)
	}
	// Other heights have their own budget.
	if err := IngestValidatorVote(layer, p, junkWireVote(t, 102, "0xother", 0), "direct"); err != nil {
		t.Fatalf("another height must be unaffected: %v", err)
	}
}

// The block cap is checked and the hash recorded under one lock. They used to
// be separate steps with AddVote in between, so admissions that had not yet
// been recorded were invisible to the next check: here, eight admitted and
// unrecorded hashes let a ninth through.
func TestVoteIngestLimits_CheckReservesTheBlockSlot(t *testing.T) {
	p := ingestTestPeer(t)
	withIngestLimits(t, []peer.ID{p}, 100, true)
	layer := newIngestLayer()

	for i := 0; i < MaxBlocksPerVoterPerHeight; i++ {
		if reason, reserved := checkVoteIngestLimits(layer, p, 101, fmt.Sprintf("0xr%d", i)); reason != "" || !reserved {
			t.Fatalf("hash %d: reason=%q reserved=%v, want admitted and reserved", i, reason, reserved)
		}
	}
	if reason, _ := checkVoteIngestLimits(layer, p, 101, "0xr-extra"); reason != "block_cap" {
		t.Fatalf("a ninth hash admitted before any write completed: reason=%q, want block_cap", reason)
	}
	if reason, reserved := checkVoteIngestLimits(layer, p, 101, "0xr0"); reason != "" || reserved {
		t.Fatalf("a reserved hash again: reason=%q reserved=%v, want admitted, not re-reserved", reason, reserved)
	}
}

// Concurrent ingests for one (sender, height) with distinct hashes store at
// most MaxBlocksPerVoterPerHeight of them. Run with -race.
func TestVoteIngestLimits_ConcurrentDistinctBlocksStayCapped(t *testing.T) {
	p := ingestTestPeer(t)
	withIngestLimits(t, []peer.ID{p}, 100, true)
	layer := newIngestLayer()

	const n = 32
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		stored []string
	)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		block := fmt.Sprintf("0xc%d", i)
		raw := junkWireVote(t, 101, block, i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := IngestValidatorVote(layer, p, raw, "direct"); err == nil {
				mu.Lock()
				stored = append(stored, block)
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(stored) != MaxBlocksPerVoterPerHeight {
		t.Fatalf("stored %d distinct blocks for one (sender, height), want exactly %d", len(stored), MaxBlocksPerVoterPerHeight)
	}
	for _, b := range stored {
		if sigElems(layer, 101, b, p) == 0 {
			t.Fatalf("block %s reported stored but has no signature element", b)
		}
	}
}

// A write that does not store (here: a compacted height) gives its block slot
// back, so failed writes cannot use up the cap.
func TestVoteIngestLimits_FailedWriteReleasesTheSlot(t *testing.T) {
	p := ingestTestPeer(t)
	withIngestLimits(t, []peer.ID{p}, 100, true)
	if err := avcvotes.DefaultWatermark.Set(200, 1); err != nil {
		t.Fatalf("Set watermark: %v", err)
	}
	layer := newIngestLayer()

	if err := IngestValidatorVote(layer, p, junkWireVote(t, 101, "0xcompacted", 0), "direct"); err != nil {
		t.Fatalf("a compacted-height vote returns nil: %v", err)
	}
	if hashes, ok := voterBlocks.Get(voterBlocksKey(p, 101)); ok {
		t.Fatalf("a vote that was not stored still holds a block slot: %v", hashes)
	}
}

func TestVoteIngestLimits_FlagOffRestoresOldBehaviour(t *testing.T) {
	outsider := ingestTestPeer(t)
	withIngestLimits(t, []peer.ID{ingestTestPeer(t)}, 100, true)
	VoteIngestLimitsEnabled = false
	layer := newIngestLayer()
	for i := 0; i < 10; i++ {
		if err := IngestValidatorVote(layer, outsider, junkWireVote(t, 9_000_000, "0xb", i), "direct"); err != nil {
			t.Fatalf("flag off: %v", err)
		}
	}
	if n := sigElems(layer, 9_000_000, "0xb", outsider); n != 10 {
		t.Fatalf("flag off must store everything as before, got %d", n)
	}
}
