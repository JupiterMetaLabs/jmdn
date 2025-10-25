package PubSubMessages

// < -- Singleton Pattern for gloabl variables -- >
type GlobalVariables struct{}

func NewGlobalVariables() *GlobalVariables {
	return &GlobalVariables{}
}

func (globalvar *GlobalVariables) Set_PubSubNode(pubsub *BuddyNode) {
	PubSub_BuddyNode = pubsub
}

func (globalvar *GlobalVariables) Get_PubSubNode() *BuddyNode {
	return PubSub_BuddyNode
}

func (globalvar *GlobalVariables) Set_ForListner(forlistener *BuddyNode) {
	ForListner = forlistener
}

func (globalvar *GlobalVariables) Get_ForListner() *BuddyNode {
	return ForListner
}
