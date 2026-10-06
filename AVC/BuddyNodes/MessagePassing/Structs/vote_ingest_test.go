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
	"testing"

	"gossipnode/AVC/BuddyNodes/DataLayer"
	"gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	PubSubMessages "gossipnode/config/PubSubMessages"

	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
	avctypes "github.com/JupiterMetaLabs/avc/types"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	ingestChainID   = uint64(7100700)
	ingestHeight    = uint64(9500)
	ingestBlockHash = "0xingest"
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
		blsResp, signed, err := BLS_Signer.SignMessageForBlock(vote, ingestChainID, height, blockHash, "")
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
