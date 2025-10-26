package Triggers

import "time"

// This trigger is used to trigger the Close the accepting messages from the nodes for listener protocol
const ListeningTriggerMessage = "ListeningTrigger"
const ListeningTriggerBufferTime = 20 * time.Second

func ListeningTrigger(){
	time.AfterFunc(ListeningTriggerBufferTime, func() {
		// TODO: you need to close the protocol to accept the messages from nodes
	})
}

func ReleaseBuddyNodesTrigger(){
	// TODO: with the given context, if this trigger is not called before the end
	// of that context, you need to release the buddy nodes to the consensus message
}

func BFTTrigger(){
	// TODO call the BFT Consensus Start Function from here
}

func StartBFTConsensus(){
	
}