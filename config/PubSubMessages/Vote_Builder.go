package PubSubMessages

import (
	"strconv"

	"gossipnode/AVC/BuddyNodes/Types"

	"github.com/libp2p/go-libp2p/core/peer"
)

func (vote *Vote) SetVote(voteInput int8) *Vote {
	vote.Vote = voteInput
	return vote
}

func (vote *Vote) GetVote() int8 {
	return vote.Vote
}

func (vote *Vote) SetBlockHash(blockHash string) *Vote {
	vote.BlockHash = blockHash
	return vote
}

func (vote *Vote) GetBlockHash() string {
	return vote.BlockHash
}

func (vote *Vote) ReturnOP(peerID peer.ID) *Types.KeyValue {
	return &Types.KeyValue{
		Key:   peerID.String(),
		Value: strconv.Itoa(int(vote.Vote)),
	}
}
