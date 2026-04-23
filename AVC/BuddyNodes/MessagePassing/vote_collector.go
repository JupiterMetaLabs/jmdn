package MessagePassing

import (
	"log"
	"sync"

	PubSubMessages "gossipnode/config/PubSubMessages"
)

// activeVoteCollector is the channel the sequencer registers to receive
// vote notifications pushed from handleSubmitVote. Only one consensus
// round is active per node at a time, so a single global channel is safe.
var (
	activeVoteCollector chan<- PubSubMessages.VoteNotification
	voteCollectorMu     sync.RWMutex
)

// RegisterVoteCollector sets the active vote notification channel.
// The sequencer calls this at the start of its event-driven vote collection loop.
func RegisterVoteCollector(ch chan<- PubSubMessages.VoteNotification) {
	voteCollectorMu.Lock()
	activeVoteCollector = ch
	voteCollectorMu.Unlock()
}

// UnregisterVoteCollector nils out the active collector.
// Called via defer when the consensus round completes or times out.
func UnregisterVoteCollector() {
	voteCollectorMu.Lock()
	activeVoteCollector = nil
	voteCollectorMu.Unlock()
}

// NotifyVoteCollector sends a vote notification to the active collector
// (if one is registered). The send is non-blocking; if the channel is full
// or no collector is registered, the notification is dropped with a log warning.
func NotifyVoteCollector(notification PubSubMessages.VoteNotification) {
	voteCollectorMu.RLock()
	collector := activeVoteCollector
	voteCollectorMu.RUnlock()

	if collector == nil {
		return
	}

	select {
	case collector <- notification:
		log.Printf("VoteCollector: notified sequencer of vote from %s for block %s (vote=%d)",
			notification.PeerID, notification.BlockHash, notification.Vote)
	default:
		log.Printf("VoteCollector: channel full, dropping vote notification from %s", notification.PeerID)
	}
}
