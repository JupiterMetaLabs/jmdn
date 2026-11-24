package gatekeeper

import (
	"context"
	"fmt"
	"runtime/debug"

	"github.com/JupiterMetaLabs/ion"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RecoveryUnaryInterceptor returns a grpc.UnaryServerInterceptor that recovers
// from any panic raised inside a unary handler and converts it into a
// codes.Internal error instead of letting it unwind and crash the whole process.
//
// This is a small, dependency-free equivalent of
// go-grpc-middleware/recovery (which is not vendored in this module). It is
// intended to be attached via grpc.ChainUnaryInterceptor so it composes with the
// single grpc.UnaryInterceptor that NewSecureGRPCServer already installs (the
// gatekeeper security interceptor runs first, then this recovery guard wraps the
// handler call).
//
// logger may be nil; in that case the panic is only surfaced as the returned
// error.
func RecoveryUnaryInterceptor(logger *ion.Ion) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
		defer func() {
			if r := recover(); r != nil {
				if logger != nil {
					method := ""
					if info != nil {
						method = info.FullMethod
					}
					logger.Error(ctx, "gRPC handler panic recovered",
						fmt.Errorf("panic: %v", r),
						ion.String("method", method),
						ion.String("stack", string(debug.Stack())))
				}
				err = status.Errorf(codes.Internal, "internal server error")
			}
		}()
		return handler(ctx, req)
	}
}
