package node

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"gossipnode/AVC/BuddyNodes/MessagePassing"
	"gossipnode/AVC/BuddyNodes/ServiceLayer"
	"gossipnode/config"
	AVCStruct "gossipnode/config/PubSubMessages"
	"gossipnode/messaging"
	"gossipnode/metrics"
	"gossipnode/transfer"
	"os"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	quic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	tcp "github.com/libp2p/go-libp2p/p2p/transport/tcp"
	ma "github.com/multiformats/go-multiaddr"
)

var localNode config.Node

const peerFile = config.PeerFile

func loadOrCreatePrivateKey() (crypto.PrivKey, peer.ID, error) {
	var colorgreen = config.ColorGreen
	var colorreset = config.ColorReset

	var config config.PeerConfig

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

			fmt.Println(colorgreen+"Loaded existing peer ID:"+colorreset, peerID.String())
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
	fmt.Println("Loading or creating private key...")
	privKey, peerID, err := loadOrCreatePrivateKey()
	if err != nil {
		return nil, fmt.Errorf("failed to load/create Peer ID: %v", err)
	}
	fmt.Println("Private key loaded successfully")

	fmt.Println("Getting libp2p metrics registerer...")
	libp2pRegisterer := metrics.GetLibp2pRegisterer()
	fmt.Println("Creating libp2p host...")

	// Build listen addresses conditionally
	listenAddrs := []string{
		config.IP6QUIC, // QUIC over IPv6
		config.IP6TCP,  // TCP over IPv6
		config.IP4QUIC, // QUIC over IPv4
		config.IP4TCP,  // TCP over IPv4
	}

	// Only add Yggdrasil address if it's valid
	if config.Yggdrasil_Address != "" && config.Yggdrasil_Address != "?" {
		// Dynamically construct the Yggdrasil address
		config.IP6YGG = "/ip6/" + config.Yggdrasil_Address + "/tcp/15000"
		listenAddrs = append(listenAddrs, config.IP6YGG)
		fmt.Printf("Adding Yggdrasil address to listen addresses: %s\n", config.IP6YGG)
	} else {
		fmt.Printf("Skipping Yggdrasil address (not available or invalid): %s\n", config.Yggdrasil_Address)
	}

	h, err := libp2p.New(
		libp2p.Identity(privKey), // Peer ID
		libp2p.ListenAddrStrings(listenAddrs...),
		libp2p.Transport(tcp.NewTCPTransport),         // TCP transport
		libp2p.Transport(quic.NewTransport),           // QUIC transport
		libp2p.NATPortMap(),                           // NAT traversal
		libp2p.ForceReachabilityPublic(),              // Force public reachability
		libp2p.Ping(true),                             // Enable ping
		libp2p.EnableRelayService(),                   // Enable relay
		libp2p.PrometheusRegisterer(libp2pRegisterer), // Enable metrics
	)

	if err != nil {
		return nil, fmt.Errorf("failed to start libp2p: %v", err)
	}
	fmt.Println("libp2p host created successfully")

	fmt.Println("Initializing block propagation...")
	if err := messaging.InitBlockPropagation(h); err != nil {
		return nil, fmt.Errorf("failed to initialize block propagation: %v", err)
	}
	fmt.Println("Block propagation initialized successfully")

	// Verify the host's peer ID matches what we expect
	if peerID.String() != h.ID().String() {
		return nil, fmt.Errorf("peer ID mismatch: expected %s, got %s", peerID, h.ID())
	}

	// Create the node
	localNode = config.Node{
		Host:       h,
		EnableQUIC: true,
	}

	messaging.SetHostInstance(h)
	// Set up stream handlers for messages (TCP) and files (QUIC)
	h.SetStreamHandler(config.MessageProtocol, messaging.HandleMessageStream)
	h.SetStreamHandler(config.FileProtocol, func(s network.Stream) {
		transfer.HandleFileStream(s, "") // Empty string will use default path in HandleFileStream
	})
	h.SetStreamHandler(config.BroadcastProtocol, messaging.HandleBroadcastStream)
	h.SetStreamHandler(config.BlockPropagationProtocol, messaging.HandleBlockStream)
	h.SetStreamHandler(config.BuddyNodesMessageProtocol, func(s network.Stream) {
		MessagePassing.HandleBuddyNodeStream(h, s)
	})

	// Set up SubmitMessageProtocol handler for all nodes (so they can receive subscription requests)
	h.SetStreamHandler(config.SubmitMessageProtocol, func(s network.Stream) {
		// Initialize ForListner for this node so it can handle subscription requests
		// This is needed because ListenerHandler.handleAskForSubscription requires ForListner to be set
		if AVCStruct.NewGlobalVariables().Get_ForListner() == nil {
			fmt.Printf("=== Initializing ForListner for regular node: %s ===\n", h.ID())
			// Create a basic BuddyNode for this regular node
			defaultBuddies := AVCStruct.NewBuddiesBuilder(nil)
			// Create StreamCache for this node
			streamCache := &AVCStruct.StreamCache{
				Streams:                make(map[peer.ID]*AVCStruct.StreamEntry),
				AccessOrder:            []peer.ID{},
				MaxStreams:             100,
				TTL:                    5 * time.Minute,
				Host:                   h,
				ParallelCleanUpRoutine: false,
			}

			// Initialize CRDT Layer
			CRDTLayer := ServiceLayer.GetServiceController()

			basicBuddyNode := &AVCStruct.BuddyNode{
				PeerID:      h.ID(),
				Host:        h,
				PubSub:      nil, // Will be set when needed
				BuddyNodes:  *defaultBuddies,
				StreamCache: streamCache, // Initialize with proper StreamCache
				CRDTLayer:   CRDTLayer,   // Initialize CRDT Layer
				MetaData: AVCStruct.MetaData{
					Received:  0,
					Sent:      0,
					Total:     0,
					UpdatedAt: time.Now().UTC(),
				},
			}
			AVCStruct.NewGlobalVariables().Set_ForListner(basicBuddyNode)
			fmt.Printf("=== ForListner initialized successfully ===\n")
		} else {
			fmt.Printf("=== ForListner already initialized for node: %s ===\n", h.ID())
		}

		// Create a clear listener handler for handling subscription requests, votes, and responses
		listenerHandler := MessagePassing.NewListenerHandler(nil)
		go func(stream network.Stream) {
			defer stream.Close()
			listenerHandler.HandleSubmitMessageStream(stream)
		}(s)
	})

	go StartDiscovery(h)

	return &localNode, nil
}

// SendMessage sends a message to a peer (uses TCP)
func SendMessage(n *config.Node, target string, message string) error {
	maddr, peerInfo, isConnected, err := getPeerInfo(target, n.Host)
	if err != nil {
		return err
	}

	fmt.Println("Connected to peer:", isConnected)

	// Connect to the peer
	if err := n.Host.Connect(context.Background(), *peerInfo); err != nil {
		return fmt.Errorf("connection failed: %v", err)
	}

	return messaging.SendMessage(n, maddr.String(), message)
}

// SendFile sends a file to a peer (uses QUIC)
func SendFile(n *config.Node, target string, filepath string, destination string) error {
	_, peerInfo, isConnected, err := getPeerInfo(target, n.Host)
	if err != nil {
		return err
	}

	fmt.Println("Connected to peer:", isConnected)
	// Connect to the peer
	if err := n.Host.Connect(context.Background(), *peerInfo); err != nil {
		return fmt.Errorf("connection failed: %v", err)
	}

	return transfer.SendFile(n.Host, peerInfo.ID, filepath, destination)
}

// getPeerInfo extracts peer information from a multiaddress
func getPeerInfo(target string, host host.Host) (ma.Multiaddr, *peer.AddrInfo, bool, error) {
	maddr, err := ma.NewMultiaddr(target)
	if err != nil {
		return nil, nil, false, fmt.Errorf("invalid multiaddr: %v", err)
	}

	peerInfo, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil {
		return nil, nil, false, fmt.Errorf("invalid peer address: %v", err)
	}

	// Check if the peer is directly connected
	isConnected := host.Network().Connectedness(peerInfo.ID) == network.Connected

	return maddr, peerInfo, isConnected, nil
}

// BroadcastMessage broadcasts a message to all connected peers
func BroadcastMessage(n *config.Node, message string) error {
	return messaging.BroadcastMessage(n.Host, message)
}

// GetPeerID returns the peer ID string of the local node
func GetPeerID() string {
	if localNode.Host == nil {
		return ""
	}
	return localNode.Host.ID().String()
}

func GetPeerIDFromJSON() string {
	// Check if file exists
	if _, err := os.Stat(peerFile); err != nil {
		fmt.Println("Failed to stat peer.json:", err)
		return ""
	}

	// Open the file
	file, err := os.Open(peerFile)
	if err != nil {
		fmt.Println("Failed to open peer.json:", err)
		return ""
	}
	defer file.Close()

	// Decode JSON
	var config config.PeerConfig
	if err := json.NewDecoder(file).Decode(&config); err != nil {
		fmt.Println("Failed to decode peer.json:", err)
		return ""
	}

	fmt.Println("Peer ID from peer.json:", config.PeerID)
	return config.PeerID
}

func GetHost() host.Host {
	return localNode.Host
}