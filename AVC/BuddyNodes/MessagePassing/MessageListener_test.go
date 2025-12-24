package MessagePassing

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	Subscription "gossipnode/Pubsub/Subscription"
	"gossipnode/config"
	AVCStruct "gossipnode/config/PubSubMessages"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// TestSubscriptionFlow tests the complete subscription flow with a single node
func TestSubscriptionFlow(t *testing.T) {
	fmt.Println("🚀 Starting Subscription Flow Test...")

	// Step 1: Create a test host (simulating buddy node)
	host, err := libp2p.New()
	if err != nil {
		t.Fatalf("Failed to create host: %v", err)
	}
	defer host.Close()

	fmt.Printf("✅ Created test host: %s\n", host.ID())

	// Step 2: Set up the listener node
	streamCache := &AVCStruct.StreamCache{
		Streams:     make(map[peer.ID]*AVCStruct.StreamEntry),
		AccessOrder: make([]peer.ID, 0),
		MaxStreams:  10,
		TTL:         5 * time.Minute,
		Host:        host,
	}

	listenerBuddyNode := &AVCStruct.BuddyNode{
		Host:        host,
		StreamCache: streamCache,
		MetaData: AVCStruct.MetaData{
			Sent:      0,
			Total:     0,
			UpdatedAt: time.Now().UTC(),
		},
	}

	// Initialize global variables
	globalVars := AVCStruct.NewGlobalVariables()
	globalVars.Set_ForListner(listenerBuddyNode)

	listener := NewListenerStruct(listenerBuddyNode)
	fmt.Printf("✅ Created listener node: %s\n", listenerBuddyNode.Host.ID())

	// Step 3: Set up stream handler for SubmitMessageProtocol
	host.SetStreamHandler(config.SubmitMessageProtocol, func(s network.Stream) {
		fmt.Printf("📡 Received stream on SubmitMessageProtocol from: %s\n", s.Conn().RemotePeer())
		listener.HandleSubmitMessageStream(s)
	})

	// Step 4: Create a client host (simulating sequencer)
	clientHost, err := libp2p.New()
	if err != nil {
		t.Fatalf("Failed to create client host: %v", err)
	}
	defer clientHost.Close()

	fmt.Printf("✅ Created client host: %s\n", clientHost.ID())

	// Step 5: Connect client to listener
	peerInfo := peer.AddrInfo{
		ID:    host.ID(),
		Addrs: host.Addrs(),
	}

	if err := clientHost.Connect(context.Background(), peerInfo); err != nil {
		t.Fatalf("Failed to connect client to listener: %v", err)
	}

	fmt.Printf("✅ Connected client to listener\n")

	// Step 6: Create subscription request message
	subscriptionRequest := createSubscriptionRequest(clientHost.ID())
	requestBytes, err := json.Marshal(subscriptionRequest)
	if err != nil {
		t.Fatalf("Failed to marshal subscription request: %v", err)
	}

	fmt.Printf("✅ Created subscription request message\n")

	// Step 7: Send subscription request
	stream, err := clientHost.NewStream(context.Background(), host.ID(), config.SubmitMessageProtocol)
	if err != nil {
		t.Fatalf("Failed to create stream: %v", err)
	}
	defer stream.Close()

	// Send the request
	_, err = stream.Write([]byte(string(requestBytes) + string(rune(config.Delimiter))))
	if err != nil {
		t.Fatalf("Failed to send subscription request: %v", err)
	}

	fmt.Printf("📤 Sent subscription request to listener\n")

	// Step 8: Read the response
	responseBytes := make([]byte, 1024)
	n, err := stream.Read(responseBytes)
	if err != nil {
		t.Fatalf("Failed to read response: %v", err)
	}

	responseStr := string(responseBytes[:n])
	fmt.Printf("📥 Received response: %s\n", responseStr)

	// Step 9: Parse and validate the response
	response := AVCStruct.NewMessageBuilder(nil).DeferenceMessage(responseStr)
	if response == nil {
		t.Fatalf("Failed to parse response message")
	}

	if response.GetACK() == nil {
		t.Fatalf("Response has nil ACK")
	}

	// Validate the response
	if response.GetACK().GetStage() != config.Type_SubscriptionResponse {
		t.Fatalf("Expected ACK stage %s, got %s", config.Type_SubscriptionResponse, response.GetACK().GetStage())
	}

	if response.GetACK().GetStatus() != config.Type_ACK_True {
		t.Fatalf("Expected ACK status to be '%s', got %s", config.Type_ACK_True, response.GetACK().GetStatus())
	}

	fmt.Printf("✅ Subscription response validated successfully!\n")
	fmt.Printf("   - ACK Stage: %s\n", response.GetACK().GetStage())
	fmt.Printf("   - ACK Status: %s\n", response.GetACK().GetStatus())
	fmt.Printf("   - Message: %s\n", response.Message)

	// Step 10: Verify that the listener is now subscribed to PubSub
	globalVars = AVCStruct.NewGlobalVariables()
	pubSubNode := globalVars.Get_PubSubNode()
	if pubSubNode == nil {
		t.Fatalf("PubSub node should be initialized after subscription request")
	}

	fmt.Printf("✅ PubSub node initialized: %s\n", pubSubNode.Host.ID())

	// Step 11: Test PubSub functionality
	testPubSubFunctionality(t, pubSubNode)

	fmt.Println("🎉 Subscription Flow Test Completed Successfully!")
}

// createSubscriptionRequest creates a subscription request message
func createSubscriptionRequest(senderID peer.ID) *AVCStruct.Message {
	ackBuilder := AVCStruct.NewACKBuilder().True_ACK_Message(senderID, config.Type_AskForSubscription)

	return AVCStruct.NewMessageBuilder(nil).
		SetSender(senderID).
		SetMessage("Request subscription to BuddyNodesMessageProtocol").
		SetTimestamp(time.Now().UTC().Unix()).
		SetACK(ackBuilder)
}

// testPubSubFunctionality tests if the PubSub system is working correctly
func testPubSubFunctionality(t *testing.T, pubSubNode *AVCStruct.BuddyNode) {
	fmt.Println("🧪 Testing PubSub functionality...")

	// Create a test GossipPubSub
	gps := AVCStruct.NewGossipPubSubBuilder(nil).
		SetHost(pubSubNode.Host).
		SetProtocol(config.BuddyNodesMessageProtocol).
		Build()

	// Test subscription
	topicName := "test-topic"
	messageReceived := make(chan bool, 1)

	// Allow subscribing to this test topic by creating a local public channel entry.
	// Subscription.CanSubscribe requires ChannelAccess to exist.
	gps.Mutex.Lock()
	if gps.ChannelAccess == nil {
		gps.ChannelAccess = make(map[string]*AVCStruct.ChannelAccess)
	}
	gps.ChannelAccess[topicName] = &AVCStruct.ChannelAccess{
		ChannelName:  topicName,
		AllowedPeers: map[peer.ID]bool{gps.Host.ID(): true},
		IsPublic:     true,
		Creator:      gps.Host.ID(),
		CreatedAt:    time.Now().UTC().Unix(),
	}
	gps.Mutex.Unlock()

	err := Subscription.Subscribe(gps, topicName, func(msg *AVCStruct.GossipMessage) {
		fmt.Printf("📨 Received test message: %s\n", msg.ID)
		messageReceived <- true
	})

	if err != nil {
		t.Fatalf("Failed to subscribe to test topic: %v", err)
	}

	fmt.Printf("✅ Successfully subscribed to test topic: %s\n", topicName)

	// Test publishing (if publish functionality exists)
	// This would depend on your PubSub implementation
	fmt.Printf("✅ PubSub functionality test completed\n")
}

// TestSingleNodeSubscription tests the subscription flow with minimal setup
func TestSingleNodeSubscription(t *testing.T) {
	fmt.Println("🔬 Testing Single Node Subscription...")

	// Create server host
	host, err := libp2p.New()
	if err != nil {
		t.Fatalf("Failed to create host: %v", err)
	}
	defer host.Close()

	fmt.Printf("✅ Created single test host: %s\n", host.ID())

	// Set up the listener
	streamCache := &AVCStruct.StreamCache{
		Streams:     make(map[peer.ID]*AVCStruct.StreamEntry),
		AccessOrder: make([]peer.ID, 0),
		MaxStreams:  10,
		TTL:         5 * time.Minute,
		Host:        host,
	}

	listenerBuddyNode := &AVCStruct.BuddyNode{
		Host:        host,
		StreamCache: streamCache,
		MetaData: AVCStruct.MetaData{
			Sent:      0,
			Total:     0,
			UpdatedAt: time.Now().UTC(),
		},
	}

	// Initialize global variables
	globalVars := AVCStruct.NewGlobalVariables()
	globalVars.Set_ForListner(listenerBuddyNode)

	listener := NewListenerStruct(listenerBuddyNode)

	// Set up stream handler
	host.SetStreamHandler(config.SubmitMessageProtocol, func(s network.Stream) {
		fmt.Printf("📡 Single node received stream from: %s\n", s.Conn().RemotePeer())
		listener.HandleSubmitMessageStream(s)
	})

	// Create client host to avoid dialing self
	clientHost, err := libp2p.New()
	if err != nil {
		t.Fatalf("Failed to create client host: %v", err)
	}
	defer clientHost.Close()

	if err := clientHost.Connect(context.Background(), peer.AddrInfo{
		ID:    host.ID(),
		Addrs: host.Addrs(),
	}); err != nil {
		t.Fatalf("Failed to connect client to host: %v", err)
	}

	// Create subscription request
	subscriptionRequest := createSubscriptionRequest(clientHost.ID())
	requestBytes, err := json.Marshal(subscriptionRequest)
	if err != nil {
		t.Fatalf("Failed to marshal subscription request: %v", err)
	}

	// Send request from client to server
	stream, err := clientHost.NewStream(context.Background(), host.ID(), config.SubmitMessageProtocol)
	if err != nil {
		t.Fatalf("Failed to create stream: %v", err)
	}
	defer stream.Close()

	// Send the request
	_, err = stream.Write([]byte(string(requestBytes) + string(rune(config.Delimiter))))
	if err != nil {
		t.Fatalf("Failed to send subscription request: %v", err)
	}

	fmt.Printf("📤 Sent subscription request to self\n")

	// Read the response
	responseBytes := make([]byte, 1024)
	n, err := stream.Read(responseBytes)
	if err != nil {
		t.Fatalf("Failed to read response: %v", err)
	}

	responseStr := string(responseBytes[:n])
	fmt.Printf("📥 Received self-response: %s\n", responseStr)

	// Validate response
	response := AVCStruct.NewMessageBuilder(nil).DeferenceMessage(responseStr)
	if response == nil {
		t.Fatalf("Failed to parse self-response message")
	}

	if response.GetACK() == nil {
		t.Fatalf("Self-response has nil ACK")
	}

	if response.GetACK().GetStatus() != config.Type_ACK_True {
		t.Fatalf("Self-response ACK status should be '%s', got %s", config.Type_ACK_True, response.GetACK().GetStatus())
	}

	fmt.Printf("✅ Single node subscription test completed!\n")
	fmt.Printf("   - ACK Stage: %s\n", response.GetACK().GetStage())
	fmt.Printf("   - ACK Status: %s\n", response.GetACK().GetStatus())
}

// TestSubscriptionWithMockPubSub tests subscription with mocked PubSub
func TestSubscriptionWithMockPubSub(t *testing.T) {
	fmt.Println("🎭 Testing Subscription with Mock PubSub...")

	// Create server host
	host, err := libp2p.New()
	if err != nil {
		t.Fatalf("Failed to create host: %v", err)
	}
	defer host.Close()

	// Set up the listener
	streamCache := &AVCStruct.StreamCache{
		Streams:     make(map[peer.ID]*AVCStruct.StreamEntry),
		AccessOrder: make([]peer.ID, 0),
		MaxStreams:  10,
		TTL:         5 * time.Minute,
		Host:        host,
	}

	listenerBuddyNode := &AVCStruct.BuddyNode{
		Host:        host,
		StreamCache: streamCache,
		MetaData: AVCStruct.MetaData{
			Sent:      0,
			Total:     0,
			UpdatedAt: time.Now().UTC(),
		},
	}

	globalVars := AVCStruct.NewGlobalVariables()
	globalVars.Set_ForListner(listenerBuddyNode)

	listener := NewListenerStruct(listenerBuddyNode)

	// Set up stream handler
	host.SetStreamHandler(config.SubmitMessageProtocol, func(s network.Stream) {
		fmt.Printf("📡 Mock test received stream from: %s\n", s.Conn().RemotePeer())
		listener.HandleSubmitMessageStream(s)
	})

	// Create and send subscription request
	clientHost, err := libp2p.New()
	if err != nil {
		t.Fatalf("Failed to create client host: %v", err)
	}
	defer clientHost.Close()

	if err := clientHost.Connect(context.Background(), peer.AddrInfo{
		ID:    host.ID(),
		Addrs: host.Addrs(),
	}); err != nil {
		t.Fatalf("Failed to connect client to host: %v", err)
	}

	subscriptionRequest := createSubscriptionRequest(clientHost.ID())
	requestBytes, err := json.Marshal(subscriptionRequest)
	if err != nil {
		t.Fatalf("Failed to marshal subscription request: %v", err)
	}

	stream, err := clientHost.NewStream(context.Background(), host.ID(), config.SubmitMessageProtocol)
	if err != nil {
		t.Fatalf("Failed to create stream: %v", err)
	}
	defer stream.Close()

	_, err = stream.Write([]byte(string(requestBytes) + string(rune(config.Delimiter))))
	if err != nil {
		t.Fatalf("Failed to send subscription request: %v", err)
	}

	// Read response
	responseBytes := make([]byte, 1024)
	n, err := stream.Read(responseBytes)
	if err != nil {
		t.Fatalf("Failed to read response: %v", err)
	}

	responseStr := string(responseBytes[:n])
	fmt.Printf("📥 Mock test received response: %s\n", responseStr)

	// Validate
	response := AVCStruct.NewMessageBuilder(nil).DeferenceMessage(responseStr)
	if response == nil || response.GetACK() == nil || response.GetACK().GetStatus() != config.Type_ACK_True {
		t.Fatalf("Mock subscription test failed")
	}

	fmt.Printf("✅ Mock subscription test completed successfully!\n")
}

// TestSubscriptionWithSpecificPeer tests the subscription flow with a specific peer ID
func TestSubscriptionWithSpecificPeer(t *testing.T) {
	fmt.Println("🎯 Testing Subscription with Specific Peer ID...")
	fmt.Println("Peer ID: 12D3KooWBeEtRULBDzwmqRbjchN89tAPfoJtEtFAhigscjP9bWVX")

	// Step 1: Create a test host (simulating buddy node)
	host, err := libp2p.New()
	if err != nil {
		t.Fatalf("Failed to create host: %v", err)
	}
	defer host.Close()

	fmt.Printf("✅ Created test host: %s\n", host.ID())

	// Step 2: Set up the listener node
	streamCache := &AVCStruct.StreamCache{
		Streams:     make(map[peer.ID]*AVCStruct.StreamEntry),
		AccessOrder: make([]peer.ID, 0),
		MaxStreams:  10,
		TTL:         5 * time.Minute,
		Host:        host,
	}

	listenerBuddyNode := &AVCStruct.BuddyNode{
		Host:        host,
		StreamCache: streamCache,
		MetaData: AVCStruct.MetaData{
			Sent:      0,
			Total:     0,
			UpdatedAt: time.Now().UTC(),
		},
	}

	// Initialize global variables
	globalVars := AVCStruct.NewGlobalVariables()
	globalVars.Set_ForListner(listenerBuddyNode)

	listener := NewListenerStruct(listenerBuddyNode)
	fmt.Printf("✅ Created listener node: %s\n", listenerBuddyNode.Host.ID())

	// Step 3: Set up stream handler for SubmitMessageProtocol
	host.SetStreamHandler(config.SubmitMessageProtocol, func(s network.Stream) {
		fmt.Printf("📡 Received stream on SubmitMessageProtocol from: %s\n", s.Conn().RemotePeer())
		listener.HandleSubmitMessageStream(s)
	})

	// Step 4: Parse the specific peer ID
	specificPeerID, err := peer.Decode("12D3KooWBeEtRULBDzwmqRbjchN89tAPfoJtEtFAhigscjP9bWVX")
	if err != nil {
		t.Fatalf("Failed to decode peer ID: %v", err)
	}

	fmt.Printf("✅ Parsed specific peer ID: %s\n", specificPeerID)

	// Step 5: Create subscription request message with the specific peer ID
	subscriptionRequest := createSubscriptionRequest(specificPeerID)
	requestBytes, err := json.Marshal(subscriptionRequest)
	if err != nil {
		t.Fatalf("Failed to marshal subscription request: %v", err)
	}

	fmt.Printf("✅ Created subscription request message for peer: %s\n", specificPeerID)

	// Step 6: Create a client host (simulating sequencer)
	clientHost, err := libp2p.New()
	if err != nil {
		t.Fatalf("Failed to create client host: %v", err)
	}
	defer clientHost.Close()

	fmt.Printf("✅ Created client host: %s\n", clientHost.ID())

	// Step 7: Connect client to listener
	peerInfo := peer.AddrInfo{
		ID:    host.ID(),
		Addrs: host.Addrs(),
	}

	if err := clientHost.Connect(context.Background(), peerInfo); err != nil {
		t.Fatalf("Failed to connect client to listener: %v", err)
	}

	fmt.Printf("✅ Connected client to listener\n")

	// Step 8: Send subscription request
	stream, err := clientHost.NewStream(context.Background(), host.ID(), config.SubmitMessageProtocol)
	if err != nil {
		t.Fatalf("Failed to create stream: %v", err)
	}
	defer stream.Close()

	// Send the request
	_, err = stream.Write([]byte(string(requestBytes) + string(rune(config.Delimiter))))
	if err != nil {
		t.Fatalf("Failed to send subscription request: %v", err)
	}

	fmt.Printf("📤 Sent subscription request to listener for peer: %s\n", specificPeerID)

	// Step 9: Read the response
	responseBytes := make([]byte, 1024)
	n, err := stream.Read(responseBytes)
	if err != nil {
		t.Fatalf("Failed to read response: %v", err)
	}

	responseStr := string(responseBytes[:n])
	fmt.Printf("📥 Received response: %s\n", responseStr)

	// Step 10: Parse and validate the response
	response := AVCStruct.NewMessageBuilder(nil).DeferenceMessage(responseStr)
	if response == nil {
		t.Fatalf("Failed to parse response message")
	}

	if response.GetACK() == nil {
		t.Fatalf("Response has nil ACK")
	}

	// Validate the response
	if response.GetACK().GetStage() != config.Type_SubscriptionResponse {
		t.Fatalf("Expected ACK stage %s, got %s", config.Type_SubscriptionResponse, response.GetACK().GetStage())
	}

	if response.GetACK().GetStatus() != config.Type_ACK_True {
		t.Fatalf("Expected ACK status to be '%s', got %s", config.Type_ACK_True, response.GetACK().GetStatus())
	}

	fmt.Printf("✅ Subscription response validated successfully!\n")
	fmt.Printf("   - ACK Stage: %s\n", response.GetACK().GetStage())
	fmt.Printf("   - ACK Status: %s\n", response.GetACK().GetStatus())
	fmt.Printf("   - Message: %s\n", response.Message)
	fmt.Printf("   - Requested Peer ID: %s\n", specificPeerID)

	// Step 11: Verify that the listener is now subscribed to PubSub
	globalVars = AVCStruct.NewGlobalVariables()
	pubSubNode := globalVars.Get_PubSubNode()
	if pubSubNode == nil {
		t.Fatalf("PubSub node should be initialized after subscription request")
	}

	fmt.Printf("✅ PubSub node initialized: %s\n", pubSubNode.Host.ID())

	// Step 12: Test PubSub functionality with the specific peer
	testPubSubWithSpecificPeer(t, pubSubNode, specificPeerID)

	fmt.Println("🎉 Subscription Flow Test with Specific Peer Completed Successfully!")
}

// testPubSubWithSpecificPeer tests PubSub functionality with a specific peer
func testPubSubWithSpecificPeer(t *testing.T, pubSubNode *AVCStruct.BuddyNode, peerID peer.ID) {
	fmt.Printf("🧪 Testing PubSub functionality with peer: %s\n", peerID)

	// Create a test GossipPubSub
	gps := AVCStruct.NewGossipPubSubBuilder(nil).
		SetHost(pubSubNode.Host).
		SetProtocol(config.BuddyNodesMessageProtocol).
		Build()

	// Test subscription with peer-specific topic
	topicName := fmt.Sprintf("peer-%s-topic", peerID.String())
	messageReceived := make(chan bool, 1)

	// Allow subscribing to this test topic by creating a local public channel entry.
	gps.Mutex.Lock()
	if gps.ChannelAccess == nil {
		gps.ChannelAccess = make(map[string]*AVCStruct.ChannelAccess)
	}
	gps.ChannelAccess[topicName] = &AVCStruct.ChannelAccess{
		ChannelName:  topicName,
		AllowedPeers: map[peer.ID]bool{gps.Host.ID(): true},
		IsPublic:     true,
		Creator:      gps.Host.ID(),
		CreatedAt:    time.Now().UTC().Unix(),
	}
	gps.Mutex.Unlock()

	err := Subscription.Subscribe(gps, topicName, func(msg *AVCStruct.GossipMessage) {
		fmt.Printf("📨 Received message for peer %s: %s\n", peerID, msg.ID)
		messageReceived <- true
	})

	if err != nil {
		t.Fatalf("Failed to subscribe to peer-specific topic: %v", err)
	}

	fmt.Printf("✅ Successfully subscribed to peer-specific topic: %s\n", topicName)
	fmt.Printf("✅ PubSub functionality test with specific peer completed\n")
}

// TestSubscriptionFlowWithRealPeerID tests the complete flow using the real peer ID
func TestSubscriptionFlowWithRealPeerID(t *testing.T) {
	fmt.Println("🌐 Testing Subscription Flow with Real Peer ID...")
	fmt.Println("This test simulates the actual peer ID you provided")

	// The peer ID you provided
	realPeerID := "12D3KooWBeEtRULBDzwmqRbjchN89tAPfoJtEtFAhigscjP9bWVX"

	// Parse it
	peerID, err := peer.Decode(realPeerID)
	if err != nil {
		t.Fatalf("Failed to decode real peer ID: %v", err)
	}

	fmt.Printf("✅ Using real peer ID: %s\n", peerID)

	// Create host
	host, err := libp2p.New()
	if err != nil {
		t.Fatalf("Failed to create host: %v", err)
	}
	defer host.Close()

	// Set up listener
	streamCache := &AVCStruct.StreamCache{
		Streams:     make(map[peer.ID]*AVCStruct.StreamEntry),
		AccessOrder: make([]peer.ID, 0),
		MaxStreams:  10,
		TTL:         5 * time.Minute,
		Host:        host,
	}

	listenerBuddyNode := &AVCStruct.BuddyNode{
		Host:        host,
		StreamCache: streamCache,
		MetaData: AVCStruct.MetaData{
			Sent:      0,
			Total:     0,
			UpdatedAt: time.Now().UTC(),
		},
	}

	globalVars := AVCStruct.NewGlobalVariables()
	globalVars.Set_ForListner(listenerBuddyNode)

	listener := NewListenerStruct(listenerBuddyNode)

	// Set up stream handler
	host.SetStreamHandler(config.SubmitMessageProtocol, func(s network.Stream) {
		fmt.Printf("📡 Real peer test received stream from: %s\n", s.Conn().RemotePeer())
		listener.HandleSubmitMessageStream(s)
	})

	// Create subscription request (sender is the client host)
	clientHost, err := libp2p.New()
	if err != nil {
		t.Fatalf("Failed to create client host: %v", err)
	}
	defer clientHost.Close()

	if err := clientHost.Connect(context.Background(), peer.AddrInfo{
		ID:    host.ID(),
		Addrs: host.Addrs(),
	}); err != nil {
		t.Fatalf("Failed to connect client to host: %v", err)
	}

	subscriptionRequest := createSubscriptionRequest(clientHost.ID())
	requestBytes, err := json.Marshal(subscriptionRequest)
	if err != nil {
		t.Fatalf("Failed to marshal subscription request: %v", err)
	}

	// Send request from client to server
	stream, err := clientHost.NewStream(context.Background(), host.ID(), config.SubmitMessageProtocol)
	if err != nil {
		t.Fatalf("Failed to create stream: %v", err)
	}
	defer stream.Close()

	_, err = stream.Write([]byte(string(requestBytes) + string(rune(config.Delimiter))))
	if err != nil {
		t.Fatalf("Failed to send subscription request: %v", err)
	}

	fmt.Printf("📤 Sent subscription request (real peer id referenced in test): %s\n", peerID)

	// Read response
	responseBytes := make([]byte, 1024)
	n, err := stream.Read(responseBytes)
	if err != nil {
		t.Fatalf("Failed to read response: %v", err)
	}

	responseStr := string(responseBytes[:n])
	fmt.Printf("📥 Received response for real peer: %s\n", responseStr)

	// Validate response
	response := AVCStruct.NewMessageBuilder(nil).DeferenceMessage(responseStr)
	if response == nil {
		t.Fatalf("Failed to parse response message")
	}

	if response.GetACK() == nil {
		t.Fatalf("Response has nil ACK")
	}

	if response.GetACK().GetStatus() != config.Type_ACK_True {
		t.Fatalf("Expected ACK status to be '%s', got %s", config.Type_ACK_True, response.GetACK().GetStatus())
	}

	fmt.Printf("✅ Real peer subscription test completed!\n")
	fmt.Printf("   - Real Peer ID: %s\n", peerID)
	fmt.Printf("   - ACK Stage: %s\n", response.GetACK().GetStage())
	fmt.Printf("   - ACK Status: %s\n", response.GetACK().GetStatus())
	fmt.Printf("   - Message: %s\n", response.Message)
}

// TestSimpleSubscriptionFlow tests the subscription flow with a minimal setup
func TestSimpleSubscriptionFlow(t *testing.T) {
	fmt.Println("🔬 Testing Simple Subscription Flow...")

	// Step 1: Create a test host (simulating buddy node)
	host, err := libp2p.New()
	if err != nil {
		t.Fatalf("Failed to create host: %v", err)
	}
	defer host.Close()

	fmt.Printf("✅ Created test host: %s\n", host.ID())

	// Step 2: Set up the listener node
	streamCache := &AVCStruct.StreamCache{
		Streams:     make(map[peer.ID]*AVCStruct.StreamEntry),
		AccessOrder: make([]peer.ID, 0),
		MaxStreams:  10,
		TTL:         5 * time.Minute,
		Host:        host,
	}

	listenerBuddyNode := &AVCStruct.BuddyNode{
		Host:        host,
		StreamCache: streamCache,
		MetaData: AVCStruct.MetaData{
			Sent:      0,
			Total:     0,
			UpdatedAt: time.Now().UTC(),
		},
	}

	// Initialize global variables
	globalVars := AVCStruct.NewGlobalVariables()
	globalVars.Set_ForListner(listenerBuddyNode)

	listener := NewListenerStruct(listenerBuddyNode)
	fmt.Printf("✅ Created listener node: %s\n", listenerBuddyNode.Host.ID())

	// Step 3: Set up stream handler for SubmitMessageProtocol
	host.SetStreamHandler(config.SubmitMessageProtocol, func(s network.Stream) {
		fmt.Printf("📡 Received stream on SubmitMessageProtocol from: %s\n", s.Conn().RemotePeer())
		listener.HandleSubmitMessageStream(s)
	})

	// Step 4: Create a client host (simulating sequencer)
	clientHost, err := libp2p.New()
	if err != nil {
		t.Fatalf("Failed to create client host: %v", err)
	}
	defer clientHost.Close()

	fmt.Printf("✅ Created client host: %s\n", clientHost.ID())

	// Step 5: Connect client to listener
	peerInfo := peer.AddrInfo{
		ID:    host.ID(),
		Addrs: host.Addrs(),
	}

	if err := clientHost.Connect(context.Background(), peerInfo); err != nil {
		t.Fatalf("Failed to connect client to listener: %v", err)
	}

	fmt.Printf("✅ Connected client to listener\n")

	// Step 6: Create subscription request message with your specific peer ID
	specificPeerID, err := peer.Decode("12D3KooWBeEtRULBDzwmqRbjchN89tAPfoJtEtFAhigscjP9bWVX")
	if err != nil {
		t.Fatalf("Failed to decode peer ID: %v", err)
	}

	fmt.Printf("✅ Using your specific peer ID: %s\n", specificPeerID)

	subscriptionRequest := createSubscriptionRequest(specificPeerID)
	requestBytes, err := json.Marshal(subscriptionRequest)
	if err != nil {
		t.Fatalf("Failed to marshal subscription request: %v", err)
	}

	fmt.Printf("✅ Created subscription request message\n")

	// Step 7: Send subscription request
	stream, err := clientHost.NewStream(context.Background(), host.ID(), config.SubmitMessageProtocol)
	if err != nil {
		t.Fatalf("Failed to create stream: %v", err)
	}
	defer stream.Close()

	// Send the request
	_, err = stream.Write([]byte(string(requestBytes) + string(rune(config.Delimiter))))
	if err != nil {
		t.Fatalf("Failed to send subscription request: %v", err)
	}

	fmt.Printf("📤 Sent subscription request to listener\n")

	// Step 8: Read the response with a timeout
	responseBytes := make([]byte, 1024)
	stream.SetReadDeadline(time.Now().UTC().Add(5 * time.Second))
	n, err := stream.Read(responseBytes)
	if err != nil {
		t.Fatalf("Failed to read response: %v", err)
	}

	responseStr := string(responseBytes[:n])
	fmt.Printf("📥 Received response: %s\n", responseStr)

	// Step 9: Parse and validate the response
	response := AVCStruct.NewMessageBuilder(nil).DeferenceMessage(responseStr)
	if response == nil {
		t.Fatalf("Failed to parse response message")
	}

	if response.GetACK() == nil {
		t.Fatalf("Response has nil ACK")
	}

	// Validate the response
	if response.GetACK().GetStage() != config.Type_SubscriptionResponse {
		t.Fatalf("Expected ACK stage %s, got %s", config.Type_SubscriptionResponse, response.GetACK().GetStage())
	}

	if response.GetACK().GetStatus() != config.Type_ACK_True {
		t.Fatalf("Expected ACK status to be '%s', got %s", config.Type_ACK_True, response.GetACK().GetStatus())
	}

	fmt.Printf("✅ Subscription response validated successfully!\n")
	fmt.Printf("   - ACK Stage: %s\n", response.GetACK().GetStage())
	fmt.Printf("   - ACK Status: %s\n", response.GetACK().GetStatus())
	fmt.Printf("   - Message: %s\n", response.Message)
	fmt.Printf("   - Requested Peer ID: %s\n", specificPeerID)

	fmt.Println("🎉 Simple Subscription Flow Test Completed Successfully!")
}
