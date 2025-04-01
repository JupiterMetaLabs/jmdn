package DID

import (
    "context"
    "fmt"
    "math/big"
    "net"
    "sync"
    "time"

    "github.com/libp2p/go-libp2p/core/host"
    "github.com/rs/zerolog/log"
    "google.golang.org/grpc"
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/reflection"
    "google.golang.org/grpc/status"
    "google.golang.org/protobuf/types/known/emptypb"

    // "gossipnode/DB_OPs"
    "gossipnode/messaging"
    pb "gossipnode/DID/proto"
)

// DIDServer implements the DIDService
type DIDServer struct {
    pb.UnimplementedDIDServiceServer
    host     host.Host
    mutex    sync.RWMutex
    statsAge int64
    stats    *pb.DIDStats
}

// NewDIDServer creates a new DID server
func NewDIDServer(h host.Host) *DIDServer {
    return &DIDServer{
        host:     h,
        statsAge: 0,
        stats:    &pb.DIDStats{},
    }
}

// RegisterDID registers a new DID and propagates it to the network
func (s *DIDServer) RegisterDID(ctx context.Context, req *pb.RegisterDIDRequest) (*pb.RegisterDIDResponse, error) {
    if req.Did == "" || req.PublicKey == "" {
        return nil, status.Error(codes.InvalidArgument, "DID and public key are required")
    }

    // Check if DID already exists
    existingDID, err := messaging.GetDID(req.Did)
    if err == nil && existingDID != nil {
        return &pb.RegisterDIDResponse{
            Success: false,
            Message: fmt.Sprintf("DID %s is already registered", req.Did),
            DidInfo: &pb.DIDInfo{
                Did:       existingDID.DID,
                PublicKey: existingDID.PublicKey,
                Balance:   existingDID.Balance,
                CreatedAt: existingDID.CreatedAt,
                UpdatedAt: existingDID.UpdatedAt,
            },
        }, nil
    }

    // Propagate DID to the network
    err = messaging.PropagateDID(s.host, req.Did, req.PublicKey)
    if err != nil {
        log.Error().Err(err).Str("did", req.Did).Msg("Failed to propagate DID")
        return nil, status.Errorf(codes.Internal, "Failed to propagate DID: %v", err)
    }

    // Return response with initial balance of 0
    now := time.Now().Unix()
    return &pb.RegisterDIDResponse{
        Success: true,
        Message: "DID registered and propagated successfully",
        DidInfo: &pb.DIDInfo{
            Did:       req.Did,
            PublicKey: req.PublicKey,
            Balance:   "0",
            CreatedAt: now,
            UpdatedAt: now,
        },
    }, nil
}

// GetDID retrieves information about a specific DID
func (s *DIDServer) GetDID(ctx context.Context, req *pb.GetDIDRequest) (*pb.DIDResponse, error) {
    if req.Did == "" {
        return nil, status.Error(codes.InvalidArgument, "DID is required")
    }

    // Retrieve DID from database
    didDoc, err := messaging.GetDID(req.Did)
    if err != nil {
        return &pb.DIDResponse{
            Exists: false,
            DidInfo: &pb.DIDInfo{
                Did: req.Did,
            },
        }, nil
    }

    // Return DID information
    return &pb.DIDResponse{
        Exists: true,
        DidInfo: &pb.DIDInfo{
            Did:       didDoc.DID,
            PublicKey: didDoc.PublicKey,
            Balance:   didDoc.Balance,
            CreatedAt: didDoc.CreatedAt,
            UpdatedAt: didDoc.UpdatedAt,
        },
    }, nil
}

// ListDIDs lists all DIDs with pagination
func (s *DIDServer) ListDIDs(ctx context.Context, req *pb.ListDIDsRequest) (*pb.ListDIDsResponse, error) {
    limit := int(req.Limit)
    if limit <= 0 {
        limit = 100 // Default limit
    }

    // Get DIDs from database
    dids, err := messaging.ListAllDIDs(limit)
    if err != nil {
        log.Error().Err(err).Msg("Failed to list DIDs")
        return nil, status.Errorf(codes.Internal, "Failed to list DIDs: %v", err)
    }

    // Convert to proto format
    var pbDids []*pb.DIDInfo
    for _, did := range dids {
        pbDids = append(pbDids, &pb.DIDInfo{
            Did:       did.DID,
            PublicKey: did.PublicKey,
            Balance:   did.Balance,
            CreatedAt: did.CreatedAt,
            UpdatedAt: did.UpdatedAt,
        })
    }

    return &pb.ListDIDsResponse{
        Dids:       pbDids,
        TotalCount: int32(len(pbDids)),
    }, nil
}

// GetDIDStats retrieves current DID system statistics
func (s *DIDServer) GetDIDStats(ctx context.Context, _ *emptypb.Empty) (*pb.DIDStats, error) {
    s.mutex.RLock()
    statsAge := s.statsAge
    stats := s.stats
    s.mutex.RUnlock()

    // If stats are older than 60 seconds, refresh them
    if time.Now().Unix()-statsAge > 60 {
        s.refreshStats()
        s.mutex.RLock()
        stats = s.stats
        s.mutex.RUnlock()
    }

    return stats, nil
}

// refreshStats refreshes the DID statistics
func (s *DIDServer) refreshStats() {
    // Get all DIDs (with a high limit)
    dids, err := messaging.ListAllDIDs(10000)
    if err != nil {
        log.Error().Err(err).Msg("Failed to get DIDs for stats calculation")
        return
    }

    // Calculate total balance
    totalBalance := big.NewInt(0)
    for _, did := range dids {
        if did.Balance != "" {
            balance, ok := new(big.Int).SetString(did.Balance, 10)
            if ok {
                totalBalance.Add(totalBalance, balance)
            }
        }
    }

    // Update stats
    s.mutex.Lock()
    s.stats = &pb.DIDStats{
        TotalDids:    int32(len(dids)),
        TotalBalance: totalBalance.String(),
        LastUpdate:   time.Now().Unix(),
    }
    s.statsAge = time.Now().Unix()
    s.mutex.Unlock()
}

// StartDIDServer starts the DID gRPC server
func StartDIDServer(h host.Host, address string) error {
    lis, err := net.Listen("tcp", address)
    if err != nil {
        return fmt.Errorf("failed to listen on %s: %v", address, err)
    }

    server := grpc.NewServer()
    didServer := NewDIDServer(h)
    pb.RegisterDIDServiceServer(server, didServer)

    // Register reflection service for tools like grpcurl
    reflection.Register(server)

    log.Info().Str("address", address).Msg("Starting DID gRPC server")
    return server.Serve(lis)
}