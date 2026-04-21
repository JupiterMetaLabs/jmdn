package PubSubMessages

// VoteNotification is pushed to the sequencer's vote collector channel
// when a vote arrives at the listener's handleSubmitVote handler.
type VoteNotification struct {
	PeerID    string // peer ID of the voter
	BlockHash string // block hash this vote is for (round scoping)
	Vote      int8   // +1 accept, -1 reject
}
