package thebesyncserve

// Server side: read-only libp2p stream handlers for the head handshake and the
// bounded GetBlocks range serve, backed by a host-supplied BlockProvider. These
// never mutate state — they answer explicit peer requests.

import (
	"encoding/json"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
)

// Server holds the BlockProvider and exposes libp2p stream handlers. Register its
// methods on the host:
//
//	srv := &thebesyncserve.Server{Provider: myProvider}
//	h.SetStreamHandler(thebesyncserve.HeadProtocol, srv.HeadHandler)
//	h.SetStreamHandler(thebesyncserve.GetBlocksProtocol, srv.GetBlocksHandler)
type Server struct {
	Provider BlockProvider

	limits serveLimits // zero value is ready to use
}

// HeadHandler answers a HeadRequest with this node's chain tip. The whole
// exchange is bounded by serveReadTimeout; the response is a few bytes.
func (s *Server) HeadHandler(stream network.Stream) {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(serveReadTimeout))
	drainLine(stream, maxHeadReqBytes) // v1 request carries nothing we need
	writeJSONLine(stream, s.buildHead())
}

func (s *Server) buildHead() HeadResponse {
	height, found, err := s.Provider.LatestHeight()
	if err != nil {
		return HeadResponse{Error: "tip unavailable"}
	}
	if !found {
		return HeadResponse{Error: "no blocks"}
	}
	resp := HeadResponse{Height: height}
	if hash, herr := s.Provider.TipHash(height); herr == nil {
		resp.TipBlockHash = hash
	}
	return resp
}

// GetBlocksHandler answers a GetBlocksRequest with the opaque blocks in the
// clamped range. Read-only. The request read, the wait for a serve slot and the
// response write are each bounded, so a peer that stalls cannot hold a serve
// slot (or this goroutine) indefinitely.
func (s *Server) GetBlocksHandler(stream network.Stream) {
	defer stream.Close()

	_ = stream.SetReadDeadline(time.Now().Add(serveReadTimeout))
	reqBytes, err := readLine(stream, maxGetBlocksReqBytes)
	if err != nil || len(reqBytes) == 0 {
		writeJSONLine(stream, GetBlocksResponse{Error: "bad request"})
		return
	}
	var req GetBlocksRequest
	if err := json.Unmarshal(reqBytes, &req); err != nil {
		writeJSONLine(stream, GetBlocksResponse{Error: "bad request"})
		return
	}
	release, ok := s.limits.acquire(stream.Conn().RemotePeer())
	if !ok {
		writeJSONLine(stream, GetBlocksResponse{Error: errBusy})
		return
	}
	defer release()

	b, err := json.Marshal(s.buildGetBlocks(req))
	if err != nil {
		return
	}
	_ = writeResponse(stream, b)
}

// buildGetBlocks loads the contiguous opaque blocks in [From..clamped-To]. It
// stops at the first not-found (tip reached) or read error and returns the
// contiguous prefix so the client can retry the remainder.
func (s *Server) buildGetBlocks(req GetBlocksRequest) GetBlocksResponse {
	if req.To < req.From {
		return GetBlocksResponse{Error: "invalid range"}
	}
	to := clampedTo(req.From, req.To)

	blocks := make([][]byte, 0, to-req.From+1)
	budget := serveByteBudget()
	var size int64
	for n := req.From; n <= to; n++ {
		raw, found, err := s.Provider.RawBlock(n)
		if err != nil || !found {
			break // tip reached or read error — return the contiguous prefix
		}
		// Stop before the response would outgrow what a client accepts; the
		// client continues from the last block it received. Always send at
		// least one block.
		if len(blocks) > 0 && size+int64(len(raw)) > budget {
			break
		}
		blocks = append(blocks, raw)
		size += int64(len(raw))
	}
	return GetBlocksResponse{Blocks: blocks}
}

// clampedTo returns the inclusive upper bound after applying the
// MaxBlocksPerRequest length cap. Written as (to-from >= Max) rather than
// (to-from+1 > Max) so a malicious to (e.g. MaxUint64) cannot overflow the +1 and
// bypass the cap. Caller must guarantee to >= from.
func clampedTo(from, to uint64) uint64 {
	if to-from >= MaxBlocksPerRequest {
		return from + MaxBlocksPerRequest - 1
	}
	return to
}
