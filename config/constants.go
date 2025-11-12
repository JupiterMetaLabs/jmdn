package config

import (
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
	MaxMainPeers     = 4  // Production size for buddy node committees
	MaxBackupPeers   = 10 // Backup peers to handle failures of main nodes
	ConsensusTimeout = 20 * time.Second
)

// Time-bounded message handling constants
const (
	MessageListeningWindow = 15 * time.Second // Nodes listen for 15 seconds
	MessageRejectionWindow = 20 * time.Second // Reject messages after 20 seconds
	MessageBufferTime      = 5 * time.Second  // 5-second buffer between windows
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
	FileProtocol              protocol.ID = "/custom/file/1.0.0"
	SeedProtocol              protocol.ID = "/custom/seed/1.0.0"           // Protocol for seed node operations
	PeerDiscoveryProtocol     protocol.ID = "/custom/peer/discovery/1.0.0" // For finding peers
	HeartbeatProtocol         protocol.ID = "/heartbeat/1.0.0"
	RegisterProtocol          protocol.ID = "/seednode/register/1.0.0" // For peer registration
	BroadcastProtocol         protocol.ID = "/broadcast/1.0.0"
	BlockPropagationProtocol  protocol.ID = "/broadcast/block/1.0.0"
	SyncProtocol              protocol.ID = "/p2p/sync/1.0.0"
	BuddyNodesMessageProtocol protocol.ID = "/p2p/buddy/message/1.0.0"
	SubmitMessageProtocol     protocol.ID = "/p2p/submit/message/1.0.0"
	BFTConsensusProtocol      protocol.ID = "/p2p/bft/consensus/1.0.0"
)

const (
	Delimiter               = 0x1E
	PubSub_ConsensusChannel = "pubsub-consensus"
	PubSub_BFTConsensus     = "pubsub-bft-consensus"
	Pubsub_MessageBuffer    = "pubsub-buffer"
	Pubsub_CRDTSync         = "pubsub-crdt-sync"
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
	DIDPropagationProtocol protocol.ID = "/gossipnode/did/1.0.0"
	MaxAccountHops         int         = 7
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
