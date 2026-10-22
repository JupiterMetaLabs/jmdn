package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"gossipnode/config"
	"gossipnode/internal/streamread"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

// PeerRequest represents a request for peer information
type PeerRequest struct {
	MaxPeers int    `json:"max_peers"`
	Type     string `json:"type,omitempty"` // Filter by peer type/capability
}

// PeerResponse contains information about peers
type PeerResponse struct {
	Peers []config.PeerInfo `json:"peers"`
}

// RequestPeers asks a seed node for available peers
func RequestPeers(h host.Host, seedAddr string, maxPeers int, peerType string) ([]config.PeerInfo, error) {
	addr, err := peer.AddrInfoFromString(seedAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid seed address: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := h.Connect(ctx, *addr); err != nil {
		return nil, fmt.Errorf("failed to connect to seed node: %v", err)
	}

	stream, err := h.NewStream(ctx, addr.ID, config.PeerDiscoveryProtocol)
	if err != nil {
		return nil, fmt.Errorf("failed to open peer discovery stream: %v", err)
	}
	defer stream.Close()

	request := PeerRequest{
		MaxPeers: maxPeers,
		Type:     peerType,
	}

	encoder := json.NewEncoder(stream)
	if err := encoder.Encode(request); err != nil {
		return nil, fmt.Errorf("failed to send peer request: %v", err)
	}

	var response PeerResponse
	_ = stream.SetReadDeadline(time.Now().Add(streamread.DefaultTimeout))
	decoder := json.NewDecoder(io.LimitReader(stream, streamread.DefaultMaxBytes))
	if err := decoder.Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to read peer response: %v", err)
	}

	return response.Peers, nil
}
