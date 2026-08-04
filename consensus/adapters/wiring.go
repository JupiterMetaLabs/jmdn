package adapters

import (
	"fmt"

	avccfg "github.com/JupiterMetaLabs/avc/config"
	"github.com/JupiterMetaLabs/avc/engine"
	"github.com/JupiterMetaLabs/avc/interfaces"

	blssigner "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config/settings"
)

// RESOLVED BLOCKER (kept for history): importing avc/engine used to pull
// github.com/JupiterMetaLabs/avc/bft/proto, which registers a protobuf file
// "bft.proto" — the same name jmdn's gossipnode/AVC/BFT/proto registers — and
// panicked at init ("proto: file bft.proto is already registered"). That link is
// now severed: the live avc path (engine -> sequencer/trigger) takes its quorum
// math from the proto-free github.com/JupiterMetaLabs/avc/quorum package and no
// longer imports package bft, so avc/engine no longer transitively links
// bft/proto. BuildEngine below can therefore be constructed inside the jmdn
// binary. (A full A1 proto regen — unique namespaces for avc's own bft.proto —
// is still worthwhile if the dormant bft gRPC engine is ever wired, but is no
// longer required for this integration.)

// This file is the single assembly point where the avc consensus module is
// configured to MATCH jmdn's live consensus, closing three parity-audit
// divergences at once:
//
//   - #2 vote domain: forces VoteDomainVersion=v3 (jmdn signs v3 by default).
//   - #4 chain id:    binds avc's v3 verification to jmdn's OWN runtime
//                     DomainChainID() — never a hardcoded constant. jmdn's
//                     DefaultDomainChainID (8000800) is a Load()-not-called
//                     FALLBACK; production signs with the loaded chain id
//                     (jmdn_default.yaml = 7000700). Sourcing from
//                     blssigner.DomainChainID() guarantees signer and avc
//                     verifier can never drift, in any environment.
//   - #14 committee size: aligns avc's committee size (and MaxMainPeers) with
//                     jmdn's MaxValidators, so Fix #1's ceil(2n/3) quorum is
//                     computed over the SAME committee jmdn's certificate path
//                     uses — not avc's old default of 4.
//
// Nothing here starts consensus. It only assembles a correctly-configured
// engine; activation stays gated behind Features.AvcValidation (Enabled +
// Mode + Network.Environment=="testnet"), per the shadow-first rollout.

// BuildAVCConfig assembles an avc config bound to the given parameters and
// validates it. It is a PURE function (no global state) so it is unit-testable
// and so the caller controls every value.
//
// FAIL-CLOSED: Validate() rejects VoteDomainV3 with chainID==0, so a caller
// that forgets to supply a real chain id gets a loud startup error rather than
// a node that signs/verifies v3 against chain 0 and silently never agrees with
// peers. committeeSize<=0 is likewise rejected.
func BuildAVCConfig(chainID uint64, committeeSize int, networkSalt, seedNodeURL string) (avccfg.Config, error) {
	if committeeSize <= 0 {
		return avccfg.Config{}, fmt.Errorf("adapters.BuildAVCConfig: committeeSize must be positive, got %d", committeeSize)
	}
	c := avccfg.DefaultConfig()
	c.Network.ChainID = chainID
	if networkSalt != "" {
		c.Network.NetworkSalt = networkSalt
	}
	if seedNodeURL != "" {
		c.Network.SeedNode = seedNodeURL
	}
	c.Consensus.VoteDomainVersion = avccfg.VoteDomainV3
	// Keep CommitteeSize and MaxMainPeers in agreement — CLAUDE.md requires
	// config.MaxMainPeers and consensus.max_validators to match, and Fix #1's
	// quorum is sized over CommitteeSize.
	c.Consensus.CommitteeSize = committeeSize
	c.Consensus.MaxMainPeers = committeeSize
	if err := c.Validate(); err != nil {
		return avccfg.Config{}, fmt.Errorf("adapters.BuildAVCConfig: invalid avc config: %w", err)
	}
	return c, nil
}

// BuildAVCConfigFromSettings is the production convenience: it sources the chain
// id from jmdn's authoritative runtime accessor (blssigner.DomainChainID) and
// the committee size from jmdn settings, then delegates to BuildAVCConfig.
//
// MUST be called AFTER settings.Load() — otherwise DomainChainID() returns the
// 8000800 fallback, which is NOT jmdn's production chain id. networkSalt is
// passed explicitly by the caller because it must match jmdn's VRF salt exactly
// (a mismatch selects a different committee); pass "" only to accept avc's
// default salt in a single-network dev setup.
func BuildAVCConfigFromSettings(networkSalt string) (avccfg.Config, error) {
	cfg := settings.Get()
	committeeSize := cfg.Consensus.MaxValidators
	if committeeSize <= 0 {
		// MaxValidators==0 means "cap disabled" in jmdn; avc needs a concrete
		// committee size, so fall back to the documented default rather than 0.
		committeeSize = 13
	}
	return BuildAVCConfig(blssigner.DomainChainID(), committeeSize, networkSalt, cfg.Network.SeedNode)
}

// BuildEngine assembles a ready-to-Start avc engine from the host adapters.
//
// seedClient MUST surface AUTHORITY-VERIFIED committee BLS keys (use
// *SeedNodeAdapter) — that is what makes Fix #2's committee authorization real.
// validationDepth selects the pre-vote check: interfaces.DepthStructural
// (hash/merkle only) or interfaces.DepthFull.
//
// DEPTHFULL CAVEAT (#5 stateful checks): DepthFull requires a validator that
// runs stateful (balance/nonce) checks against a PER-BLOCK account cache loaded
// from ImmuDB. StatefulChecker (stateful_checker.go) implements the checks, but
// its cache must be (re)loaded per block, which a single injected BlockValidator
// does not do on its own. Passing DepthFull with a structural-only validator
// silently runs only structural checks; passing it with a FullValidator whose
// cache is not per-block-loaded fails closed (rejects). Real stateful validation
// therefore still needs a per-block validator/cache lifecycle (the
// runFullValidatorAgainstDB pattern in shadow.go). Pass DepthStructural until
// that lifecycle is wired.
//
// Nothing here starts consensus; activation stays gated behind
// Features.AvcValidation (Enabled + Mode + Network.Environment=="testnet").
func BuildEngine(
	cfg avccfg.Config,
	pubSub interfaces.PubSubPublisher,
	node interfaces.NodeConfigProvider,
	peerLister interfaces.PeerLister,
	seedClient interfaces.SeedNodeClient,
	sink interfaces.VoteResultSink,
	validator interfaces.BlockValidator,
	validationDepth interfaces.ValidationDepth,
) (*engine.Engine, error) {
	if seedClient == nil {
		return nil, fmt.Errorf("adapters.BuildEngine: nil seedClient (need verified-committee source for Fix #2 authorization)")
	}
	eng := engine.New(cfg, pubSub, node, peerLister, seedClient, sink, validator)
	eng.SetValidationDepth(validationDepth)
	return eng, nil
}
