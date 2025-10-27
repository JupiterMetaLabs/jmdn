package PubSubMessages

import (
	"fmt"
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"
)

// < -- Singleton Pattern for gloabl variables -- >
type GlobalVariables struct{}

// SubscriptionTracker tracks active subscriptions
type SubscriptionTracker struct {
	AcceptedPeers map[peer.ID]bool
	ActiveCount   int
	mutex         sync.RWMutex
}

var subscriptionTracker *SubscriptionTracker
var trackerOnce sync.Once

func GetSubscriptionTracker() *SubscriptionTracker {
	trackerOnce.Do(func() {
		subscriptionTracker = &SubscriptionTracker{
			AcceptedPeers: make(map[peer.ID]bool),
			ActiveCount:   0,
		}
	})
	return subscriptionTracker
}

// MarkPeerAccepted marks a peer as accepted
func (st *SubscriptionTracker) MarkPeerAccepted(peerID peer.ID) {
	st.mutex.Lock()
	defer st.mutex.Unlock()

	if !st.AcceptedPeers[peerID] {
		st.AcceptedPeers[peerID] = true
		st.ActiveCount++
		fmt.Printf("=== SubscriptionTracker: Marked peer %s as accepted (count: %d) ===\n", peerID, st.ActiveCount)
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

// Reset resets the tracker
func (st *SubscriptionTracker) Reset() {
	st.mutex.Lock()
	defer st.mutex.Unlock()
	st.AcceptedPeers = make(map[peer.ID]bool)
	st.ActiveCount = 0
}

func NewGlobalVariables() *GlobalVariables {
	return &GlobalVariables{}
}

func (globalvar *GlobalVariables) Set_PubSubNode(pubsub *BuddyNode) {
	PubSub_BuddyNode = pubsub
}

func (globalvar *GlobalVariables) Get_PubSubNode() *BuddyNode {
	if PubSub_BuddyNode == nil {
		fmt.Println("PubSub_BuddyNode is nil - not initialized")
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
