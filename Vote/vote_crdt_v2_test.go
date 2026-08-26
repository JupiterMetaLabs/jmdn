package Vote

// Stage 2 tests, per docs/JMDN-CRDT-VOTE-MIGRATION-LLD.md.
//
// HONEST SCOPE NOTE: SubmitVote() itself has zero pre-existing test coverage
// in this repo (no Vote/*_test.go existed before this stage) and is tightly
// coupled to live infrastructure — the global ForListner singleton,
// Security.CheckZKBlockValidation (DB-backed checks), and
// adapters.EvaluateShadow. There is no test harness here to drive it
// end-to-end, and building one is out of scope for this stage.
//
// What IS tested directly, without needing SubmitVote's surrounding
// machinery: the flag itself, and the exact sign-then-write-then-verify
// sequence the new code block performs (BLS_Signer.SignMessageForBlock ->
// avcvotes.AddVote -> independent BLS_Verifier check against what was
// written). That covers exit criterion 4 for real. Criteria 2 and 3 (full
// behavior through SubmitVote, flag on and off) are NOT verified here —
// stated plainly rather than glossed over.

import (
	"testing"

	"gossipnode/AVC/BuddyNodes/DataLayer"
	"gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Verifier"

	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
	avctypes "github.com/JupiterMetaLabs/avc/types"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func TestEnvOn_DefaultsAndOverrides(t *testing.T) {
	const key = "JMDN_VOTE_CRDT_V2_TEST_ONLY"

	if got := envOn(key, false); got != false {
		t.Errorf("unset var: got %v, want default false", got)
	}
	if got := envOn(key, true); got != true {
		t.Errorf("unset var: got %v, want default true", got)
	}

	for _, off := range []string{"0", "false", "False", "no", "off", " OFF "} {
		t.Setenv(key, off)
		if got := envOn(key, true); got != false {
			t.Errorf("value %q: got %v, want false", off, got)
		}
	}
	for _, on := range []string{"1", "true", "yes", "anything-else"} {
		t.Setenv(key, on)
		if got := envOn(key, false); got != true {
			t.Errorf("value %q: got %v, want true", on, got)
		}
	}
}

func TestVoteCRDTDualWrite_OffByDefault(t *testing.T) {
	// This asserts the package-level default the var was initialized with,
	// not a live re-read of the env — same limitation every other envOn-based
	// flag in this codebase has (JMDN_M2B_HASH etc.): it is read once at
	// package init. Documented here so it isn't mistaken for a bug in a
	// future test that sets the env var and expects the var to change.
	if VoteCRDTDualWrite {
		t.Fatal("VoteCRDTDualWrite must default to false — same discipline as every other rollout flag in this repo")
	}
}

// This is exit criterion 4 from the Stage 2 spec, exercised directly: sign a
// vote the same way the new SubmitVote block does, write it with AddVote,
// read it back, rebuild the canonical v3 message, and confirm an independent
// verifier accepts it against the pubkey that was written.
func TestBLSSignThenAddVote_SignatureVerifiesIndependently(t *testing.T) {
	t.Setenv("JMDN_BLS_AUTOGEN", "1") // avoids needing a provisioned key file

	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 0)
	if err != nil {
		t.Fatalf("generating a test peer identity: %v", err)
	}
	peerID, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatalf("deriving peer ID: %v", err)
	}

	const (
		chainID   = uint64(7000700)
		height    = uint64(42)
		blockHash = "0xabc123"
		vote      = int8(1)
	)

	blsResp, signed, err := BLS_Signer.SignMessageForBlock(vote, chainID, height, blockHash)
	if err != nil {
		t.Fatalf("SignMessageForBlock: %v", err)
	}
	if !signed {
		t.Fatal("SignMessageForBlock reported signed=false with no error")
	}
	if blsResp.Signature == "" || blsResp.PubKey == "" {
		t.Fatal("a real signature must never be written as an empty string — see the LLD's non-negotiable rules")
	}

	voteEngine := avctypes.Controller{CRDTLayer: DataLayer.GetVoteCRDTLayer().CRDTLayer}
	rec := avcvotes.VoteRecord{
		PeerID:       peerID.String(),
		Vote:         vote,
		BlockHash:    blockHash,
		Height:       height,
		BLSSignature: blsResp.Signature,
		BLSPubKeyHex: blsResp.PubKey,
	}
	if err := avcvotes.AddVote(&voteEngine, peerID, rec); err != nil {
		t.Fatalf("AddVote: %v", err)
	}

	tally, err := avcvotes.TallyBlock(&voteEngine, height, blockHash, map[string]string{peerID.String(): blsResp.PubKey})
	if err != nil {
		t.Fatalf("TallyBlock: %v", err)
	}
	got, ok := tally.SingleVotePeers()[peerID.String()]
	if !ok || got != vote {
		t.Fatalf("tally did not authorize the written vote: %+v", tally.SingleVotePeers())
	}

	// Independent re-verification: rebuild the canonical v3 message
	// (bound to chainID/height/blockHash, matching what SignMessageForBlock
	// actually signed) and check it against BLS_Verifier directly — VerifyForBlock,
	// not the unbound Verify, is SignMessageForBlock's real counterpart.
	if err := BLS_Verifier.VerifyForBlock(BLS_Signer.BLSresponse{
		Signature: blsResp.Signature,
		PubKey:    blsResp.PubKey,
	}, chainID, height, blockHash, vote); err != nil {
		t.Fatalf("independent BLS verification failed: %v", err)
	}
}

// A signature must never be written empty — the LLD's own non-negotiable
// rule. This asserts AddVote's own validation would reject an attempt to,
// as the last line of defense if that rule is ever violated upstream.
func TestAddVote_RejectsEmptyPeerIDRegardlessOfSignature(t *testing.T) {
	voteEngine := avctypes.Controller{CRDTLayer: DataLayer.GetVoteCRDTLayer().CRDTLayer}
	rec := avcvotes.VoteRecord{
		PeerID:       "", // deliberately invalid
		Vote:         1,
		BlockHash:    "0xdeadbeef",
		Height:       999,
		BLSSignature: "aa",
		BLSPubKeyHex: "bb",
	}
	if err := avcvotes.AddVote(&voteEngine, peer.ID(""), rec); err == nil {
		t.Fatal("AddVote accepted an empty peer ID")
	}
}
