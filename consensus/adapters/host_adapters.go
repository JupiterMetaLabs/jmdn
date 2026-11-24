package adapters

import (
	"context"
	"crypto"
	"fmt"

	"github.com/JupiterMetaLabs/avc/interfaces"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// This file provides the remaining host adapters avc's engine needs, beyond the
// SeedNodeClient (seednode.go) and validator (perblock_validator.go). Each is a
// THIN bridge over a jmdn dependency, injected as a function/value so it can be
// unit-tested with fakes and so it pulls no heavy jmdn infra (libp2p host,
// pubsub, DB) into this package's own compile/test. jmdn's main.go supplies the
// real closures at startup, gated behind Features.AvcValidation.

// Compile-time interface assertions.
var (
	_ interfaces.PubSubPublisher    = (*PubSubAdapter)(nil)
	_ interfaces.NodeConfigProvider = (*NodeConfigAdapter)(nil)
	_ interfaces.PeerLister         = (*PeerListerAdapter)(nil)
	_ interfaces.VoteResultSink     = (*VoteSinkAdapter)(nil)
)

// PubSubAdapter implements interfaces.PubSubPublisher by delegating to jmdn's
// existing gossip publish (Pubsub/ + messaging/broadcast.go).
type PubSubAdapter struct {
	publish func(ctx context.Context, topic string, payload []byte) error
}

func NewPubSubAdapter(publish func(ctx context.Context, topic string, payload []byte) error) (*PubSubAdapter, error) {
	if publish == nil {
		return nil, fmt.Errorf("adapters.NewPubSubAdapter: nil publish func")
	}
	return &PubSubAdapter{publish: publish}, nil
}

func (a *PubSubAdapter) Publish(ctx context.Context, topic string, payload []byte) error {
	return a.publish(ctx, topic, payload)
}

// NodeConfigAdapter implements interfaces.NodeConfigProvider from the host
// node's libp2p identity (peer ID, listen addrs, private key).
type NodeConfigAdapter struct {
	peerID  peer.ID
	addrs   []multiaddr.Multiaddr
	privKey crypto.PrivateKey
}

func NewNodeConfigAdapter(peerID peer.ID, addrs []multiaddr.Multiaddr, privKey crypto.PrivateKey) *NodeConfigAdapter {
	return &NodeConfigAdapter{peerID: peerID, addrs: addrs, privKey: privKey}
}

func (a *NodeConfigAdapter) PeerID() peer.ID                        { return a.peerID }
func (a *NodeConfigAdapter) ListenAddresses() []multiaddr.Multiaddr { return a.addrs }
func (a *NodeConfigAdapter) PrivateKey() crypto.PrivateKey          { return a.privKey }

// PeerListerAdapter implements interfaces.PeerLister over jmdn's libp2p host
// (e.g. n.Host.Network().Peers() and self identity).
type PeerListerAdapter struct {
	listPeers func(ctx context.Context) ([]peer.ID, error)
	getPeer   func(ctx context.Context, id peer.ID) (interfaces.Node, error)
}

func NewPeerListerAdapter(
	listPeers func(ctx context.Context) ([]peer.ID, error),
	getPeer func(ctx context.Context, id peer.ID) (interfaces.Node, error),
) (*PeerListerAdapter, error) {
	if listPeers == nil || getPeer == nil {
		return nil, fmt.Errorf("adapters.NewPeerListerAdapter: listPeers and getPeer are both required")
	}
	return &PeerListerAdapter{listPeers: listPeers, getPeer: getPeer}, nil
}

func (a *PeerListerAdapter) ListPeers(ctx context.Context) ([]peer.ID, error) {
	return a.listPeers(ctx)
}

func (a *PeerListerAdapter) GetPeer(ctx context.Context, id peer.ID) (interfaces.Node, error) {
	return a.getPeer(ctx, id)
}

// VoteSinkAdapter implements interfaces.VoteResultSink by handing avc's verdict
// back to jmdn. In SHADOW mode the injected store only logs/metrics the result
// (it must NOT change the real vote); in ENFORCE mode it delivers into jmdn's
// actual vote path. The mode decision lives in the injected closure, not here.
type VoteSinkAdapter struct {
	store func(result interfaces.VoteResult) error
}

func NewVoteSinkAdapter(store func(result interfaces.VoteResult) error) (*VoteSinkAdapter, error) {
	if store == nil {
		return nil, fmt.Errorf("adapters.NewVoteSinkAdapter: nil store func")
	}
	return &VoteSinkAdapter{store: store}, nil
}

func (a *VoteSinkAdapter) StoreVoteResult(result interfaces.VoteResult) error {
	return a.store(result)
}
