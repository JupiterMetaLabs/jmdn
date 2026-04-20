package CLI

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"time"

	pb "gossipnode/CLI/proto"
	"gossipnode/DB_OPs/thebestatus"
	"gossipnode/config/settings"
	"gossipnode/config/version"
	"gossipnode/pkg/gatekeeper"

	"github.com/JupiterMetaLabs/ion"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// CLIServer implements the gRPC server
type CLIServer struct {
	handler *CommandHandler
	logger  *ion.Ion
	pb.UnimplementedCLIServiceServer
}

// NewCLIServer creates a new gRPC server instance
func NewCLIServer(handler *CommandHandler, logger *ion.Ion) *CLIServer {
	return &CLIServer{
		handler: handler,
		logger:  logger,
	}
}

// Register registers the server with a gRPC server
func (s *CLIServer) Register(grpcServer *grpc.Server) {
	pb.RegisterCLIServiceServer(grpcServer, s)
}

// Peer management
func (s *CLIServer) ListPeers(ctx context.Context, _ *emptypb.Empty) (*pb.PeerList, error) {
	resp, err := s.handler.HandleListPeers()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	peers := make([]*pb.Peer, 0, len(resp.Peers))
	for _, p := range resp.Peers {
		peers = append(peers, &pb.Peer{
			Id:            p.PeerID.String(),
			Multiaddr:     p.Multiaddr,
			HeartbeatFail: int32(p.HeartbeatFail),
			IsAlive:       p.IsAlive,
			Status:        p.Status,
			LastSeen:      p.LastSeen,
		})
	}

	return &pb.PeerList{Peers: peers}, nil
}

func (s *CLIServer) ReturnAddrs(ctx context.Context, _ *emptypb.Empty) (*pb.Addrs, error) {
	resp, err := s.handler.ReturnAddrs()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pb.Addrs{Peers: resp.Peers, Total: int32(resp.Total), Error: resp.Error}, nil
}

func (s *CLIServer) AddPeer(ctx context.Context, req *pb.PeerRequest) (*pb.OperationResponse, error) {
	success, err := s.handler.HandleAddPeer(req.Peer)
	if err != nil {
		return &pb.OperationResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &pb.OperationResponse{
		Success: success,
		Message: "Peer added successfully",
	}, nil
}

func (s *CLIServer) RemovePeer(ctx context.Context, req *pb.PeerRequest) (*pb.OperationResponse, error) {
	success, err := s.handler.HandleRemovePeer(req.Peer)
	if err != nil {
		return &pb.OperationResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &pb.OperationResponse{
		Success: success,
		Message: "Peer removed successfully",
	}, nil
}

func (s *CLIServer) CleanPeers(ctx context.Context, _ *emptypb.Empty) (*pb.CleanPeersResponse, error) {
	count, err := s.handler.HandleCleanPeers()
	if err != nil {
		return &pb.CleanPeersResponse{
			CleanedCount: 0,
			Error:        err.Error(),
		}, nil
	}
	return &pb.CleanPeersResponse{
		CleanedCount: int32(count),
	}, nil
}

// Version
func (s *CLIServer) GetNodeVersion(ctx context.Context, _ *emptypb.Empty) (*pb.VersionInfo, error) {
	v := version.GetVersionInfo()
	return &pb.VersionInfo{
		GitTag:    v.GitTag,
		GitBranch: v.GitBranch,
		GitCommit: v.GitCommit,
		BuildTime: v.BuildTime,
		GoVersion: v.GoVersion,
	}, nil
}

// Messaging
func (s *CLIServer) SendMessage(ctx context.Context, req *pb.MessageRequest) (*pb.OperationResponse, error) {
	success, err := s.handler.HandleSendMessage(req.Target, req.Message)
	if err != nil {
		return &pb.OperationResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &pb.OperationResponse{
		Success: success,
		Message: "Message sent successfully",
	}, nil
}

func (s *CLIServer) SendYggdrasilMessage(ctx context.Context, req *pb.MessageRequest) (*pb.OperationResponse, error) {
	success, err := s.handler.HandleYggdrasilMessage(req.Target, req.Message)
	if err != nil {
		return &pb.OperationResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &pb.OperationResponse{
		Success: success,
		Message: "Yggdrasil message sent successfully",
	}, nil
}

func (s *CLIServer) SendFile(ctx context.Context, req *pb.FileRequest) (*pb.OperationResponse, error) {
	success, err := s.handler.HandleSendFile(req.Peer, req.Filepath, req.RemoteFilename)
	if err != nil {
		return &pb.OperationResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &pb.OperationResponse{
		Success: success,
		Message: "File sent successfully",
	}, nil
}

func (s *CLIServer) BroadcastMessage(ctx context.Context, req *pb.MessageRequest) (*pb.OperationResponse, error) {
	success, err := s.handler.HandleBroadcast(req.Message)
	if err != nil {
		return &pb.OperationResponse{
			Success: false,
			Message: err.Error(),
		}, nil
	}
	return &pb.OperationResponse{
		Success: success,
		Message: "Broadcast message sent successfully",
	}, nil
}

func (s *CLIServer) GetMessageStats(ctx context.Context, _ *emptypb.Empty) (*pb.MessageStats, error) {
	stats, err := s.handler.HandleShowStats()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pb.MessageStats{
		MessagesSent:     stats.MessagesSent,
		MessagesReceived: stats.MessagesReceived,
		MessagesFailed:   stats.MessagesFailed,
	}, nil
}

// DID Operations
func (s *CLIServer) GetDID(ctx context.Context, req *pb.DIDRequest) (*pb.DIDDocument, error) {
	doc, err := s.handler.HandleGetDID(req.Did)
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return &pb.DIDDocument{
		Did:       doc.DIDAddress,
		Type:      doc.AccountType,
		Nonce:     int64(doc.Nonce),
		Metadata:  convertMetadataToString(doc.Metadata),
		PublicKey: doc.Address.Hex(),
		Balance:   doc.Balance,
		CreatedAt: timestamppb.New(time.Unix(0, doc.CreatedAt)),
		UpdatedAt: timestamppb.New(time.Unix(0, doc.UpdatedAt)),
	}, nil
}

// Database Operations
func (s *CLIServer) FastSync(ctx context.Context, req *pb.PeerRequest) (*pb.SyncStats, error) {
	stats, err := s.handler.HandleFastSyncV2(req.Peer)
	if err != nil {
		return &pb.SyncStats{
			Error: err.Error(),
		}, nil
	}
	return &pb.SyncStats{
		TimeTaken:     int64(stats.TimeTaken.Seconds()),
		MainState:     convertDBState(stats.MainState),
		AccountsState: convertDBState(stats.AccountsState),
	}, nil
}

func (s *CLIServer) FastSyncV2(ctx context.Context, req *pb.PeerRequest) (*pb.SyncStats, error) {
	stats, err := s.handler.HandleFastSyncV2(req.Peer)
	if err != nil {
		return &pb.SyncStats{
			Error: err.Error(),
		}, nil
	}
	return &pb.SyncStats{
		TimeTaken:     int64(stats.TimeTaken.Seconds()),
		MainState:     convertDBState(stats.MainState),
		AccountsState: convertDBState(stats.AccountsState),
	}, nil
}

func (s *CLIServer) GetDatabaseState(ctx context.Context, _ *emptypb.Empty) (*pb.DatabaseStates, error) {
	mainState, accountsState, err := s.handler.CheckDBStats()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pb.DatabaseStates{
		MainDb:     convertDBState(mainState),
		AccountsDb: convertDBState(accountsState),
	}, nil
}

// Helper function to convert database state
func convertDBState(state *thebestatus.Status) *pb.DatabaseState {
	if state == nil {
		return &pb.DatabaseState{}
	}
	// Keep protobuf contract stable while replacing immudb ImmutableState semantics.
	txHash := []byte(fmt.Sprintf("sql:%d lag:%d mode:%s", state.SQLProjected, state.Lag, state.Mode))
	return &pb.DatabaseState{
		TxId:     state.KVHead,
		TxHash:   txHash,
		Database: "thebedb",
	}
}

// convertMetadataToString converts a metadata map to a JSON string
func convertMetadataToString(metadata map[string]interface{}) string {
	if metadata == nil {
		return "{}"
	}
	jsonData, err := json.Marshal(metadata)
	if err != nil {
		log.Printf("Error marshaling metadata: %v", err)
		return "{}"
	}
	return string(jsonData)
}

// StartGRPCServer starts the gRPC server
func StartGRPCServer(handler *CommandHandler, bindAddr string, port int) error {
	return StartGRPCServerWithContext(context.Background(), handler, bindAddr, port)
}

// StartGRPCServerWithContext starts the gRPC server and gracefully stops it when ctx is cancelled.
func StartGRPCServerWithContext(ctx context.Context, handler *CommandHandler, bindAddr string, port int) error {
	// 1. Setup Logger
	l := logger()
	if l == nil {
		// If logger fails, we can't really log about it easily, but we should error out or fallback
		// For now, let's try to proceed with a basic error if possible or just fail
		return fmt.Errorf("failed to initialize logger")
	}

	l.Info(ctx, "Initializing CLI gRPC Server",
		ion.String("bind_addr", bindAddr),
		ion.Int("port", port))

	// 2. Create secure gRPC server via gatekeeper helper
	secCfg := &settings.Get().Security
	grpcServer, serverTLS, err := gatekeeper.NewSecureGRPCServer(
		settings.ServiceCLI, secCfg, l, false,
	)
	if err != nil {
		l.Error(ctx, "Failed to create secure gRPC server", err)
		return fmt.Errorf("failed to create secure gRPC server: %w", err)
	}
	if serverTLS == nil {
		l.Warn(ctx, "TLS is disabled for Admin service - THIS IS INSECURE FOR PRODUCTION", ion.String("service", settings.ServiceCLI))
	} else {
		l.Info(ctx, "TLS enabled for CLI/Admin Server")
	}

	addr := fmt.Sprintf("%s:%d", bindAddr, port)
	l.Info(ctx, "Attempting to listen", ion.String("address", addr))
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		l.Error(ctx, "Failed to listen", err, ion.Int("port", port))
		return fmt.Errorf("failed to listen: %v", err)
	}

	cliServer := NewCLIServer(handler, l)
	cliServer.Register(grpcServer)
	reflection.Register(grpcServer)

	l.Info(ctx, "gRPC server started")

	errCh := make(chan error, 1)
	go func() {
		errCh <- grpcServer.Serve(lis)
	}()

	select {
	case <-ctx.Done():
		l.Info(ctx, "Shutting down gRPC server...")
		done := make(chan struct{})
		go func() {
			grpcServer.GracefulStop()
			close(done)
		}()

		select {
		case <-done:
			l.Info(ctx, "gRPC server stopped gracefully")
		case <-time.After(5 * time.Second):
			l.Warn(ctx, "gRPC server graceful stop timed out, forcing stop")
			grpcServer.Stop()
		}

		_ = lis.Close()
		return nil
	case err := <-errCh:
		if err != nil {
			l.Error(ctx, "gRPC server failed", err)
			return fmt.Errorf("failed to serve: %v", err)
		}
		return nil
	}
}
