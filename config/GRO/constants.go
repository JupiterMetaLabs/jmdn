package GRO

import (
	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
)

var (
	GlobalGRO interfaces.GlobalGoroutineManagerInterface
	apps      *appmanager
)

// This is the variable tracking for all pulled up app manager
const (
	// main.go
	MainLM = "local:main"

	// Sequencer/Triggers/Triggers.go
	SequencerTriggerLocal   = "local:sequencer:trigger"
	SequencerConsensusLocal = "local:sequencer:consensus"
	SeedLocal               = "local:seed"
	PubsubSubscribeLocal    = "local:pubsub:subscription"
	PubsubPublishLocal      = "local:pubsub:publish"
	PubsubChannelLocal      = "local:pubsub:channel"
	NodeLocal               = "local:node"
	MessagingLocal          = "local:messaging"
	DIDPropagationLocal     = "local:did:propagation"
	BroadcastLocal          = "local:direct:msg"

	BlockPropagationLocal = "local:block:propagation"

	MetricsLocal = "local:metrics"

	BFTLocal = "local:bft"

	CRDTSyncLocal = "local:crdt:sync"

	HandleBFTRequestLocal                  = "local:bft:request"
	StreamCacheParallelCleanUpRoutineLocal = "local:stream:cache:parallel:clean:up:routine"

	NodeDiscoveryLocal = "local:node:discovery"

	BlockGRPCServerLocal = "local:block:grpc:server"

	CLILocal = "local:cli"

	ConnectionPoolLocal = "local:connection:pool"
	AddPeersCacheLocal  = "local:add:peers:cache"

	DB_OPsLocal           = "local:db:ops"
	DB_OPsImmuclientLocal = "local:db:ops:immuclient"

	ExplorerBlockOpsLocal = "local:explorer:block:ops"

	GETHLocal   = "local:geth"
	FacadeLocal = "local:facade"

	LoggingLocal = "local:logging"
)

const (
	FacadeThread      = "thread:facade"
	WSServerThread    = "thread:ws"
	CLIThread         = "thread:cli"
	ExplorerThread    = "thread:explorer"
	GETHgRPCThread    = "thread:geth:grpc"
	BlockgRPCThread   = "thread:block:grpc"
	BlockgenThread    = "thread:block:gen"
	DIDThread         = "thread:did"
	ShutdownThread    = "thread:shutdown"
	BlockPollerThread = "thread:block:poller"

	// SequencerTriggerThread   = "thread:sequencer:trigger"
	SequencerConsensusThread              = "thread:sequencer:consensus"
	SequencerVoteCollectionThread         = "thread:sequencer:vote:collection"
	SequencerRequestEventDrivenFlowThread = "thread:sequencer:request:event:driven:flow"
	// SeedThread               = "thread:seed:maintenance"

	PubsubSubscriptionThread = "thread:pubsub:subscription"
	PubsubPublishThread      = "thread:pubsub:publish"
	PubsubChannelThread      = "thread:pubsub:channel"
	PubsubCacheCleanupThread = "thread:pubsub:cache:cleanup"

	NodeThread          = "thread:node"
	NodeDiscoveryThread = "thread:node:discovery"
	NodeStreamThread    = "thread:node:stream"

	MessageListenerThread   = "thread:messaging:listener"
	YGGMessageCleanerThread = "thread:messaging:yggdrasil:cleaner"
	MessageYggdrasilThread  = "thread:messaging:yggdrasil"

	DIDStoreThread             = "thread:did:store"
	DIDPropagationThread       = "thread:did:propagation"
	DIDForwardThread           = "thread:did:forward"
	DIDPropagationStreamThread = "thread:did:propagation:stream"

	MessageCleanerThread   = "thread:messaging:cleaner"
	MessageBroadcastThread = "thread:messaging:broadcast"
	VoteBroadcastThread    = "thread:messaging:vote:broadcast"
	BroadcastBlockThread   = "thread:messaging:block:broadcast"
	BroadcastCleanupThread = "thread:messaging:broadcast:cleanup"

	RecordMetricsThread = "thread:metrics:record"
	MetricsServerThread = "thread:metrics:server"

	BlockPropagationPeersCleanupThread           = "thread:block:propagation:peers:cleanup"
	BlockPropagationProcessAndValidateThread     = "thread:block:propagation:process:and:validate"
	BlockPropagationForwardThread                = "thread:block:propagation:forward"
	BlockPropagationZKBlockThread                = "thread:block:propagation:zkblock"
	BlockPropagationWaitForConsensusResultThread = "thread:block:propagation:wait:for:consensus:result"

	BFTConsensusThread         = "thread:bft:consensus"
	BFTByzantineDetectorThread = "thread:bft:byzantine:detector"
	BFTPrepareThread           = "thread:bft:prepare"
	BFTCommitThread            = "thread:bft:commit"
	BFTSendRequestThread       = "thread:bft:send:request"

	CRDTSyncThread = "thread:crdt:sync"

	StreamCacheParallelCleanUpRoutineThread = "thread:stream:cache:parallel:clean:up:routine"
	StreamCacheMessageListenerThread        = "thread:stream:cache:message:listener"
	BuddyNodesMessageProtocolThread         = "thread:buddy:nodes:message:protocol"

	NodeDiscoveryDiscoveryLoopThread = "thread:node:discovery:discovery:loop"
	NodeDiscoverySyncLoopThread      = "thread:node:discovery:sync:loop"

	SeedThread = "thread:seed"

	BlockGRPCServerThread      = "thread:block:grpc:server"
	SubmitRawTransactionThread = "thread:block:submit:raw:transaction"

	ConnectionPoolThread = "thread:connection:pool"
	AddPeersCacheThread  = "thread:add:peers:cache"

	DB_OPsAccountsThread          = "thread:db:ops:accounts"
	DB_OPsImmuclientThread        = "thread:db:ops:immuclient"
	DB_OPsMainDBConnectionsThread = "thread:db:ops:main:db:connections"
	ExplorerBlockOpsThread        = "thread:explorer:block:ops"

	LoggingThread     = "thread:logging"
	LoggingLokiThread = "thread:logging:loki"

	BroadcastForwardThread     = "thread:messaging:broadcast:forward"
	BroadcastVoteTriggerThread = "thread:messaging:broadcast:vote:trigger"
)

// Apps
const (
	MainAM            = "app:main"
	SequencerApp      = "app:sequencer"
	SeedApp           = "app:seed"
	PubsubApp         = "app:pubsub"
	NodeApp           = "app:node"
	MessagingApp      = "app:messaging"
	DIDPropagationApp = "app:did:propagation"
	MetricsApp        = "app:metrics"
	BFTApp            = "app:bft"
	CRDTSyncApp       = "app:crdt:sync"
	BlockApp          = "app:block"
	CLIApp            = "app:cli"
	ConfigApp         = "app:config"
	PubSubMessagesApp = "app:pubsub:messages"
	DB_OPsApp         = "app:db:ops"
	ExplorerApp       = "app:explorer"
	GETHApp           = "app:geth"
	LoggingApp        = "app:logging"
)

var (
	FacadeWG = "waitgroup:facade"

	// Waitgroup for the node manager
	NodeManagerWG                    = "waitgroup:node:manager"
	DIDForwardWG                     = "waitgroup:did:forward"
	MessageBroadcastWG               = "waitgroup:messaging:broadcast"
	VoteBroadcastWG                  = "waitgroup:messaging:vote:broadcast"
	BroadcastBlockWG                 = "waitgroup:messaging:block:broadcast"
	BlockPropagationForwardWG        = "waitgroup:block:propagation:forward"
	BlockPropagationZKBlockWG        = "waitgroup:block:propagation:zkblock"
	BFTWaitGroup                     = "waitgroup:bft"
	SequencerVoteCollectionWaitGroup = "waitgroup:sequencer:vote:collection"
	AddPeersCacheWaitGroup           = "waitgroup:add:peers:cache"
	DB_OPsImmuclientWG               = "waitgroup:db:ops:immuclient"
	ExplorerBlockOpsWaitGroup        = "waitgroup:explorer:block:ops"
	BroadcastForwardWG               = "waitgroup:messaging:broadcast:forward"
	BroadcastVoteTriggerWG           = "waitgroup:messaging:broadcast:vote:trigger"
)
