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
	MainLM            = "local:main"
	FacadeWG          = "waitgroup:facade"
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

	// Sequencer/Triggers/Triggers.go
	SequencerTriggerLocal   = "local:sequencer:trigger"
	SequencerConsensusLocal = "local:sequencer:consensus"
	SeedLocal               = "local:seed"
	PubsubSubscribeLocal    = "local:pubsub:subscription"
	PubsubPublishLocal      = "local:pubsub:publish"
	PubsubChannelLocal      = "local:pubsub:channel"
	NodeLocal               = "local:node"
)

const (
	SequencerTriggerThread   = "thread:sequencer:trigger"
	SequencerConsensusThread = "thread:sequencer:consensus"
	SeedThread               = "thread:seed:maintenance"
	PubsubSubscriptionThread = "thread:pubsub:subscription"
	PubsubPublishThread      = "thread:pubsub:publish"
	PubsubChannelThread      = "thread:pubsub:channel"
	NodeThread               = "thread:node"
	NodeDiscoveryThread      = "thread:node:discovery"
	NodeStreamThread         = "thread:node:stream"
)

// Apps
const (
	MainAM       = "app:main"
	SequencerApp = "app:sequencer"
	SeedApp      = "app:seed"
	PubsubApp    = "app:pubsub"
	NodeApp      = "app:node"
)

var (
	// Waitgroup for the node manager
	NodeManagerWG = "waitgroup:node:manager"
)
