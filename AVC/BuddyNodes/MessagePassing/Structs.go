package MessagePassing

import (
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"time"
	"sync"
)

type BuddyNode struct{
	Host host.Host
	Network network.Network
	PeerID peer.ID
	BuddyNodes Buddies
	Mutex sync.RWMutex
	MetaData MetaData
}

type Message struct{
	Sender peer.ID
	Vote int8
	Timestamp int64
}

type MetaData struct{
	Received uint32
	Sent uint32
	Total uint32
	UpdatedAt time.Time
}

type Buddies struct{
	Buddies_Nodes []peer.ID
}
