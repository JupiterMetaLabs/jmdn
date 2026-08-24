package messaging

// Timeout-certificate rejoin/catch-up RPC (M0/§7.1c) — 2026-08-24.
//
// Closes the scope note left in timeout_gossip.go's header: "a real network
// fetch-on-rejoin RPC ('ask a peer for the current height's latest
// certificate') is NOT built here." This file is that RPC. It adds no new
// trust and no new verification logic — LatestTimeoutCertificateFor
// (already built, already tested) answers the question on the server side,
// and AcceptIncomingTimeoutCertificate (already built, already tested)
// re-verifies whatever comes back on the client side before it can affect
// anything. What's new here is purely the wire plumbing connecting the two
// across a network hop, one request/response pair on its own protocol —
// same shape as entropy_reveal_push.go's RevealPush transport (a direct
// libp2p stream, JSON+newline), request/response instead of fire-and-forget.
//
// Gated OFF by default (JMDN_TIMEOUT_CERT_REJOIN) — flip together with
// JMDN_TIMEOUT_CERT_WIRING once both are fleet-tested; a node that doesn't
// have the underlying timeout-certificate wiring live has nothing useful to
// answer with anyway.
import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/rs/zerolog/log"

	"gossipnode/config"
)

// TimeoutCertRejoinEnabled gates this file's send AND receive sides. Default
// OFF, same discipline as TimeoutCertWiringEnabled — see that flag's doc for
// why a mixed fleet must not have this half-live.
var TimeoutCertRejoinEnabled = os.Getenv("JMDN_TIMEOUT_CERT_REJOIN") == "1"

// timeoutCertRejoinTimeout bounds one request end-to-end (dial + write +
// read). Short: a rejoining node asks several peers (see
// RequestLatestTimeoutCertificateFromPeers below), so one slow/unresponsive
// peer must not stall the others.
const timeoutCertRejoinTimeout = 5 * time.Second

// TimeoutCertRejoinRequest is the wire request: "what's your latest accepted
// TimeoutCertificate for this height?" No other fields needed — the server
// answers strictly from its own already-verified PeriodStore state, it does
// not trust anything else the requester claims.
type TimeoutCertRejoinRequest struct {
	Height uint64 `json:"height"`
}

// TimeoutCertRejoinResponse is the wire response. Found=false with a zero
// Cert is a normal, expected answer (the responder simply has never accepted
// a certificate for that height, most commonly because that height has never
// timed out) — not an error condition.
type TimeoutCertRejoinResponse struct {
	Found bool               `json:"found"`
	Cert  TimeoutCertificate `json:"cert,omitempty"`
}

// HandleTimeoutCertRejoinStream is the receive side, registered on
// config.TimeoutCertRejoinProtocol at node startup (see node/node.go).
//
// Answers strictly from this node's own local state
// (LatestTimeoutCertificateFor) — it never fetches, forwards, or otherwise
// trusts a third party's claim, so the worst a malicious requester can do is
// waste one short-lived stream, and the worst a malicious RESPONDER can do is
// answer wrong or not at all — which is exactly why the client side
// (RequestLatestTimeoutCertificateFromPeers) re-verifies via
// AcceptIncomingTimeoutCertificate and queries more than one peer, rather
// than trusting whichever one answers first.
func HandleTimeoutCertRejoinStream(s network.Stream) {
	defer s.Close()
	remote := s.Conn().RemotePeer()

	if !TimeoutCertRejoinEnabled {
		return
	}

	_ = s.SetReadDeadline(time.Now().Add(timeoutCertRejoinTimeout))
	line, err := bufio.NewReader(s).ReadString('\n')
	if err != nil && line == "" {
		log.Warn().Err(err).Str("from", remote.String()).
			Msg("timeout rejoin: request stream read failed")
		return
	}

	var req TimeoutCertRejoinRequest
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		log.Warn().Err(err).Str("from", remote.String()).
			Msg("timeout rejoin: request payload was not valid JSON")
		return
	}

	resp := TimeoutCertRejoinResponse{}
	if cert, ok := LatestTimeoutCertificateFor(req.Height); ok {
		resp.Found = true
		resp.Cert = cert
	}

	payload, err := json.Marshal(resp)
	if err != nil {
		log.Warn().Err(err).Uint64("height", req.Height).
			Msg("timeout rejoin: marshaling response failed")
		return
	}
	payload = append(payload, '\n')

	_ = s.SetWriteDeadline(time.Now().Add(timeoutCertRejoinTimeout))
	if _, err := s.Write(payload); err != nil {
		log.Warn().Err(err).Str("to", remote.String()).Uint64("height", req.Height).
			Msg("timeout rejoin: writing response failed")
		return
	}

	log.Debug().Str("to", remote.String()).Uint64("height", req.Height).Bool("found", resp.Found).
		Msg("timeout rejoin: answered request")
}

// requestLatestTimeoutCertificate asks exactly one peer for height's latest
// certificate over a fresh stream. Returns (cert, true, nil) only when the
// peer claims to have one — the caller (RequestLatestTimeoutCertificateFromPeers)
// is responsible for re-verifying it before trusting it for anything.
func requestLatestTimeoutCertificate(ctx context.Context, h host.Host, p peer.ID, height uint64) (TimeoutCertificate, bool, error) {
	payload, err := json.Marshal(TimeoutCertRejoinRequest{Height: height})
	if err != nil {
		return TimeoutCertificate{}, false, fmt.Errorf("timeout rejoin: marshaling request: %w", err)
	}
	payload = append(payload, '\n')

	stream, err := h.NewStream(ctx, p, config.TimeoutCertRejoinProtocol)
	if err != nil {
		return TimeoutCertificate{}, false, fmt.Errorf("timeout rejoin: opening stream to %s: %w", p, err)
	}
	defer stream.Close()

	deadline := time.Now().Add(timeoutCertRejoinTimeout)
	_ = stream.SetDeadline(deadline)

	if _, err := stream.Write(payload); err != nil {
		return TimeoutCertificate{}, false, fmt.Errorf("timeout rejoin: writing request to %s: %w", p, err)
	}

	line, err := bufio.NewReader(stream).ReadString('\n')
	if err != nil && line == "" {
		return TimeoutCertificate{}, false, fmt.Errorf("timeout rejoin: reading response from %s: %w", p, err)
	}

	var resp TimeoutCertRejoinResponse
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		return TimeoutCertificate{}, false, fmt.Errorf("timeout rejoin: response from %s was not valid JSON: %w", p, err)
	}
	if !resp.Found {
		return TimeoutCertificate{}, false, nil
	}
	return resp.Cert, true, nil
}

// RequestLatestTimeoutCertificateFromPeers is the rejoin/catch-up entry
// point: ask each of peers in turn for height's latest TimeoutCertificate,
// and accept the first one that VERIFIES — via AcceptIncomingTimeoutCertificate,
// the same acceptance path gossip already uses, so a fabricated or stale
// answer from a lying/lagging peer is rejected exactly as it would be over
// gossip, not trusted because it arrived over this RPC.
//
// Querying multiple peers (rather than one) means a single unresponsive or
// dishonest peer cannot stall a rejoining node — the caller (rejoin/catch-up
// orchestration, not built in this pass) is expected to pass a handful of
// already-known-good peers, e.g. the same peer set FastsyncV2 or the
// seednode-vetted reconcile path already uses.
//
// Returns (newPeriod, true, nil) on the first peer whose answer verifies and
// actually advances PeriodStore; (0, false, nil) — not an error — if every
// peer answered "not found" or nothing came back that verified, since
// "nobody has a certificate for this height" is the expected steady state
// for the overwhelming majority of heights (most rounds never time out).
func RequestLatestTimeoutCertificateFromPeers(h host.Host, peers []peer.ID, height uint64) (uint64, bool, error) {
	if !TimeoutCertRejoinEnabled {
		return 0, false, nil
	}
	var lastErr error
	for _, p := range peers {
		ctx, cancel := context.WithTimeout(context.Background(), timeoutCertRejoinTimeout)
		cert, found, err := requestLatestTimeoutCertificate(ctx, h, p, height)
		cancel()
		if err != nil {
			lastErr = err
			log.Warn().Err(err).Str("peer", p.String()).Uint64("height", height).
				Msg("timeout rejoin: request failed, trying next peer")
			continue
		}
		if !found {
			continue
		}
		newPeriod, accepted, err := AcceptIncomingTimeoutCertificate(cert)
		if err != nil {
			log.Warn().Err(err).Str("peer", p.String()).Uint64("height", height).
				Msg("timeout rejoin: peer's certificate failed verification — rejected, trying next peer")
			continue
		}
		if !accepted {
			// Verified but did not advance anything (e.g. we already hold an
			// equal-or-newer period for this height) — not an error, just
			// nothing to do.
			continue
		}
		log.Info().Str("peer", p.String()).Uint64("height", height).Uint64("new_period", newPeriod).
			Msg("timeout rejoin: adopted a verified certificate from a peer")
		return newPeriod, true, nil
	}
	if lastErr != nil {
		return 0, false, fmt.Errorf("timeout rejoin: no peer produced a usable certificate for height %d (last error: %w)", height, lastErr)
	}
	return 0, false, nil
}
