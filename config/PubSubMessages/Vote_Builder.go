package PubSubMessages

import (
	"strconv"

	"gossipnode/AVC/BuddyNodes/Types"

	"github.com/libp2p/go-libp2p/core/peer"
)

// __DEAD_CODE_AUDIT_PUBLIC__
func NewVoteBuilder(vote *Vote) *Vote {
	if vote != nil {
		return &Vote{
			Vote:      vote.Vote,
			BlockHash: vote.BlockHash,
		}
	}
	return &Vote{}
}

// __DEAD_CODE_AUDIT_PUBLIC__
func (vote *Vote) SetVote(voteInput int8) *Vote {
	vote.Vote = voteInput
	return vote
}

func (vote *Vote) GetVote() int8 {
	return vote.Vote
}

// __DEAD_CODE_AUDIT_PUBLIC__
func (vote *Vote) SetBlockHash(blockHash string) *Vote {
	vote.BlockHash = blockHash
	return vote
}

// __DEAD_CODE_AUDIT_PUBLIC__
func (vote *Vote) GetBlockHash() string {
	return vote.BlockHash
}

func (vote *Vote) ReturnOP(peerID peer.ID) *Types.KeyValue {
	return &Types.KeyValue{
		Key:   peerID.String(),
		Value: strconv.Itoa(int(vote.Vote)),
	}
}
