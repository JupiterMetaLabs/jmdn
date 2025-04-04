package DID

import (
    "context"
    "fmt"
    "math/big"
    "net"
    "sync"
    "time"

    // "github.com/codenotary/immudb/pkg/api/schema"
    // "github.com/codenotary/immudb/pkg/client"
    "github.com/libp2p/go-libp2p/core/host"
    "github.com/rs/zerolog/log"
    "google.golang.org/grpc"
    "google.golang.org/grpc/codes"
    // "google.golang.org/grpc/metadata"
    "google.golang.org/grpc/reflection"
    "google.golang.org/grpc/status"
    "google.golang.org/protobuf/types/known/emptypb"

    "gossipnode/DB_OPs"
    "gossipnode/config"
    "gossipnode/messaging"
    pb "gossipnode/DID/proto"
)

// DIDServer implements the DIDService
type DIDServer struct {
    pb.UnimplementedDIDServiceServer
    host         host.Host
    mutex        sync.RWMutex
    statsAge     int64
    stats        *pb.DIDStats
    accountsClient *config.ImmuClient
    inMemoryDIDs map[string]*DB_OPs.DIDDocument
    standalone   bool
}

// NewDIDServer creates a new DID server
func NewDIDServer(h host.Host) *DIDServer {
    return &DIDServer{
        host:         h,
        statsAge:     0,
        stats:        &pb.DIDStats{},
        inMemoryDIDs: make(map[string]*DB_OPs.DIDDocument),
        standalone:   false,
    }
}

// Initialize sets up the accounts database connection
func (s *DIDServer) Initialize() error {
    // First try to initialize the accounts client
    client, err := DB_OPs.NewAccountsClient()
    if err != nil {
        log.Warn().Err(err).Msg("Failed to initialize accounts database connection. Running in standalone mode.")
        s.standalone = true
        return nil
    }

    s.mutex.Lock()
    s.accountsClient = client
    s.standalone = false
    s.mutex.Unlock()

    log.Info().Msg("DID server successfully connected to accounts database")
    return nil
}

// Close releases resources used by the server
func (s *DIDServer) Close() {
    s.mutex.Lock()
    defer s.mutex.Unlock()

    if s.accountsClient != nil {
        DB_OPs.Close(s.accountsClient)
        s.accountsClient = nil
    }
}

// storeDID stores a DID either in the database or in memory
func (s *DIDServer) storeDID(didDoc *DB_OPs.DIDDocument) error {
    s.mutex.Lock()
    defer s.mutex.Unlock()

    // Set creation and update timestamps if not set
    if didDoc.CreatedAt == 0 {
        didDoc.CreatedAt = time.Now().Unix()
    }
    didDoc.UpdatedAt = time.Now().Unix()

    // If in standalone mode, store in memory
    if s.standalone || s.accountsClient == nil {
        s.inMemoryDIDs[didDoc.DID] = didDoc
        return nil
    }

    // Otherwise store in the database
    return DB_OPs.StoreDID(s.accountsClient, didDoc)
}

// getDID retrieves a DID either from the database or memory
func (s *DIDServer) getDID(did string) (*DB_OPs.DIDDocument, error) {
    s.mutex.RLock()
    defer s.mutex.RUnlock()

    // If in standalone mode, get from memory
    if s.standalone || s.accountsClient == nil {
        doc, exists := s.inMemoryDIDs[did]
        if !exists {
            return nil, fmt.Errorf("DID not found: %s", did)
        }
        return doc, nil
    }

    // Otherwise get from the database
    return DB_OPs.GetDID(s.accountsClient, did)
}

// listDIDs retrieves all DIDs either from the database or memory
func (s *DIDServer) listDIDs(limit int) ([]*DB_OPs.DIDDocument, error) {
    s.mutex.RLock()
    defer s.mutex.RUnlock()

    // If in standalone mode, get from memory
    if s.standalone || s.accountsClient == nil {
        docs := make([]*DB_OPs.DIDDocument, 0, len(s.inMemoryDIDs))
        count := 0
        for _, doc := range s.inMemoryDIDs {
            if count >= limit {
                break
            }
            docs = append(docs, doc)
            count++
        }
        return docs, nil
    }

    // Otherwise get from the database
    return DB_OPs.ListAllDIDs(s.accountsClient, limit)
}

// RegisterDID registers a new DID
func (s *DIDServer) RegisterDID(ctx context.Context, req *pb.RegisterDIDRequest) (*pb.RegisterDIDResponse, error) {
    if req.Did == "" || req.PublicKey == "" {
        return nil, status.Error(codes.InvalidArgument, "DID and public key are required")
    }

    // Check if DID already exists
    existingDID, err := s.getDID(req.Did)
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

    // Create DID document
    now := time.Now().Unix()
    didDoc := &DB_OPs.DIDDocument{
        DID:       req.Did,
        PublicKey: req.PublicKey,
        Balance:   "0", // Initial balance
        CreatedAt: now,
        UpdatedAt: now,
    }

    // Store the DID
    err = s.storeDID(didDoc)
    if err != nil {
        log.Error().Err(err).Str("did", req.Did).Msg("Failed to store DID")
        return nil, status.Errorf(codes.Internal, "Failed to store DID: %v", err)
    }

    // If not in standalone mode, propagate the DID
    if !s.standalone && s.accountsClient != nil {
        // Try to propagate to network, but don't fail if it doesn't work
        err = messaging.PropagateDID(s.host, didDoc)
        if err != nil {
            log.Warn().Err(err).Str("did", req.Did).Msg("Failed to propagate DID to network")
        }
    }

    storageMode := "in-memory storage only"
    if !s.standalone && s.accountsClient != nil {
        storageMode = "persistent database storage"
    }

    // Return response with initial balance of 0
    return &pb.RegisterDIDResponse{
        Success: true,
        Message: fmt.Sprintf("DID registered successfully using %s", storageMode),
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

    // Retrieve DID
    didDoc, err := s.getDID(req.Did)
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

    // Get DIDs
    dids, err := s.listDIDs(limit)
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
    // Get all DIDs
    dids, err := s.listDIDs(10000)
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
func StartDIDServer(h host.Host, address string, existingClient *config.ImmuClient) error {
    lis, err := net.Listen("tcp", address)
    if err != nil {
        return fmt.Errorf("failed to listen on %s: %w", address, err)
    }
    
    // Create DID server with existing client
    server := NewDIDServer(h)
    
    // Set the existing client if provided
    if existingClient != nil {
        server.accountsClient = existingClient
        server.standalone = false
    } else {
        // Try to initialize a new client
        if err := server.Initialize(); err != nil {
            log.Warn().Err(err).Msg("Failed to initialize DID server database. Running in standalone mode.")
            server.standalone = true
        }
    }
    
    // Create gRPC server
    grpcServer := grpc.NewServer()
    pb.RegisterDIDServiceServer(grpcServer, server)
    
    // Register reflection service
    reflection.Register(grpcServer)
    
    log.Info().
        Str("address", address).
        Bool("standalone", server.standalone).
        Msg("Starting DID gRPC server")
    
    return grpcServer.Serve(lis)
}

// UpdateDID updates an existing DID document
func (s *DIDServer) UpdateDID(ctx context.Context, req *pb.UpdateDIDRequest) (*pb.UpdateDIDResponse, error) {
    if req.Did == "" {
        return nil, status.Error(codes.InvalidArgument, "DID is required")
    }

    // Retrieve existing DID
    existingDID, err := s.getDID(req.Did)
    if err != nil {
        return nil, status.Errorf(codes.NotFound, "DID not found: %v", err)
    }

    // Store original values for logging changes
    originalBalance := existingDID.Balance
    originalPublicKey := existingDID.PublicKey
    changesApplied := false

    // Update balance if specified
    if req.Balance != "" {
        // Check if balance is a valid number
        _, ok := new(big.Int).SetString(req.Balance, 10)
        if !ok {
            return nil, status.Error(codes.InvalidArgument, "Invalid balance format")
        }
        if existingDID.Balance != req.Balance {
            existingDID.Balance = req.Balance
            changesApplied = true
        }
    }

    // Update public key if specified
    if req.PublicKey != "" && existingDID.PublicKey != req.PublicKey {
        existingDID.PublicKey = req.PublicKey
        changesApplied = true
    }

    // Only update if changes were made
    if !changesApplied {
        return &pb.UpdateDIDResponse{
            Success: true,
            Message: "No changes needed - DID already up to date",
            DidInfo: &pb.DIDInfo{
                Did:       existingDID.DID,
                PublicKey: existingDID.PublicKey,
                Balance:   existingDID.Balance,
                CreatedAt: existingDID.CreatedAt,
                UpdatedAt: existingDID.UpdatedAt,
            },
        }, nil
    }

    // Update timestamp
    existingDID.UpdatedAt = time.Now().Unix()

    // Store the updated DID
    err = s.storeDID(existingDID)
    if err != nil {
        log.Error().Err(err).
            Str("did", req.Did).
            Str("original_balance", originalBalance).
            Str("new_balance", existingDID.Balance).
            Msg("Failed to update DID")
        return nil, status.Errorf(codes.Internal, "Failed to update DID: %v", err)
    }

    // If not in standalone mode, propagate the DID update
    if !s.standalone && s.accountsClient != nil {
        err = messaging.PropagateDID(s.host, existingDID)
        if err != nil {
            log.Warn().
                Err(err).
                Str("did", req.Did).
                Str("original_balance", originalBalance).
                Str("new_balance", existingDID.Balance).
                Bool("key_changed", originalPublicKey != existingDID.PublicKey).
                Msg("Failed to propagate DID update to network")
        } else {
            log.Info().
                Str("did", req.Did).
                Str("original_balance", originalBalance).
                Str("new_balance", existingDID.Balance).
                Bool("key_changed", originalPublicKey != existingDID.PublicKey).
                Msg("Successfully propagated DID update to network")
        }
    }

    // Return response with updated DID
    return &pb.UpdateDIDResponse{
        Success: true,
        Message: "DID updated successfully",
        DidInfo: &pb.DIDInfo{
            Did:       existingDID.DID,
            PublicKey: existingDID.PublicKey,
            Balance:   existingDID.Balance,
            CreatedAt: existingDID.CreatedAt,
            UpdatedAt: existingDID.UpdatedAt,
        },
    }, nil
}