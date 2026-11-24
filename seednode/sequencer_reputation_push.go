package seednode

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"gossipnode/seednode/committee"
	peerpb "gossipnode/seednode/proto"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"google.golang.org/grpc/metadata"
)

// reputationPushAuthMethod mirrors seqAuthMethod's convention (see
// sequencer_listbuddy.go) for the reputation-push call. It MUST match
// whatever method string the seed's SequencerAuthenticator is configured to
// recognize for this RPC once/if server-side verification is added -- see the
// PHASE A4.2 CAVEAT below.
const reputationPushAuthMethod = "PushReputation"

// ErrNotSequencer is returned by PushReputationWeights (never wrapped, check
// with errors.Is) when this node has no registered sequencer sign key — i.e.
// on every node except the one actually acting as sequencer. Callers use this
// to distinguish "nothing to do here" from a genuine RPC failure without
// resorting to string matching or counting.
var ErrNotSequencer = errors.New("seednode: no sequencer sign key registered — not the sequencer")

// sequencerAuthContextForMethod is sequencer_listbuddy.go's sequencerAuthContext
// generalized to an arbitrary method string, so this file can sign a
// "PushReputation" challenge without touching the already-tested ListBuddy
// auth path (sequencerAuthContext / ListBuddySigned stay exactly as they were).
// Same construction: SequencerAuthChallenge(method, sequencer_peer_id, unix_ts)
// signed by the sequencer's libp2p identity key, attached as the same two
// gRPC metadata headers the seed's (future) SequencerAuthenticator reads.
func sequencerAuthContextForMethod(ctx context.Context, seqPriv ic.PrivKey, method string) (context.Context, error) {
	ts, sigHex, err := committee.SignSequencerRequest(seqPriv, method, time.Now())
	if err != nil {
		return ctx, fmt.Errorf("sign %s request: %w", method, err)
	}
	return metadata.AppendToOutgoingContext(ctx,
		committee.SeqAuthTimestampHeader, strconv.FormatInt(ts, 10),
		committee.SeqAuthSignatureHeader, sigHex,
	), nil
}

// PHASE A4.2 CAVEAT -- read before wiring this into anything that matters:
//
// This reuses the EXISTING, already-generated PeerDirectory.UpdatePeerWeights
// RPC (peerpb.UpdatePeerWeightsRequest{PeerId, Weights, V, R, S}) rather than
// adding a new RPC/message, because adding one would require regenerating
// seedNodes' and jmdn's protobuf-generated code with protoc, which is not
// available in this environment (no Go toolchain either) -- hand-editing
// generated .pb.go files (raw descriptor bytes + reflection tables shared
// across every message in the file) is not something that can be done
// correctly without protoc, and a mistake there risks the whole PeerDirectory
// service's wire format, not just this one RPC.
//
// V/R/S are deliberately left EMPTY. As of the seedNodes JMNS branch commit
// this was checked against, the server-side check UpdatePeerWeights calls --
// CryptoManager.VerifyRecord in pkg/peer/crypto.go -- is a no-op stub
// ("we'll skip verification since we don't have the public key ... return
// nil"). Putting fabricated V/R/S bytes here would misrepresent a signature
// that is not actually verified today; leaving them empty is the honest
// reflection of the current state. Two consequences to flag to whoever owns
// seedNodes:
//
//  1. UpdatePeerWeights is currently callable by ANYONE for ANY peer_id with
//     ANY weight -- this is a pre-existing gap in seedNodes, not something
//     this change introduces, but this change is the first real caller of the
//     RPC and will make the gap matter in practice.
//  2. The self-signed design VerifyRecord was stubbed out for cannot support
//     a THIRD PARTY (the sequencer) legitimately updating another peer's
//     weights even once real verification is implemented -- self-signing and
//     sequencer-signing are different trust models on the same three fields.
//     Closing this properly needs seedNodes to accept a second, distinct
//     signer (the configured SequencerPeerID's key) for this one field,
//     which is a schema-compatible change (PeerId/Weights/V/R/S already
//     exist) but is real verification-logic work in a repo this session has
//     no compiler for -- flagged as a design, not shipped as a silent fix.
//
// The x-seed-auth-timestamp/x-seed-auth-signature headers ARE attached below
// (same mechanism ListBuddySigned uses), so this call is forward-compatible
// with the seed someday enforcing them -- but as of this check, the JMNS
// ListBuddy handler (cmd/jmns-service/main.go) does not read those headers
// either, so today they are inert defense-in-depth, not an active gate.
//
// Net effect: calling this DOES functionally update peer.Weights on the seed
// today (the RPC is implemented, verification is a no-op so nothing rejects
// the call) -- Phase A4.2's practical goal is reachable now. What it does NOT
// yet have is real cryptographic proof that the caller was the sequencer, at
// the RPC's own auth layer. Until seedNodes closes that, only the network's
// trust in the operators running jmdn sequencer nodes stands behind this
// value, not the protocol.

// IsSequencer reports whether this node currently has a registered sequencer
// sign key (SetSequencerSignKey) -- i.e. whether it is the one node that will
// ever actually attempt PushReputationWeights or a signed ListBuddy call.
// A4-COMPLETION-LLD.md §3.4's ordering mechanism (ops/prometheus/
// reputation-divergence-alert.rules.yml) needs a way to label the
// sequencer's own metrics distinctly from buddy nodes' -- this is that seam,
// exported so package main can set a gauge from it without needing to know
// anything about currentSequencerSignKey internally.
func IsSequencer() bool {
	return currentSequencerSignKey() != nil
}

// PushReputationWeights pushes selection-remapped reputation scores (see
// reputation.SnapshotSelectionWeights / reputation.SelectionWeight) to the
// seed's peer.Weights field, one UpdatePeerWeights call per peer. Uses the
// registered sequencer sign key (SetSequencerSignKey) to attach the same
// sequencer-auth metadata ListBuddySigned uses; returns an error immediately
// (no calls made) if no sequencer key is registered -- i.e. this is a no-op
// on every node except the one actually running as sequencer, exactly like
// ListBuddyHeads.
//
// Returns the number of peers accepted, and a slice of per-peer errors for
// any that failed (a single peer's RPC error does not abort the batch).
func (c *Client) PushReputationWeights(ctx context.Context, weights map[string]float64) (accepted int, failures []error) {
	seqPriv := currentSequencerSignKey()
	if seqPriv == nil {
		return 0, []error{ErrNotSequencer}
	}
	if len(weights) == 0 {
		return 0, nil
	}

	signedCtx, err := sequencerAuthContextForMethod(ctx, seqPriv, reputationPushAuthMethod)
	if err != nil {
		return 0, []error{fmt.Errorf("sign reputation push request: %w", err)}
	}

	for peerID, w := range weights {
		req := &peerpb.UpdatePeerWeightsRequest{
			PeerId:  peerID,
			Weights: float32(w),
			// V, R, S intentionally empty -- see the PHASE A4.2 CAVEAT above.
		}
		if _, err := c.client.UpdatePeerWeights(signedCtx, req); err != nil {
			failures = append(failures, fmt.Errorf("peer %s: %w", peerID, err))
			continue
		}
		accepted++
	}
	return accepted, failures
}
