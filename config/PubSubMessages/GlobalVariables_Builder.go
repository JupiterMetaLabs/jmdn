package PubSubMessages

import (
	"fmt"
)

// < -- Singleton Pattern for gloabl variables -- >
type GlobalVariables struct{}

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
