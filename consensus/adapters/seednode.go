package adapters

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/JupiterMetaLabs/avc/interfaces"
	"github.com/libp2p/go-libp2p/core/peer"

	"gossipnode/seednode"
	peerpb "gossipnode/seednode/proto"
)

// Compile-time assertion that the adapter satisfies avc's contract.
var _ interfaces.SeedNodeClient = (*SeedNodeAdapter)(nil)

// SeedNodeAdapter implements avc's interfaces.SeedNodeClient by joining jmdn's
// two authoritative sources into the peer records avc needs:
//
//   - the AUTHENTICATED committee (peer_id -> bls_pub), from a seed-signed,
//     authority-verified, epoch-fresh CommitteeSnapshot — supplied via
//     eligibleFn, which MUST be jmdn's Client.CommitteeEligibility /
//     CommitteeEligibilityAuto. That is the SAME verified source
//     messaging.keyAuthorized uses (VerifyCommitteeSnapshot against the pinned
//     authority key + epoch-freshness -> BLSPubByPeer); and
//   - each member's multiaddrs, from the seed peer records (Client.GetPeers).
//
// The BLS public key avc uses to authorize a committee member's votes therefore
// comes ONLY from the verified snapshot — NEVER from the self-asserted
// SignedPeerRecord.BlsPub, and NEVER from local key storage (which holds only
// this node's own key and so cannot yield OTHER members' keys). This is the
// correction over the naive "populate BLSPubKey from GetPeers/local config"
// design: it would either authenticate against unverified self-asserted keys or
// fail 100% of the time.
//
// FAIL-CLOSED throughout: an unavailable/empty verified committee yields no
// peers, so avc's VRF selection and vote tally both see an empty committee and
// authorize nobody, rather than falling back to unauthenticated votes.
type SeedNodeAdapter struct {
	client     *seednode.Client
	eligibleFn func(epoch uint64, pinned bool) (map[string]string, error)
}

// NewSeedNodeAdapter builds the adapter.
//
// eligibleFn MUST be a VERIFIED eligibility source — pass
// client.CommitteeEligibility(pinnedAuthorityHex, epochSeconds) or
// client.CommitteeEligibilityAuto(...). Do NOT pass a bare
// FetchCommitteeSnapshot wrapper: that fetches without verifying the authority
// signature or epoch freshness, which would defeat the entire point of Fix #2.
// A nil eligibleFn is rejected so the adapter can never silently degrade to
// "no authorization".
func NewSeedNodeAdapter(client *seednode.Client, eligibleFn func(epoch uint64, pinned bool) (map[string]string, error)) (*SeedNodeAdapter, error) {
	if client == nil {
		return nil, fmt.Errorf("avc seednode adapter: nil seednode client")
	}
	if eligibleFn == nil {
		return nil, fmt.Errorf("avc seednode adapter: nil eligibility source (must be a VERIFIED committee source)")
	}
	return &SeedNodeAdapter{client: client, eligibleFn: eligibleFn}, nil
}

// GetPeers returns the authenticated committee: each member's peer ID,
// multiaddrs, and authority-verified BLS public key.
//
// FAIL-CLOSED: if the verified eligibility source errors or is empty, an error
// is returned and no peers are produced, so avc authorizes nobody. Address
// resolution is best-effort — a member missing a multiaddr is still returned
// with its verified key, because addresses are a dial/liveness concern, not an
// authorization one.
func (a *SeedNodeAdapter) GetPeers(ctx context.Context) ([]interfaces.Node, error) {
	// (0, false) = the CURRENT authenticated committee. This adapter feeds avc's
	// own VRF selection, a separate consumer from messaging's W1 pinned path; it
	// wants "who is the committee now", not a specific past epoch.
	eligible, err := a.eligibleFn(0, false)
	if err != nil {
		return nil, fmt.Errorf("avc seednode adapter: verified committee unavailable (fail-closed): %w", err)
	}
	if len(eligible) == 0 {
		return nil, fmt.Errorf("avc seednode adapter: verified committee is empty (fail-closed)")
	}

	// Multiaddr lookup from the seed peer records (best-effort).
	addrByPeer := make(map[string][]string)
	if records, rerr := a.client.GetPeers(0, peerpb.PeerStatus_PEER_STATUS_ACTIVE); rerr == nil {
		for _, r := range records {
			addrByPeer[r.GetPeerId()] = r.GetMultiaddrs()
		}
	}

	nodes := make([]interfaces.Node, 0, len(eligible))
	for peerIDStr, blsHex := range eligible {
		pid, derr := peer.Decode(peerIDStr)
		if derr != nil {
			continue // malformed peer id in snapshot — skip this member, don't fail the whole set
		}
		blsKey, herr := hex.DecodeString(blsHex)
		if herr != nil || len(blsKey) == 0 {
			continue // a member without a usable key cannot be authenticated -> omit (fail-closed)
		}
		nodes = append(nodes, interfaces.Node{
			ID:        pid,
			Addrs:     addrByPeer[peerIDStr],
			BLSPubKey: blsKey,
		})
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("avc seednode adapter: no committee member had a decodable id+key (fail-closed)")
	}
	return nodes, nil
}

// Bootstrap is a no-op: the seed connection is owned by the jmdn seednode.Client
// lifecycle (established at seednode.NewClient), not by this adapter.
func (a *SeedNodeAdapter) Bootstrap(ctx context.Context) error { return nil }
