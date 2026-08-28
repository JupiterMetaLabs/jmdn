package MessagePassing

// Stage 3 tests, per docs/JMDN-CRDT-VOTE-MIGRATION-LLD.md.
//
// mergeCRDTData and buildLocalSyncData are tested directly, without the
// surrounding pubsub/host machinery TriggerCRDTSyncForBuddyNode needs — both
// are pure functions over a *AVCStruct.BuddyNode and plain data, which is
// exactly what makes them worth having extracted as their own functions.

import (
	"encoding/json"
	"testing"

	"gossipnode/AVC/BuddyNodes/CRDTSync"
	"gossipnode/AVC/BuddyNodes/DataLayer"
	AVCStruct "gossipnode/config/PubSubMessages"

	avcdatalayer "github.com/JupiterMetaLabs/avc/buddynodes/datalayer"
	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// freshTestNode returns a BuddyNode with its own isolated pair of engines —
// never the process-wide singletons — so tests never see another test's
// data and can run with t.Parallel if ever needed.
func freshTestNode(t *testing.T) (*AVCStruct.BuddyNode, peer.ID) {
	t.Helper()
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 0)
	if err != nil {
		t.Fatalf("generating test identity: %v", err)
	}
	peerID, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatalf("deriving peer ID: %v", err)
	}
	return &AVCStruct.BuddyNode{
		PeerID:        peerID,
		CRDTLayer:     DataLayer.NewCRDTLayer(nil),
		VoteCRDTLayer: DataLayer.NewVoteCRDTLayer(nil),
	}, peerID
}

// rawLWWSetJSON builds the wire bytes mergeCRDTData expects for one CRDT
// object: the envelope both engines share, with the given element set as
// Adds. Mirrors rawLWWSet's own json tags exactly.
func rawLWWSetJSON(t *testing.T, key string, elements ...string) json.RawMessage {
	t.Helper()
	adds := make(map[string]any, len(elements))
	for _, e := range elements {
		adds[e] = nil // VectorClock value is never read by the merge path
	}
	data, err := json.Marshal(rawLWWSet{Key: key, Adds: adds, Removes: map[string]any{}})
	if err != nil {
		t.Fatalf("marshaling rawLWWSet fixture: %v", err)
	}
	return data
}

// The central promise of Stage 3: one sync message carrying both keyspaces
// merges each element into the CORRECT engine, and never into the other.
func TestMergeCRDTData_RoutesLegacyAndV2KeysToTheirOwnEngines(t *testing.T) {
	node, _ := freshTestNode(t)
	_, senderID := freshTestNode(t) // the remote peer this sync message is "from"

	const (
		legacyVoteJSON = `{"vote":1,"block_hash":"0xlegacy"}`
		height         = uint64(500)
		blockHash      = "0xabc123"
	)
	voteElem := senderID.String() + ":1"
	voteKey := avcvotes.BlockVoteKey(height, blockHash)

	syncMsg := CRDTSync.Message{
		NodeID: senderID.String(),
		SyncData: map[string]json.RawMessage{
			senderID.String(): rawLWWSetJSON(t, senderID.String(), legacyVoteJSON),
			voteKey:           rawLWWSetJSON(t, voteKey, voteElem),
		},
	}

	if err := mergeCRDTData(node, syncMsg); err != nil {
		t.Fatalf("mergeCRDTData: %v", err)
	}

	// Landed in the legacy engine, under the legacy key.
	legacySet, ok := DataLayer.GetSet(node.CRDTLayer, senderID.String())
	if !ok || len(legacySet) != 1 || legacySet[0] != legacyVoteJSON {
		t.Fatalf("legacy engine got %v (ok=%v), want [%q]", legacySet, ok, legacyVoteJSON)
	}

	// Landed in the v2 engine, under the block-keyed key.
	voteSet, ok := avcdatalayer.GetSet(node.VoteCRDTLayer, voteKey)
	if !ok || len(voteSet) != 1 || voteSet[0] != voteElem {
		t.Fatalf("v2 engine got %v (ok=%v), want [%q]", voteSet, ok, voteElem)
	}

	// Did NOT cross over: the legacy key must not exist in the v2 engine,
	// and the vote key must not exist in the legacy engine. This is the
	// contamination check the LLD's §4.2 names explicitly.
	if _, ok := avcdatalayer.GetSet(node.VoteCRDTLayer, senderID.String()); ok {
		t.Fatal("the legacy peer-keyed entry leaked into the v2 engine")
	}
	if _, ok := DataLayer.GetSet(node.CRDTLayer, voteKey); ok {
		t.Fatal("the block-keyed vote entry leaked into the legacy engine")
	}
}

// A v2-shaped element must be tallyable after merging, through the real
// TallyBlock path — not just present as a raw string. This is the actual
// point of the whole sync stage: a vote cast on one node must become
// visible and authenticatable on another.
func TestMergeCRDTData_MergedV2VoteIsTallyable(t *testing.T) {
	node, _ := freshTestNode(t)
	_, voterID := freshTestNode(t)

	const height, blockHash = uint64(10), "0xdead"
	voteKey := avcvotes.BlockVoteKey(height, blockHash)
	sigKey := avcvotes.BlockSigKey(height, blockHash)

	rec := avcvotes.VoteRecord{
		PeerID:       voterID.String(),
		Vote:         1,
		BlockHash:    blockHash,
		Height:       height,
		BLSSignature: "aa",
		BLSPubKeyHex: "bb",
	}
	sigJSON, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshaling VoteRecord fixture: %v", err)
	}

	syncMsg := CRDTSync.Message{
		NodeID: voterID.String(),
		SyncData: map[string]json.RawMessage{
			voteKey: rawLWWSetJSON(t, voteKey, voterID.String()+":1"),
			sigKey:  rawLWWSetJSON(t, sigKey, voterID.String()+":"+string(sigJSON)),
		},
	}

	if err := mergeCRDTData(node, syncMsg); err != nil {
		t.Fatalf("mergeCRDTData: %v", err)
	}

	tally, err := avcvotes.TallyBlock(node.VoteCRDTLayer, height, blockHash,
		map[string]string{voterID.String(): "bb"})
	if err != nil {
		t.Fatalf("TallyBlock: %v", err)
	}
	if got, ok := tally.SingleVotePeers()[voterID.String()]; !ok || got != 1 {
		t.Fatalf("merged vote is not authorized/tallyable: %+v", tally.SingleVotePeers())
	}
}

// A node still mid-migration — VoteCRDTLayer nil, e.g. before Stage 1 ships
// on some node in a mixed rollout — must not fail merging the legacy portion
// of a sync message just because the v2 portion has nowhere to go.
func TestMergeCRDTData_NilVoteCRDTLayerDegradesLegacyOnly(t *testing.T) {
	node, _ := freshTestNode(t)
	node.VoteCRDTLayer = nil
	_, senderID := freshTestNode(t)

	syncMsg := CRDTSync.Message{
		NodeID: senderID.String(),
		SyncData: map[string]json.RawMessage{
			senderID.String():                  rawLWWSetJSON(t, senderID.String(), `{"vote":1}`),
			avcvotes.BlockVoteKey(1, "0xhash"): rawLWWSetJSON(t, "k", "irrelevant:1"),
		},
	}

	if err := mergeCRDTData(node, syncMsg); err != nil {
		t.Fatalf("mergeCRDTData must not fail outright when only VoteCRDTLayer is nil: %v", err)
	}
	if got, ok := DataLayer.GetSet(node.CRDTLayer, senderID.String()); !ok || len(got) != 1 {
		t.Fatalf("legacy element should still have merged, got %v (ok=%v)", got, ok)
	}
}

func TestBuildLocalSyncData_CombinesBothEnginesWithoutCollision(t *testing.T) {
	node, selfID := freshTestNode(t)

	if err := DataLayer.Add(node.CRDTLayer, selfID, selfID.String(), `{"vote":1}`); err != nil {
		t.Fatalf("seeding legacy engine: %v", err)
	}
	rec := avcvotes.VoteRecord{PeerID: selfID.String(), Vote: 1, BlockHash: "0xh", Height: 7, BLSSignature: "aa", BLSPubKeyHex: "bb"}
	if err := avcvotes.AddVote(node.VoteCRDTLayer, selfID, rec); err != nil {
		t.Fatalf("seeding v2 engine: %v", err)
	}

	syncData := buildLocalSyncData(node)

	if _, ok := syncData[selfID.String()]; !ok {
		t.Error("legacy key missing from combined sync data")
	}
	voteKey := avcvotes.BlockVoteKey(7, "0xh")
	sigKey := avcvotes.BlockSigKey(7, "0xh")
	if _, ok := syncData[voteKey]; !ok {
		t.Error("v2 vote key missing from combined sync data")
	}
	if _, ok := syncData[sigKey]; !ok {
		t.Error("v2 sig key missing from combined sync data")
	}
	// Three distinct keys, three distinct entries — nothing was overwritten
	// by the union, which is the collision risk votes.OwnsKey exists to rule
	// out by construction.
	if len(syncData) != 3 {
		t.Errorf("got %d entries, want 3 (1 legacy + votes: + votesig:): %v", len(syncData), keysOf(syncData))
	}
}

func TestBuildLocalSyncData_NilVoteCRDTLayerStillPublishesLegacy(t *testing.T) {
	node, selfID := freshTestNode(t)
	node.VoteCRDTLayer = nil

	if err := DataLayer.Add(node.CRDTLayer, selfID, selfID.String(), `{"vote":1}`); err != nil {
		t.Fatalf("seeding legacy engine: %v", err)
	}

	syncData := buildLocalSyncData(node)
	if len(syncData) != 1 {
		t.Fatalf("got %d entries, want exactly the 1 legacy entry: %v", len(syncData), keysOf(syncData))
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
