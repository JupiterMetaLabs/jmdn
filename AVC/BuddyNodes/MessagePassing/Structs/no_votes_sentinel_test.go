package Structs

import (
	"context"
	"errors"
	"testing"

	"gossipnode/AVC/BuddyNodes/DataLayer"
	"gossipnode/config"
	PubSubMessages "gossipnode/config/PubSubMessages"

	avctypes "github.com/JupiterMetaLabs/avc/types"
	"github.com/ethereum/go-ethereum/common"
)

// An empty vote CRDT (votes not replicated yet) is the normal transient state
// that callers retry on. It must surface as ErrNoVotesInCRDT so callers can
// tell it apart from a real failure, and keep its historical message.
func TestProcessVotesFromCRDT_V2_EmptyIsErrNoVotesInCRDT(t *testing.T) {
	origFn := authorizedCommitteeFn
	defer func() { authorizedCommitteeFn = origFn }()
	SetAuthorizedCommitteeFn(func() (map[string]string, error) {
		return map[string]string{stage5TestPeer(t).String(): "00"}, nil
	})

	engine := avctypes.Controller{CRDTLayer: DataLayer.NewVoteCRDTLayer(nil).CRDTLayer}
	listener := &PubSubMessages.BuddyNode{VoteCRDTLayer: &engine}

	// This test is specifically about the EMPTY-VOTES transient, not the
	// separate "this node has never seen the block at all" case that
	// processVotesFromCRDT_v2 now also checks — seed that cache so the empty
	// CRDT is the only thing under test here.
	(&PubSubMessages.ConsensusMessage{ZKBlock: &config.ZKBlock{
		BlockNumber:   stage5Height,
		BlockHash:     common.HexToHash(stage5BlockHash),
		ConsensusHash: common.HexToHash(stage5ConsensusHash),
	}}).SetGloalVarCacheConsensusMessage()

	result, _, cert, vcert, err := ProcessVotesFromCRDT(context.Background(), listener, stage5BlockHash, stage5Height)
	if !errors.Is(err, ErrNoVotesInCRDT) {
		t.Fatalf("want ErrNoVotesInCRDT, got %v", err)
	}
	if err.Error() != "no votes found in CRDT" {
		t.Fatalf("message changed: %q", err.Error())
	}
	if result != 0 || cert != nil || vcert != nil {
		t.Fatalf("empty CRDT must yield no decision/certificates: result=%d cert=%v vcert=%v", result, cert, vcert)
	}
}
