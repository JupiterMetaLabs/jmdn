package Pubsub

import (
	"context"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// GetLargestString returns the largest string by length from a slice of strings
func getLargestString(strings []string) string {
	if len(strings) == 0 {
		return ""
	}

	largest := strings[0]
	for _, str := range strings[1:] {
		if len(str) > len(largest) {
			largest = str
		}
	}

	return largest
}

// GetlargestMultiaddr returns the largest multiaddr from a slice of multiaddrs
func getLargestMultiaddr(multiaddrs []multiaddr.Multiaddr) multiaddr.Multiaddr {
	if len(multiaddrs) == 0 {
		return nil
	}
	largest := multiaddrs[0]
	for _, multiaddr := range multiaddrs[1:] {
		if multiaddr.String() > largest.String() {
			largest = multiaddr
		}
	}
	return largest
}

// Convert string to multiaddr
func stringToMultiaddr(str string) (multiaddr.Multiaddr, error) {
	return multiaddr.NewMultiaddr(str)
}

// Take multiaddr as input and return the peer.ID from it
func getPeerIDFromMultiaddr(multiaddr multiaddr.Multiaddr) (peer.ID, error) {
	peerInfo, err := peer.AddrInfoFromP2pAddr(multiaddr)
	if err != nil {
		return peer.ID(""), err
	}
	return peerInfo.ID, nil
}

// Take the list of multiaddrs and return the multiaddr which can be pinged
func getPingableMultiaddr(host host.Host, multiaddrs []multiaddr.Multiaddr) (multiaddr.Multiaddr, error) {
	for _, addr := range multiaddrs {
		// Extract peer info from multiaddr
		peerInfo, err := peer.AddrInfoFromP2pAddr(addr)
		if err != nil {
			continue // Skip invalid multiaddrs
		}

		// Set a timeout for the ping operation
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Try to connect to the peer
		if err := host.Connect(ctx, *peerInfo); err != nil {
			continue // Skip if connection fails
		}

		// If connection successful, this multiaddr is pingable
		return addr, nil
	}
	return nil, fmt.Errorf("no pingable multiaddr found")
}