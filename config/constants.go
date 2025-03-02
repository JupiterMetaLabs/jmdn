package config

import (
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/host"

)

// Protocol IDs for message and file sharing
const (
	MessageProtocol protocol.ID = "/custom/message/1.0.0"
	FileProtocol    protocol.ID = "/custom/file/1.0.0"
)

// Increase buffer sizes
const (
    BufferSize            = 1024 * 1024 * 8 // 8MB
    CHUNK_SIZE            = 1024 * 1024 * 4 // 4MB chunks
    MAX_CONCURRENT_CHUNKS = 4               // Concurrent chunks
	FlowControlWindowSize   = 7168000 // 7MB
)


const (
	IP6TCP  = "/ip6/::/tcp/15000"
	IP6QUIC = "/ip6/::/udp/15000/quic-v1"
	IP4TCP  = "/ip4/0.0.0.0/tcp/15000"
	IP4QUIC = "/ip4/0.0.0.0/udp/15000/quic"
)

// Node represents our libp2p service with network integration
type Node struct {
	Host       host.Host
	EnableQUIC bool
}