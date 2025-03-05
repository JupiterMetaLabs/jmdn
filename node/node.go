package node

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"gossipnode/config"
	"gossipnode/messaging"
	"gossipnode/transfer"
	"os"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	quic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	tcp "github.com/libp2p/go-libp2p/p2p/transport/tcp"
	ma "github.com/multiformats/go-multiaddr"
)

var localNode config.Node

type PeerConfig struct {
    PeerID    string `json:"peer_id"`
    PrivKeyB64 string `json:"priv_key"` // Base64 encoded private key
}

const peerFile = "./config/peer.json"

func loadOrCreatePrivateKey() (crypto.PrivKey, peer.ID, error) {
    var config PeerConfig

    // Check if peer.json exists
    if _, err := os.Stat(peerFile); err == nil {
        file, err := os.Open(peerFile)
        if err != nil {
            return nil, "", fmt.Errorf("failed to open peer.json: %v", err)
        }
        defer file.Close()

        if err := json.NewDecoder(file).Decode(&config); err != nil {
            return nil, "", fmt.Errorf("failed to decode peer.json: %v", err)
        }

        if config.PrivKeyB64 != "" {
            // Decode the private key from base64
            privKeyBytes, err := base64.StdEncoding.DecodeString(config.PrivKeyB64)
            if err != nil {
                return nil, "", fmt.Errorf("failed to decode private key: %v", err)
            }
            
            // Unmarshal the private key
            privKey, err := crypto.UnmarshalPrivateKey(privKeyBytes)
            if err != nil {
                return nil, "", fmt.Errorf("failed to unmarshal private key: %v", err)
            }
            
            // Get the peer ID from the private key
            peerID, err := peer.IDFromPrivateKey(privKey)
            if err != nil {
                return nil, "", fmt.Errorf("failed to derive peer ID: %v", err)
            }
            
            fmt.Printf("Loaded existing peer ID: %s\n", peerID.String())
            return privKey, peerID, nil
        }
    }

    // Generate new key pair
    fmt.Println("Generating new peer identity...")
    privKey, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 0)
    if err != nil {
        return nil, "", fmt.Errorf("failed to generate key pair: %v", err)
    }
    
    // Get peer ID from the private key
    peerID, err := peer.IDFromPrivateKey(privKey)
    if err != nil {
        return nil, "", fmt.Errorf("failed to extract peer ID: %v", err)
    }

    // Marshal the private key to bytes
    privKeyBytes, err := crypto.MarshalPrivateKey(privKey)
    if err != nil {
        return nil, "", fmt.Errorf("failed to marshal private key: %v", err)
    }
    
    // Encode the private key as base64
    privKeyB64 := base64.StdEncoding.EncodeToString(privKeyBytes)
    
    // Save private key and peer ID to peer.json
    config.PeerID = peerID.String()
    config.PrivKeyB64 = privKeyB64
    
    file, err := os.Create(peerFile)
    if err != nil {
        return nil, "", fmt.Errorf("failed to create peer.json: %v", err)
    }
    defer file.Close()

    if err := json.NewEncoder(file).Encode(config); err != nil {
        return nil, "", fmt.Errorf("failed to write peer.json: %v", err)
    }

    fmt.Printf("Generated new peer ID: %s\n", peerID.String())
    return privKey, peerID, nil
}

// NewNode creates and starts a libp2p node
func NewNode() (*config.Node, error) {
	// Load or create Peer ID
    privKey, peerID, err := loadOrCreatePrivateKey()
	if err != nil {
		return nil, fmt.Errorf("failed to load/create Peer ID: %v", err)
	}

	h, err := libp2p.New(
		libp2p.Identity(privKey), // Peer ID
		libp2p.ListenAddrStrings(
			config.IP6QUIC, // QUIC over IPv6
			config.IP6TCP,  // TCP over IPv6
			config.IP4QUIC, // QUIC over IPv4
			config.IP4TCP,  // TCP over IPv4
		),
		libp2p.Transport(tcp.NewTCPTransport),      // TCP transport
		libp2p.Transport(quic.NewTransport),        // QUIC transport
		libp2p.NATPortMap(),                        // NAT traversal
		libp2p.ForceReachabilityPublic(),           // Force public reachability
	)

	if err != nil {
		return nil, fmt.Errorf("failed to start libp2p: %v", err)
	}

	// Verify the host's peer ID matches what we expect
	if peerID.String() != h.ID().String() {
		return nil, fmt.Errorf("peer ID mismatch: expected %s, got %s", peerID, h.ID())
	}

	// Create the node
	localNode = config.Node{
		Host:       h,
		EnableQUIC: true,
	}

	// Set up stream handlers for messages (TCP) and files (QUIC)
	h.SetStreamHandler(config.MessageProtocol, messaging.HandleMessageStream)
	h.SetStreamHandler(config.FileProtocol, transfer.HandleFileStream)

	// Start peer discovery using mDNS (optional)
	go StartDiscovery(h)

	return &localNode, nil
}

// SendMessage sends a message to a peer (uses TCP)
func SendMessage(n *config.Node, target string, message string) error {
	peerInfo, err := getPeerInfo(target)
	if err != nil {
		return err
	}

	// Connect to the peer
	if err := n.Host.Connect(context.Background(), *peerInfo); err != nil {
		return fmt.Errorf("connection failed: %v", err)
	}

	return messaging.SendMessage(n , peerInfo.ID.String(), message)
}

// SendFile sends a file to a peer (uses QUIC)
func SendFile(n *config.Node,target string, filepath string) error {
	peerInfo, err := getPeerInfo(target)
	if err != nil {
		return err
	}

	// Connect to the peer
	if err := n.Host.Connect(context.Background(), *peerInfo); err != nil {
		return fmt.Errorf("connection failed: %v", err)
	}

	return transfer.SendFile(n.Host, peerInfo.ID, filepath)
}

// getPeerInfo extracts peer information from a multiaddress
func getPeerInfo(target string) (*peer.AddrInfo, error) {
	maddr, err := ma.NewMultiaddr(target)
	if err != nil {
		return nil, fmt.Errorf("invalid multiaddr: %v", err)
	}

	peerInfo, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil {
		return nil, fmt.Errorf("invalid peer address: %v", err)
	}

	return peerInfo, nil
}