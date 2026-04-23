package utils

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"sort"

	log "gossipnode/logging"
	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// ConsistantHashing picks one peer deterministically from the given Peers map using SHA-256 hashing
func ConsistantHashing(Peers map[int]multiaddr.Multiaddr, peerID *peer.AddrInfo) multiaddr.Multiaddr {
	if len(Peers) == 0 {
		return nil
	}

	hasher := sha256.New()
	hasher.Write([]byte(peerID.String())) // correct method call
	hashBytes := hasher.Sum(nil)

	// Convert first 8 bytes to uint64
	hashInt := binary.BigEndian.Uint64(hashBytes[:8])

	// Get sorted keys from Peers map to ensure deterministic iteration
	keys := make([]int, 0, len(Peers))
	for k := range Peers {
		keys = append(keys, k)
	}
	sort.Ints(keys)

	// Choose one peer based on hash modulo number of peers
	index := int(hashInt % uint64(len(keys)))
	selectedKey := keys[index]

	// Debugging
	ctx := context.Background()
	logInstance, err := log.NewAsyncLogger().Get().NamedLogger(log.Config, "")
	if err == nil && logInstance != nil {
		logInstance.GetNamedLogger().Debug(ctx, "Selected peer", ion.Int("key", selectedKey))
	}
	return Peers[selectedKey]
}
