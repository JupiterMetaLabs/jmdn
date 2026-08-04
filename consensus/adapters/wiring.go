package adapters

import (
	"fmt"

	avccfg "github.com/JupiterMetaLabs/avc/config"

	blssigner "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config/settings"
)

// BLOCKER for runtime engine construction (BuildEngine, intentionally NOT in
// this file yet): importing avc/engine pulls github.com/JupiterMetaLabs/avc/bft/proto,
// which registers a protobuf file named "bft.proto" — the SAME file name jmdn's
// own gossipnode/AVC/BFT/proto already registers. Two packages registering the
// same proto path in one binary panics at init:
//
//	proto: file "bft.proto" is already registered
//	  previously from: "github.com/JupiterMetaLabs/avc/bft/proto"
//	  currently from:  "gossipnode/AVC/BFT/proto"
//
// This is a LINK/INIT-time collision, so no runtime feature flag can gate around
// it: the moment a jmdn binary imports avc/engine, it crashes on startup. It
// must be resolved first by giving avc's bft proto a UNIQUE file path + proto
// package + go_package and regenerating (parity plan A1 "regenerate protobuf
// from one canonical source"). Until then, avc's config can be assembled here
// (BuildAVCConfig, below — no proto import) but the engine cannot be constructed
// inside the jmdn binary.

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

// NOTE: BuildEngine (constructing *engine.Engine with the host adapters and
// SetValidationDepth for #5) is deliberately omitted until the bft.proto
// namespace collision above is resolved — see the package-level blocker comment.
// Once avc's bft proto is regenerated under a unique namespace, BuildEngine
// becomes a small function: engine.New(cfg, pubSub, node, peerLister,
// seedClient, sink, validator) + eng.SetValidationDepth(depth), gated behind
// Features.AvcValidation. It is scoped out here so this package still links and
// tests cleanly inside the jmdn binary today.
