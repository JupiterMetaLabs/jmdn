package DID

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "gossipnode/DID/proto"
)

// RegisterDID must never write an account: an account is created only by its
// first incoming transfer, which pays for it.
func TestRegisterDIDIsRetired(t *testing.T) {
	s := &AccountServer{}
	_, err := s.RegisterDID(context.Background(), &pb.RegisterDIDRequest{
		Did:       "did:jmdt:x",
		PublicKey: "0x00000000000000000000000000000000000000a1",
	})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("RegisterDID: got %v, want codes.Unimplemented", err)
	}
}
