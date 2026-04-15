package PubSubMessages

import (
	"context"
	"sync"

	log "gossipnode/logging"
	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/peer"
)

// < -- Singleton Pattern for gloabl variables -- >
type GlobalVariables struct{}

// SubscriptionTracker tracks active subscriptions
type SubscriptionTracker struct {
	AcceptedPeers map[peer.ID]bool
	ActiveCount   int
	BuddyNodes    map[peer.ID]string // peerID -> "main" or "backup"
	mutex         sync.RWMutex
}

var subscriptionTracker *SubscriptionTracker
var trackerOnce sync.Once

func GetSubscriptionTracker() *SubscriptionTracker {
	trackerOnce.Do(func() {
		subscriptionTracker = &SubscriptionTracker{
			AcceptedPeers: make(map[peer.ID]bool),
			ActiveCount:   0,
			BuddyNodes:    make(map[peer.ID]string),
		}
	})
	return subscriptionTracker
}

// MarkPeerAccepted marks a peer as accepted with role
func (st *SubscriptionTracker) MarkPeerAccepted(peerID peer.ID, role string) {
	st.mutex.Lock()
	defer st.mutex.Unlock()

	if !st.AcceptedPeers[peerID] {
		st.AcceptedPeers[peerID] = true
		st.ActiveCount++
		st.BuddyNodes[peerID] = role
		ctx := context.Background()
		logInstance, err := log.NewAsyncLogger().Get().NamedLogger(log.Config, "")
		if err == nil && logInstance != nil {
			logInstance.GetNamedLogger().Debug(ctx, "SubscriptionTracker: Marked peer as accepted",
				ion.String("peer_id", peerID.String()),
				ion.String("role", role),
				ion.Int("count", st.ActiveCount))
		}
	}
}

// IsPeerAccepted checks if a peer is accepted
func (st *SubscriptionTracker) IsPeerAccepted(peerID peer.ID) bool {
	st.mutex.RLock()
	defer st.mutex.RUnlock()
	return st.AcceptedPeers[peerID]
}

// GetActiveCount returns the current active count
func (st *SubscriptionTracker) GetActiveCount() int {
	st.mutex.RLock()
	defer st.mutex.RUnlock()
	return st.ActiveCount
}

// HasRequiredSubscriptions checks if we have enough subscriptions
func (st *SubscriptionTracker) HasRequiredSubscriptions(required int) bool {
	return st.GetActiveCount() >= required
}

// GetBuddyNodes returns the map of active buddy nodes with their roles
func (st *SubscriptionTracker) GetBuddyNodes() map[peer.ID]string {
	st.mutex.RLock()
	defer st.mutex.RUnlock()

	// Return a copy to avoid race conditions
	result := make(map[peer.ID]string)
	for peerID, role := range st.BuddyNodes {
		result[peerID] = role
	}
	return result
}

// GetMainPeers returns only the main peers
func (st *SubscriptionTracker) GetMainPeers() []peer.ID {
	st.mutex.RLock()
	defer st.mutex.RUnlock()

	var mainPeers []peer.ID
	for peerID, role := range st.BuddyNodes {
		if role == "main" {
			mainPeers = append(mainPeers, peerID)
		}
	}
	return mainPeers
}

// GetBackupPeers returns only the backup peers
func (st *SubscriptionTracker) GetBackupPeers() []peer.ID {
	st.mutex.RLock()
	defer st.mutex.RUnlock()

	var backupPeers []peer.ID
	for peerID, role := range st.BuddyNodes {
		if role == "backup" {
			backupPeers = append(backupPeers, peerID)
		}
	}
	return backupPeers
}

// Reset resets the tracker
func (st *SubscriptionTracker) Reset() {
	st.mutex.Lock()
	defer st.mutex.Unlock()
	st.AcceptedPeers = make(map[peer.ID]bool)
	st.ActiveCount = 0
	st.BuddyNodes = make(map[peer.ID]string)
}

func NewGlobalVariables() *GlobalVariables {
	return &GlobalVariables{}
}

func (globalvar *GlobalVariables) Set_PubSubNode(pubsub *BuddyNode) {
	PubSub_BuddyNode = pubsub
}

func (globalvar *GlobalVariables) Get_PubSubNode() *BuddyNode {
	if PubSub_BuddyNode == nil {
		ctx := context.Background()
		logInstance, err := log.NewAsyncLogger().Get().NamedLogger(log.Config, "")
		if err == nil && logInstance != nil {
			logInstance.GetNamedLogger().Warn(ctx, "PubSub_BuddyNode is nil - not initialized")
		}
		return nil
	}
	return PubSub_BuddyNode
}

// IsPubSubNodeInitialized checks if the PubSub node is properly initialized
func (globalvar *GlobalVariables) IsPubSubNodeInitialized() bool {
	return PubSub_BuddyNode != nil && PubSub_BuddyNode.PubSub != nil
}

func (globalvar *GlobalVariables) Set_ForListner(forlistener *BuddyNode) {
	ForListner = forlistener
}

func (globalvar *GlobalVariables) Get_ForListner() *BuddyNode {
	return ForListner
}
