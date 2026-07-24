package seednode

import (
	"context"
	"fmt"
	"time"

	"gossipnode/seednode/committee"
	peerpb "gossipnode/seednode/proto"
)

// FetchCommitteeSnapshot calls GetCommitteeSnapshot(epoch) on the seed
// (epoch 0 = current) and converts the response into the committee mirror
// struct. It does NOT verify — callers MUST run committee.VerifyCommitteeSnapshot
// against the pinned authority key before trusting the result.
func (c *Client) FetchCommitteeSnapshot(ctx context.Context, epoch uint64) (*committee.CommitteeSnapshot, error) {
	resp, err := c.client.GetCommitteeSnapshot(ctx, &peerpb.GetCommitteeSnapshotRequest{Epoch: epoch})
	if err != nil {
		return nil, fmt.Errorf("get committee snapshot: %w", err)
	}
	if resp == nil || resp.Snapshot == nil {
		return nil, fmt.Errorf("empty committee snapshot response")
	}
	s := resp.Snapshot
	entries := make([]committee.CommitteeEntry, 0, len(s.Entries))
	for _, e := range s.Entries {
		entries = append(entries, committee.CommitteeEntry{PeerID: e.PeerId, BLSPub: e.BlsPub})
	}
	return &committee.CommitteeSnapshot{
		Epoch:           s.Epoch,
		Entries:         entries,
		Seed:            s.Seed,
		AuthorityPubHex: s.AuthorityPubkey,
		Signature:       s.Signature,
	}, nil
}

// CommitteeEligibility returns a fail-closed eligibility source for
// messaging.SetCommitteeEligibilitySource (P1). Each call fetches the current
// epoch's committee snapshot, verifies its authority signature against the
// PINNED authority key, and returns the eligible peer_id set. Because the
// returned set IS the committee, VerifyCertificate then counts only snapshot
// members — enforcing committee ⊆ snapshot at the tally.
//
// Fail-closed: an unset pin, a fetch error, a signature/pin-mismatch, or an
// empty snapshot all return an error (never "allow all"), so a node with no
// authenticated committee refuses consensus participation.
func (c *Client) CommitteeEligibility(pinnedAuthorityHex string) func() (map[string]string, error) {
	return func() (map[string]string, error) {
		if pinnedAuthorityHex == "" {
			return nil, fmt.Errorf("committee source disabled: no pinned seed authority key (fail closed)")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		snap, err := c.FetchCommitteeSnapshot(ctx, 0) // 0 = current epoch
		if err != nil {
			return nil, err
		}
		if err := committee.VerifyCommitteeSnapshot(snap, pinnedAuthorityHex); err != nil {
			return nil, fmt.Errorf("committee snapshot rejected: %w", err)
		}
		// peer_id -> authenticated bls_pub, so the verifier can enforce the
		// peer_id↔bls_pub binding (M1), not just membership.
		return snap.BLSPubByPeer(), nil
	}
}
