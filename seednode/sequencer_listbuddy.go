package seednode

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"gossipnode/seednode/committee"
	peerpb "gossipnode/seednode/proto"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"google.golang.org/grpc/metadata"
)

// seqAuthMethod is the RPC method name bound into the sequencer auth challenge.
// MUST match the seed's SequencerAuthenticator.Verify(method=...) for ListBuddy.
const seqAuthMethod = "ListBuddy"

// sequencerSignKey is the OPTIONAL sequencer identity key used to auto-sign
// ListBuddy (committee-selection) requests. It is registered once, only on
// the sequencer node (SetSequencerSignKey), so ListBuddy calls made through the
// NodeSelection router are authenticated without threading the key through every
// layer. Non-sequencer nodes never set it, send unsigned selection requests, and
// are refused by the seed (fail closed).
var (
	seqKeyMu         sync.RWMutex
	sequencerSignKey ic.PrivKey
)

// SetSequencerSignKey registers this node's libp2p identity key as the sequencer
// signer for committee-selection requests. Call once at sequencer startup.
func SetSequencerSignKey(priv ic.PrivKey) {
	seqKeyMu.Lock()
	sequencerSignKey = priv
	seqKeyMu.Unlock()
}

// currentSequencerSignKey returns the registered sequencer signer, or nil.
func currentSequencerSignKey() ic.PrivKey {
	seqKeyMu.RLock()
	defer seqKeyMu.RUnlock()
	return sequencerSignKey
}

// sequencerAuthContext returns ctx augmented with the sequencer-auth gRPC
// metadata (x-seed-auth-timestamp + x-seed-auth-signature) for a ListBuddy call.
// The signature is over committee.SequencerAuthChallenge(ListBuddy, <peer_id>,
// <unix_ts>) by seqPriv (the sequencer's libp2p identity key). Split out from the
// RPC call so it is unit-testable without a live gRPC client.
func sequencerAuthContext(ctx context.Context, seqPriv ic.PrivKey) (context.Context, error) {
	ts, sigHex, err := committee.SignSequencerRequest(seqPriv, seqAuthMethod, time.Now())
	if err != nil {
		return ctx, fmt.Errorf("sign %s request: %w", seqAuthMethod, err)
	}
	return metadata.AppendToOutgoingContext(ctx,
		committee.SeqAuthTimestampHeader, strconv.FormatInt(ts, 10),
		committee.SeqAuthSignatureHeader, sigHex,
	), nil
}

// ListBuddySigned calls ListBuddy with sequencer authentication. Only the
// authoritative sequencer (whose PeerID the seed has configured as
// SEQUENCER_PEER_ID) can produce a signature the seed accepts; every other
// caller — and any stale request — is refused by the seed (fail closed). Use
// this from the sequencer's selection path instead of the unauthenticated
// ListBuddy.
func (c *Client) ListBuddySigned(ctx context.Context, request *peerpb.ListBuddyRequest, seqPriv ic.PrivKey) (*peerpb.ListBuddyResponse, error) {
	signedCtx, err := sequencerAuthContext(ctx, seqPriv)
	if err != nil {
		return nil, err
	}
	return c.client.ListBuddy(signedCtx, request)
}

// ListBuddyHeads returns peer_id -> latest reported block head from a signed
// ListBuddy call, using the registered sequencer sign key (SetSequencerSignKey).
// It is a best-effort observability helper (the "Built Final Buddies List" alert)
// that reuses the authenticated gRPC channel — no HTTP, no extra credentials.
// Returns an error when no sign key is registered (non-sequencer) or the RPC fails.
func (c *Client) ListBuddyHeads(ctx context.Context) (map[string]uint64, error) {
	seqPriv := currentSequencerSignKey()
	if seqPriv == nil {
		return nil, fmt.Errorf("no sequencer sign key registered (SetSequencerSignKey not called)")
	}
	resp, err := c.ListBuddySigned(ctx, &peerpb.ListBuddyRequest{}, seqPriv)
	if err != nil {
		return nil, err
	}
	heads := make(map[string]uint64, len(resp.GetPeers()))
	for _, p := range resp.GetPeers() {
		if p.GetPeerId() != "" {
			heads[p.GetPeerId()] = p.GetBlockHead()
		}
	}
	return heads, nil
}
