package MessagePassing

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func mergeTestPeer(t *testing.T) peer.ID {
	t.Helper()
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// A-3 (D-52): a buddy's sync merge used to store votesig: records unverified
// and count them toward the per-peer cap, so three junk records sent first
// locked a validator's genuine record out of every buddy that took the merge.
func TestMergeVoteCRDTElement_JunkCannotFillTheCap(t *testing.T) {
	saved := avcvotes.DefaultWatermark
	avcvotes.DefaultWatermark = avcvotes.NewWatermark()
	t.Cleanup(func() { avcvotes.DefaultWatermark = saved })

	height := uint64(4400)
	hash := common.HexToHash("0x4400").String()
	consensusHash := common.HexToHash("0xc044").String()
	(&AVCStruct.ConsensusMessage{ZKBlock: &config.ZKBlock{
		BlockNumber:   height,
		BlockHash:     common.HexToHash(hash),
		ConsensusHash: common.HexToHash(consensusHash),
	}}).SetGloalVarCacheConsensusMessage()

	t.Setenv("JMDN_BLS_AUTOGEN", "1")
	victim, relay := mergeTestPeer(t), mergeTestPeer(t)
	resp, ok, err := BLS_Signer.SignMessageForBlock(1, BLS_Signer.DomainChainID(), height, hash, consensusHash)
	if err != nil || !ok {
		t.Fatalf("sign: ok=%v err=%v", ok, err)
	}
	genuine := avcvotes.VoteRecord{PeerID: victim.String(), Vote: 1, BlockHash: hash, Height: height,
		BLSSignature: resp.Signature, BLSPubKeyHex: resp.PubKey}

	auth := map[string]string{victim.String(): strings.ToLower(genuine.BLSPubKeyHex)}
	Structs.SetAuthorizedVotersForHeightFn(func(uint64) (map[string]string, error) { return auth, nil })
	t.Cleanup(func() { Structs.SetAuthorizedVotersForHeightFn(nil) })

	elem := func(r avcvotes.VoteRecord) string {
		b, _ := json.Marshal(r)
		return r.PeerID + ":" + string(b)
	}
	node := &AVCStruct.BuddyNode{VoteCRDTLayer: &avctypes.Controller{CRDTLayer: DataLayer.NewVoteCRDTLayer(nil).CRDTLayer}}
	key := avcvotes.BlockSigKey(height, hash)
	merge := func(elems ...string) {
		adds := make(map[string]interface{}, len(elems))
		for _, e := range elems {
			adds[e] = map[string]uint64{}
		}
		raw, _ := json.Marshal(rawLWWSet{Key: key, Adds: adds})
		if _, err := mergeVoteCRDTElement(node, relay, key, raw); err != nil {
			t.Fatal(err)
		}
	}

	var junk []string
	for i := 0; i < avcvotes.MaxElementsPerPeerPerBlock; i++ {
		r := genuine
		b := make([]byte, 64)
		_, _ = rand.Read(b)
		r.BLSSignature = hex.EncodeToString(b)
		junk = append(junk, elem(r))
	}
	merge(junk...)
	merge(elem(genuine))

	stored, _ := node.VoteCRDTLayer.CRDTLayer.GetSet(key)
	if len(stored) != 1 || stored[0] != elem(genuine) {
		t.Fatalf("want only the genuine record stored, got %d elements", len(stored))
	}
}
