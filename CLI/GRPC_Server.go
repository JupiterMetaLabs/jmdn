package CLI

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"time"

	pb "gossipnode/CLI/proto"

	"github.com/codenotary/immudb/pkg/api/schema"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// CLIServer implements the gRPC server
type CLIServer struct {
	handler *CommandHandler
	pb.UnimplementedCLIServiceServer
}

// NewCLIServer creates a new gRPC server instance
func NewCLIServer(handler *CommandHandler) *CLIServer {
	return &CLIServer{handler: handler}
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
		CreatedAt: timestamppb.New(time.Unix(doc.CreatedAt, 0)),
		UpdatedAt: timestamppb.New(time.Unix(doc.UpdatedAt, 0)),
	}, nil
}

// Database Operations
func (s *CLIServer) FastSync(ctx context.Context, req *pb.PeerRequest) (*pb.SyncStats, error) {
	stats, err := s.handler.HandleFastSync(req.Peer)
	if err != nil {
		return &pb.SyncStats{
			Error: err.Error(),
		}, nil
	}
	return &pb.SyncStats{
		TimeTaken:     int64(stats.TimeTaken.Seconds()),
		MainState:     convertDBState(&stats.MainState),
		AccountsState: convertDBState(&stats.AccountsState),
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
func convertDBState(state *schema.ImmutableState) *pb.DatabaseState {
	return &pb.DatabaseState{
		TxId:     state.TxId,
		TxHash:   state.TxHash,
		Database: state.Db,
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
func StartGRPCServer(handler *CommandHandler, port int) error {
	fmt.Printf("Attempting to listen on port %d...\n", port)
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		fmt.Printf("Failed to listen on port %d: %v\n", port, err)
		return fmt.Errorf("failed to listen: %v", err)
	}
	fmt.Printf("Successfully listening on port %d\n", port)

	grpcServer := grpc.NewServer()
	cliServer := NewCLIServer(handler)
	cliServer.Register(grpcServer)

	fmt.Printf("gRPC server configured, starting to serve...\n")
	log.Printf("Starting gRPC server on port %d", port)
	if err := grpcServer.Serve(lis); err != nil {
		fmt.Printf("gRPC server failed to serve: %v\n", err)
		return fmt.Errorf("failed to serve: %v", err)
	}

	return nil
}
