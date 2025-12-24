package helper

const (
	Alert_Consensus_InsufficientPeers                      = "Consensus Insufficient Peers"
	Alert_Consensus_FailedToPopulatePeerList               = "Failed to Populate Peer List"
	Alert_Consensus_BuiltFinalBuddiesList                  = "Built Final Buddies List"
	Alert_Consensus_FailedToSetZKBlockData                 = "Failed to Set ZK Block Data"
	Alert_Consensus_FailedToValidateConsensusConfiguration = "Failed to Validate Consensus Configuration"
	Alert_Consensus_FailedToSetGossipnode                  = "Failed to Set Gossipnode"
	Alert_Consensus_FailedToAddPeersToCache                = "Failed to add peers to cache"
	Alert_Consensus_Timeout                                = "Consensus Timeout"
	Alert_Consensus_FailedToCreatePubsubChannel            = "Failed to Create Pubsub Channel"
	Alert_Consensus_FailedToRequestSubscriptionPermission  = "Failed to Request Subscription Permission"
	Alert_Consensus_ZKBlockDataNotSet                      = "Failed: ZK Block Data Not Set"
	Alert_Consensus_FailedToBroadcastVoteTrigger           = "Failed to Broadcast Vote Trigger"
	//
	Alert_BFT_Consensus_NoBLSResultsCollected = "BFT Consensus No BLS Results Collected"
	Alert_BFT_Consensus_Reached               = "BFT Consensus Reached"
	Alert_BFT_Consensus_Failed                = "BFT Consensus Failed"
	//
	Alert_Consensus_ProcessBlockFailed_ZKBlockDataNotSet           = "Process Block Failed: ZKBlockData not initialized"
	Alert_Consensus_ProcessBlockFailed_FailedToProcessBlockLocally = "Process Block Failed: Failed to process block locally"
	Alert_Consensus_ProcessBlockSuccess_BlockProcessedLocally      = "Process Block Success: Block processed locally"
	Alert_Consensus_ProcessBlockFailed_ConsensusNotReached         = "Process Block Failed: Consensus not reached"
)
