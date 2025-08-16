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

// Protocol IDs for message and file sharing
const (
	MessageProtocol       protocol.ID = "/custom/message/1.0.0"
	FileProtocol          protocol.ID = "/custom/file/1.0.0"
	SeedProtocol          protocol.ID = "/custom/seed/1.0.0"           // Protocol for seed node operations
	PeerDiscoveryProtocol protocol.ID = "/custom/peer/discovery/1.0.0" // For finding peers
	HeartbeatProtocol     protocol.ID = "/heartbeat/1.0.0"
	RegisterProtocol      protocol.ID = "/seednode/register/1.0.0" // For peer registration
	BroadcastProtocol     protocol.ID = "/broadcast/1.0.0"
	BlockPropagationProtocol protocol.ID = "/broadcast/block/1.0.0"
	SyncProtocol 		  protocol.ID = "/p2p/sync/1.0.0"
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
    MaxDIDHops             = 7
)

// Network addresses
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

