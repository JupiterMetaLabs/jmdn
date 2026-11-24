package Router

import (
	"context"
	"fmt"
	"time"

	Service "gossipnode/AVC/BuddyNodes/MessagePassing/Service"
	PubSubMessages "gossipnode/config/PubSubMessages"

	"github.com/libp2p/go-libp2p/core/peer"
)

// Router handles all verification functions from VerificationService
type Router struct {
	verificationService *Service.VerificationService
}

func (r *Router) Close() {
	// Clean up resources held by verificationService, if necessary
	// Currently, VerificationService does not require explicit cleanup,
	// but this method is here for future extensibility.
	r.verificationService = nil
}

// NewRouter creates a new router instance
func NewRouter(buddyNode *PubSubMessages.BuddyNode) *Router {
	return &Router{
		verificationService: Service.NewVerificationService(buddyNode),
	}
}

// VerifySubscriptions routes bulk subscription verification
func (r *Router) VerifySubscriptions(logger_ctx context.Context, expectedPeers []peer.ID, timeout time.Duration) (map[peer.ID]string, error) {
	if r.verificationService == nil {
		return nil, fmt.Errorf("verification service not initialized")
	}

	return r.verificationService.VerifySubscriptions(logger_ctx, expectedPeers, timeout)
}
