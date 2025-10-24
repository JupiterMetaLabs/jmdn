package selection

import (
	"context"
	"fmt"
	"time"

	pb "gossipnode/AVC/NodeSelection/api/proto/peer"
	"gossipnode/AVC/NodeSelection/pkg/types"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/protobuf/types/known/emptypb"
)

// PeerClient wraps gRPC PeerDirectory client
type PeerClient struct {
	conn   *grpc.ClientConn
	client pb.PeerDirectoryClient
}

// NewPeerClient creates a new peer directory client with optimized settings
func NewPeerClient(address string, timeout time.Duration) (*PeerClient, error) {
	// Optimized gRPC dial options
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                10 * time.Second,
			Timeout:             3 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(10 * 1024 * 1024), // 10MB
		),
	}

	conn, err := grpc.NewClient(address, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create peer directory client: %w", err)
	}

	client := pb.NewPeerDirectoryClient(conn)

	// Verify connection with health check
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = client.HealthCheck(ctx, &emptypb.Empty{})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("health check failed: %w", err)
	}

	return &PeerClient{
		conn:   conn,
		client: client,
	}, nil
}

// ListAllPeers fetches all active peers from the directory
func (c *PeerClient) ListAllPeers(ctx context.Context) ([]Node, error) {
	allNodes := make([]Node, 0, 100)
	var afterPeerID string
	limit := int32(100)
	totalFetched := 0

	for {
		reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)

		req := &pb.PeerListRequest{
			AfterPeerId: afterPeerID,
			Limit:       limit,
			Status:      pb.PeerStatus_PEER_STATUS_ACTIVE,
		}

		fmt.Printf("🔍 Fetching peers (after: %s, limit: %d)...\n", afterPeerID, limit)

		resp, err := c.client.ListPeers(reqCtx, req)
		cancel()

		if err != nil {
			return nil, fmt.Errorf("failed to list peers: %w", err)
		}

		batchSize := len(resp.Peers)
		totalFetched += batchSize

		// Convert proto peers to Node type
		for _, peer := range resp.Peers {
			node := convertProtoToNode(peer)
			allNodes = append(allNodes, node)
		}

		// Check if there are more peers to fetch
		if !resp.HasMore {
			fmt.Println("✅ No more peers to fetch")
			break
		}

		afterPeerID = resp.Last
	}

	return allNodes, nil
}

// ListBuddyPeers fetches available buddy peers (excluding recent buddies)
func (c *PeerClient) ListBuddyPeers(ctx context.Context) ([]Node, error) {
	fmt.Printf("Debugging 4\n")
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	fmt.Printf("Debugging 5\n")
	resp, err := c.client.ListBuddy(reqCtx, &pb.ListBuddyRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to list buddy peers: %w", err)
	}

	fmt.Printf("Debugging 6\n")
	fmt.Printf("✅ Received %d buddy-eligible peers\n", len(resp.Peers))

	if len(resp.Peers) == 0 {
		return nil, fmt.Errorf("no buddy peers found")
	}

	nodes := make([]Node, 0, len(resp.Peers))
	fmt.Printf("%+v\n", resp.Peers[0])
	for _, peer := range resp.Peers {
		node := convertBuddyPeerRecordToNode(peer)
		nodes = append(nodes, node)
	}

	return nodes, nil
}

// UpdateBuddies updates the buddy list on the peer directory
func (c *PeerClient) UpdateBuddies(ctx context.Context, peerIDs []string) error {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	resp, err := c.client.UpdateBuddy(reqCtx, &pb.UpdateBuddyRequest{
		PeerIds: peerIDs,
	})
	if err != nil {
		return fmt.Errorf("failed to update buddies: %w", err)
	}

	if !resp.Accepted {
		return fmt.Errorf("buddy update rejected: %s", resp.Message)
	}

	fmt.Printf("✅ Updated buddy list: added %d, removed %d\n", resp.AddedCount, resp.RemovedCount)
	return nil
}

// convertProtoToNode converts protobuf SignedPeerRecord to Node
func convertProtoToNode(peer *pb.SignedPeerRecord) Node {
	fmt.Println("ℹ️ Converting peer:", peer.PeerId)

	var lastSeen time.Time
	if peer.LastUpdated > 0 {
		lastSeen = time.Unix(peer.LastUpdated, 0)
	} else {
		lastSeen = time.Unix(peer.FirstCreated, 0)
	}

	var address string
	if len(peer.Multiaddrs) > 0 {
		address = peer.Multiaddrs[0]
	}

	var asn string
	if peer.Labels != nil {
		asn = peer.Labels["asn"]
	}
	// Fallback if ASN not in labels
	if asn == "" {
		asn = "AS" + peer.Region // Use region as fallback
	}

	isActive := peer.CurrentStatus == pb.PeerStatus_PEER_STATUS_ACTIVE &&
		time.Since(lastSeen) <= 10*time.Minute

	capacity := int(peer.Weights * 100)
	if capacity == 0 {
		capacity = 100 // Default capacity
	}

	// Selection score logic with proper defaults
	selectionScore := 0.5 // Default starting score for new nodes
	if peer.Labels != nil && peer.Labels["selection_score"] != "" {
		fmt.Sscanf(peer.Labels["selection_score"], "%f", &selectionScore)
	} else if peer.Weights > 0 {
		selectionScore = float64(peer.Weights)
		// Clamp to valid range [0.5, 1.0)
		if selectionScore < 0.5 {
			selectionScore = 0.5
		}
		if selectionScore >= 1.0 {
			selectionScore = 0.99
		}
	}

	return Node{
		Node: types.Node{
			ID:              peer.PeerId,
			Address:         address,
			ReputationScore: float64(peer.Weights),
			ASN:             asn,
			LastSeen:        lastSeen,
			IsActive:        isActive,
			Capacity:        capacity,
		},
		SelectionScore: selectionScore,
	}
}

// convertBuddyPeerRecordToNode converts BuddyPeerRecord to Node
func convertBuddyPeerRecordToNode(peer *pb.BuddyPeerRecord) Node {
	var lastSeen time.Time
	if peer.LastUpdated > 0 {
		lastSeen = time.Unix(peer.LastUpdated, 0)
	} else {
		lastSeen = time.Unix(peer.FirstCreated, 0)
	}

	var address string
	if len(peer.Multiaddrs) > 0 {
		address = peer.Multiaddrs[0]
	}

	// ASN from region or use default
	asn := "UNKNOWN"
	if peer.Region != "" {
		asn = "AS-" + peer.Region
	}

	capacity := int(peer.Weights * 100)
	if capacity == 0 {
		capacity = 100
	}

	// Selection score from weights
	selectionScore := float64(peer.Weights)
	if selectionScore < 0.5 {
		selectionScore = 0.5
	}
	if selectionScore >= 1.0 {
		selectionScore = 0.99
	}

	return Node{
		Node: types.Node{
			ID:              peer.PeerId,
			Address:         address,
			ReputationScore: float64(peer.Weights),
			ASN:             asn,
			LastSeen:        lastSeen,
			IsActive:        true, // Buddy peers are always active
			Capacity:        capacity,
		},
		SelectionScore: selectionScore,
	}
}

// Close closes the gRPC connection
func (c *PeerClient) Close() error {
	return c.conn.Close()
}
