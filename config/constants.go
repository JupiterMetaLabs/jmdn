package config

import (
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// Add ANSI color constants
const (
	ColorReset  = "\033[0m"
	ColorGreen  = "\033[1;32m"
	ColorCyan   = "\033[1;36m"
	ColorYellow = "\033[1;33m"
	ColorRed    = "\033[1;31m"
)

const PeerFile = "./config/peer.json"
const BLSFile = "./config/bls.json"

const (
	DefaultProfilerPort = "6060" // Default port for the profiler server
)

const (
	MaxMainPeers     = 7 // Production committee size (voting set; keep == consensus.max_validators). n=7 → BFT quorum 2f+1=5 with f=2; n=5 was unsafe (quorum 3, intersection 2q-n=1 < f+1=2).
	MaxBackupPeers   = 5 // Backup peers to handle failures of main nodes
	ConsensusTimeout = 90 * time.Second
)

// MaxBlockMessageBytes is the single source of truth for the maximum size of a
// propagated block message. BOTH transports MUST use it or they silently diverge:
// the direct libp2p stream (messaging.HandleBlockStream, io.LimitReader) and the
// gossip topic (pubsub.WithMaxMessageSize). If the gossip cap is lower than the
// direct cap, blocks between the two sizes publish over direct but silently skip
// gossip (fan-out degrades to catch-up); every node must compile the SAME value
// or peers reject each other's gossip. Sized for large many-tx blocks (8 MiB).
const MaxBlockMessageBytes = 8 * 1024 * 1024

// Time-bounded message handling constants
const (
	MessageListeningWindow = 15 * time.Second // Nodes listen for 15 seconds
	MessageRejectionWindow = 20 * time.Second // Reject messages after 20 seconds
	MessageBufferTime      = 5 * time.Second  // 5-second buffer between windows
)

var (
	DefaultGasPrice          = big.NewInt(1_000_000_000) // 1 gwei
	DefaultPriorityFeePerGas = big.NewInt(1_000_000_000) // 1 gwei
)

var SeedNodeURL string = "" // Default seed node URL, can be updated via SetSeedNodeURL

// SetSeedNodeURL sets the seed node URL for the application
func SetSeedNodeURL(url string) {
	SeedNodeURL = url
}

// GetSeedNodeURL returns the current seed node URL
func GetSeedNodeURL() string {
	return SeedNodeURL
}

// Protocol IDs for message and file sharing
const (
	MessageProtocol           protocol.ID = "/custom/message/1.0.0"
	SeedProtocol              protocol.ID = "/custom/seed/1.0.0"           // Protocol for seed node operations
	PeerDiscoveryProtocol     protocol.ID = "/custom/peer/discovery/1.0.0" // For finding peers
	HeartbeatProtocol         protocol.ID = "/heartbeat/1.0.0"
	RegisterProtocol          protocol.ID = "/seednode/register/1.0.0" // For peer registration
	BroadcastProtocol         protocol.ID = "/broadcast/1.0.0"
	BlockPropagationProtocol  protocol.ID = "/broadcast/block/2.0.0" // v2 protocol: body binding, cache-after-validate, equivocation persistence, linkage fail-closed
	SyncProtocol              protocol.ID = "/p2p/sync/1.0.0"
	BuddyNodesMessageProtocol protocol.ID = "/p2p/buddy/message/2.0.0"  // v2 protocol: vote path (eligibility, 2f+1, chain-id domain, sync-gate)
	SubmitMessageProtocol     protocol.ID = "/p2p/submit/message/2.0.0" // v2 protocol: vote path

	// RevealPushProtocol carries Architecture §4.4's RevealPush: an entropy-
	// committee member pushing its own RANDAO reveal directly to the node
	// proposing the current slot. Added 2026-08-20 with M4 Decision A.
	//
	// Its own protocol ID rather than another case on SubmitMessageProtocol's
	// envelope, deliberately: a reveal is not a vote, it is verified by a
	// completely different mechanism (ed25519 against the peer ID, not BLS
	// against the committee snapshot), and giving it its own ID means a node
	// that does not speak it fails to connect rather than silently mis-routing
	// into the vote path. Same 1:1 direct-stream shape as the vote path, so it
	// is still "T1" in the architecture doc's transport inventory.
	RevealPushProtocol   protocol.ID = "/p2p/randao/reveal/1.0.0"
	BFTConsensusProtocol protocol.ID = "/p2p/bft/consensus/2.0.0" // v2 protocol: signed PREPARE/COMMIT

	// TimeoutCertRejoinProtocol carries the rejoin/catch-up request-response
	// RPC for M0/§7.1c's timeout certificates: a node that just synced (or
	// restarted) asks a peer "what's your latest accepted TimeoutCertificate
	// for height H", instead of waiting to observe one over gossip live.
	// Added 2026-08-24 — closes the scope note in messaging/timeout_gossip.go
	// ("a real network fetch-on-rejoin RPC ... is NOT built here"). One
	// request, one response, both JSON+newline over a direct stream — same
	// shape as RevealPushProtocol above, its own protocol ID for the same
	// reason: a node that doesn't speak it fails the connection rather than
	// silently mis-routing into another handler.
	TimeoutCertRejoinProtocol protocol.ID = "/p2p/timeout-cert/rejoin/1.0.0"
)

const (
	Delimiter               = 0x1E
	PubSub_ConsensusChannel = "pubsub-consensus/2.0.0"     // v2 protocol: chain-id-bound votes + vote sync-gate
	PubSub_BFTConsensus     = "pubsub-bft-consensus/2.0.0" // v2 protocol: signed BFT messages
	// PubSub_L1CommitChannel is a persistent topic for L1 finality broadcasts.
	// It is deliberately separate from PubSub_ConsensusChannel, whose
	// subscriptions are round-scoped (unsubscribed on END_PUBSUB).
	PubSub_L1CommitChannel = "pubsub-l1-commit"
	Pubsub_MessageBuffer   = "pubsub-buffer"
	Pubsub_CRDTSync        = "pubsub-crdt-sync"
	// PubSub_BlockPropagation is a persistent topic for fanning finalized,
	// committee-certified blocks to the whole fleet (not just directly-connected
	// peers). Receivers run the same fail-closed admitZKBlock gate as the direct
	// stream path, so signed-block propagation is preserved. Versioned to 2.0.0 to
	// match the block/vote wire protocols.
	PubSub_BlockPropagation = "pubsub-block-propagation/2.0.0"
)

const (
	// Operation flags
	Type_StartPubSub        = "START_PUBSUB"
	Type_EndPubSub          = "END_PUBSUB"
	Type_Publish            = "PUBLISH"
	Type_ToBeProcessed      = "PROCESS_MESSAGE"
	Type_Subscribe          = "SUBSCRIBE"
	Type_Unsubscribe        = "UNSUBSCRIBE"
	Type_AskForSubscription = "ASK_FOR_SUBSCRIPTION"

	// Verify
	Type_VerifySubscription   = "VERIFY_SUBSCRIPTION"
	Type_SubscriptionResponse = "SUBSCRIPTION_RESPONSE"

	// ACK messages
	Type_ACK_True  = "ACK_TRUE"
	Type_ACK_False = "ACK_FALSE"

	// For Voting Aggregation
	Type_SubmitVote = "SUBMIT_VOTE"
	Type_VoteResult = "VOTE_RESULT"

	// For CRDT Synchronization
	Type_CRDT_SYNC = "CRDT_SYNC"

	Topic_SYNCRequest   = "SYNC_REQUEST"
	Type_BFTRequest     = "BFT_REQUEST" // ADD THIS LINE
	Type_BFTPrepare     = "BFT_PREPARE"
	Type_BFTPrepareVote = "BFT_PREPARE_VOTE"
	Type_BFTCommitVote  = "BFT_COMMIT_VOTE"
	Type_BFTResult      = "BFT_RESULT"

	// For BLS Voting - BLS Voting would be listening on the message transfer protocol
	Type_BLSRequest = "BLS_REQUEST"
	Type_BLSVote    = "BLS_VOTE"

	// L1 finality broadcast — sent by the node that received the commit confirmation
	Type_L1Commit      = "L1_COMMIT"
	Type_L1CommitRange = "L1_COMMIT_RANGE"

	// Finalized-block gossip fan-out (PubSub_BlockPropagation)
	Type_BlockPropagation = "BLOCK_PROPAGATION"
)

// Increase buffer sizes
const (
	BufferSize            = 1024 * 1024 * 8 // 8MB
	CHUNK_SIZE            = 1024 * 1024 * 4 // 4MB chunks
	MAX_CONCURRENT_CHUNKS = 4               // Concurrent chunks
	FlowControlWindowSize = 7168000         // 7MB
)

const (
	MessageExpiryTime = 5 * time.Minute
	MaxHops           = 5
)

const (
	DIDPropagationProtocol      protocol.ID = "/gossipnode/did/2.0.0" // v2 protocol: bumped with the network upgrade
	MaxAccountHops              int         = 7
	ContractPropagationProtocol protocol.ID = "/gossipnode/contract/1.0.0"
	MaxContractHops             int         = 7
	ContractPullProtocol        protocol.ID = "/gossipnode/contract/pull/1.0.0"
)

// Network addresses
var Yggdrasil_Address string
var IP6YGG string

const (
	IP6TCP  = "/ip6/::/tcp/15000"
	IP6QUIC = "/ip6/::/udp/15000/quic-v1"
	IP4TCP  = "/ip4/0.0.0.0/tcp/15000"
	IP4QUIC = "/ip4/0.0.0.0/udp/15000/quic-v1"
)

// SeedNodeConfig defines configuration for seed nodes
const (
	MaxTrackedPeers           = 1000 // Maximum number of peers to track
	PeerTTL                   = 3600 // Time in seconds before a peer is considered stale
	AdvertiseInterval         = 300  // How often seed nodes advertise themselves (seconds)
	HeartbeatFailureThreshold = 3
	HeartbeatRemovalThreshold = 9
)

// Node represents our libp2p service with network integration
type Node struct {
	Host       host.Host
	EnableQUIC bool
	IsSeed     bool          // Whether this node acts as a seed node
	PeerStore  *PeerRegistry // Only used by seed nodes
	DB         *UnifiedDB    // Unified database connection
}

// PeerRegistry tracks active peers in the network
type PeerRegistry struct {
	Peers       map[peer.ID]*PeerInfo
	Mutex       sync.RWMutex
	LastCleanup int64
}

// PeerInfo stores information about a peer
type PeerInfo struct {
	ID           peer.ID
	Addrs        []string
	LastSeen     int64
	Capabilities []string // What the peer can do (e.g., "relay", "storage", etc.)
}

// Database configuration
var (
	// Database location and name
	DBPath = "./DB/gossipnode.db"

	// Table names
	PeersTable     = "peers"           // For seed node peer tracking
	KeyValueTable  = "key_value"       // For general key-value storage
	MerkleTable    = "merkle_hashes"   // For storing Merkle tree hashes
	ConnectedPeers = "connected_peers" // For tracking connected peers
)

// UnifiedDB represents our shared database connection
type UnifiedDB struct {
	Instance interface{} // Could be *sql.DB or another DB interface
	Mutex    sync.RWMutex
}

// AccessTuple is the element type of an access list.
type AccessTuple struct {
	Address     common.Address
	StorageKeys []common.Hash
}

// AccessList is an EIP-2930 access list.
type AccessList []AccessTuple

type PeerConfig struct {
	PeerID     string `json:"peer_id"`
	PrivKeyB64 string `json:"priv_key"` // Base64 encoded private key
}
