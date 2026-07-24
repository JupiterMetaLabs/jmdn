package seednode

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"gossipnode/seednode/committee"
	peerpb "gossipnode/seednode/proto"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"google.golang.org/grpc/metadata"
)

// seqAuthMethod is the RPC method name bound into the sequencer auth challenge.
// MUST match the seed's SequencerAuthenticator.Verify(method=...) for ListBuddy.
const seqAuthMethod = "ListBuddy"

// sequencerAuthContext returns ctx augmented with the S4 sequencer-auth gRPC
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

// ListBuddySigned calls ListBuddy with sequencer authentication (S4). Only the
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
