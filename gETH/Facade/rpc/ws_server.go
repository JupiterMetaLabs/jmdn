package rpc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"gossipnode/config/GRO"
	"gossipnode/config/settings"
	"gossipnode/gETH/Facade/Service"
	"gossipnode/gETH/Facade/Service/Types"
	"gossipnode/gETH/common"
	"gossipnode/pkg/gatekeeper"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
	"github.com/gorilla/websocket"
)

// allowedWSOrigin restricts websocket upgrades to loopback origins to close the
// previous always-true CheckOrigin (which allowed cross-site WebSocket hijacking
// from any web page). Non-browser clients (curl, native ws libraries) send no
// Origin header and are still allowed.
//
// TODO: source an explicit allowlist from config (e.g. a future
// security.allowed_origins list) so operators can permit their own dashboards;
// until that field exists, default to loopback only.
func allowedWSOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser client, no Origin to check
	}
	return isLoopbackOrigin(origin)
}

// isLoopbackOrigin reports whether a browser Origin URL points at loopback.
// Shared by the WS upgrade check and the HTTP CORS middleware in this package.
func isLoopbackOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

var FacadeLocalGRO interfaces.LocalGoroutineManagerInterface

type WSServer struct {
	h   *Handlers
	be  Service.Service
	upg websocket.Upgrader

	// gate rate-limits upgrades and every message; nil disables limiting.
	gate  *gatekeeper.NetHTTPMiddleware
	conns *connLimiter
}

// Connection caps for the public WS endpoint. Each connection holds a reader
// goroutine plus one forwarder per subscription, so the count is bounded both
// overall and per client IP.
const (
	maxWSConns      = 1000
	maxWSConnsPerIP = 20
)

// connLimiter tracks open connections overall and per client IP.
type connLimiter struct {
	mu       sync.Mutex
	total    int
	perIP    map[string]int
	maxTotal int
	maxPerIP int
}

func newConnLimiter(maxTotal, maxPerIP int) *connLimiter {
	return &connLimiter{perIP: map[string]int{}, maxTotal: maxTotal, maxPerIP: maxPerIP}
}

// acquire reserves a slot for ip, returning false when a cap is reached.
func (l *connLimiter) acquire(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.total >= l.maxTotal || l.perIP[ip] >= l.maxPerIP {
		return false
	}
	l.total++
	l.perIP[ip]++
	return true
}

func (l *connLimiter) release(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.total--
	if l.perIP[ip] <= 1 {
		delete(l.perIP, ip)
	} else {
		l.perIP[ip]--
	}
}

func NewWSServer(h *Handlers, be Service.Service) *WSServer {
	return &WSServer{
		h: h, be: be,
		upg:   websocket.Upgrader{CheckOrigin: allowedWSOrigin},
		conns: newConnLimiter(maxWSConns, maxWSConnsPerIP),
	}
}

// clientIP resolves the caller's IP the same way the gatekeeper does, falling
// back to the socket address when no gatekeeper is configured.
func (s *WSServer) clientIP(r *http.Request) string {
	if s.gate != nil {
		return s.gate.ClientIP(r)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func (s *WSServer) Serve(addr string) error {
	return s.ServeWithContext(context.Background(), addr)
}

func (s *WSServer) ServeWithContext(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleWS)
	srv := &http.Server{
		Addr: addr,
		// Pre-upgrade hardening, mirroring http_server.go's own timeouts
		// (JMDN-V3-013): without these, this listener had no connection-level
		// bound at all -- a slowloris-style client could hold the handshake
		// open indefinitely.
		//
		// Verified safe for the post-upgrade WebSocket connection itself:
		// once handleWS's Upgrade call hijacks the connection (see
		// net/http.ResponseWriter's Hijacker interface), net/http stops
		// enforcing these server-level timeouts on it -- an http.Server with
		// a 1s ReadTimeout/WriteTimeout whose handler hijacks then sleeps 3s
		// past both before writing still completes that write successfully
		// (verified with a standalone net/http test, not shipped as part of
		// this codebase). These fields only bound the pre-upgrade HTTP
		// handshake handled by this http.Server.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MiB, matching http_server.go
	}
	// Same security policy as the HTTP JSON-RPC endpoint (eth_rpc): rate
	// limits, auth type and TLS. The middleware sees the upgrade request;
	// handleWS charges each subsequent message.
	tlsEnabled, gate, err := gatekeeper.ConfigureNetHTTPServer(srv, settings.ServiceEthRPC, &settings.Get().Security, nil)
	if err != nil {
		return fmt.Errorf("ws: configure security: %w", err)
	}
	s.gate = gate
	srv.Handler = gate.Wrap(settings.ServiceEthRPC, mux)

	errCh := make(chan error, 1)
	go func() {
		errCh <- gatekeeper.ServeHTTP(srv, tlsEnabled)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		return err
	}
}

// JMDN-V3-007: still-open hardening for the public WS endpoint — bound
// per-message size and per-connection subscription count. (Rate limiting,
// auth and TLS remain open; see DEVNET-VALIDATION notes / audit tracking.)
const (
	maxWSMessageBytes       = 1 << 20 // 1 MiB per WS frame/message
	maxSubscriptionsPerConn = 50      // eth_subscribe cap per connection
)

type sub struct {
	id   string
	stop func()
}

func (s *WSServer) handleWS(w http.ResponseWriter, r *http.Request) {
	if FacadeLocalGRO == nil {
		var err error
		FacadeLocalGRO, err = common.InitializeGRO(GRO.FacadeLocal)
		if err != nil {
			log.Printf("❌ Failed to initialize local gro: %v", err)
			return
		}
	}
	ip := s.clientIP(r)
	if !s.conns.acquire(ip) {
		http.Error(w, "too many websocket connections", http.StatusServiceUnavailable)
		return
	}
	defer s.conns.release(ip)

	rawConn, err := s.upg.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer rawConn.Close()
	// gorilla/websocket allows one concurrent writer; the read loop and every
	// subscription forwarder write to this connection, so serialize them.
	conn := &wsConn{Conn: rawConn}

	// JMDN-V3-007: bound per-message size so one connection cannot force
	// unbounded buffer growth reading a single frame.
	conn.SetReadLimit(maxWSMessageBytes)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	subs := map[string]*sub{}
	var mu sync.Mutex
	// On disconnect, stop every subscription this connection opened. Without
	// this, newHeads entries stayed registered (keeping the block poller and a
	// forwarder goroutine alive) for every connection that ever subscribed.
	defer func() {
		mu.Lock()
		for id, sb := range subs {
			sb.stop()
			delete(subs, id)
		}
		mu.Unlock()
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}

		// Every message counts against the client's eth_rpc rate limit, the
		// same budget as an HTTP JSON-RPC call.
		if !s.gate.AllowRequest(ctx, settings.ServiceEthRPC, ip) {
			_ = conn.WriteJSON(RespErr(nil, -32005, "rate limit exceeded"))
			continue
		}

		var req Request
		if err := json.Unmarshal(data, &req); err != nil {
			log.Printf("❌ WS Parse Error: %v", err)
			_ = conn.WriteJSON(RespErr(nil, -32700, "Parse error"))
			continue
		}

		if req.Method == "eth_subscribe" {
			mu.Lock()
			tooMany := len(subs) >= maxSubscriptionsPerConn
			mu.Unlock()
			if tooMany {
				_ = conn.WriteJSON(RespErr(req.ID, -32000, "too many subscriptions on this connection"))
				continue
			}

			// params: [subscriptionType, (optional) filter]
			if len(req.Params) < 1 {
				_ = conn.WriteJSON(RespErr(req.ID, -32602, "missing subscription type"))
				continue
			}
			typ, _ := req.Params[0].(string)
			sid := newSubscriptionID()

			switch typ {
			case "newHeads":
				ch, stop, err := s.be.SubscribeNewHeads(ctx)
				if err != nil {
					_ = conn.WriteJSON(RespErr(req.ID, -32000, publicErrorMessage(ctx, err)))
					continue
				}
				storeSub(subs, &mu, sid, stop)
				_ = conn.WriteJSON(RespOK(req.ID, sid))
				chainID := s.be.GetChainIDValue()
				FacadeLocalGRO.Go(GRO.FacadeThread, func(ctx context.Context) error {
					forwardBlocks(conn, sid, ch, chainID)
					return nil
				})

			case "logs":
				var q Types.FilterQuery
				if len(req.Params) > 1 {
					if qq, err := toFilterQuery(req.Params[1]); err == nil {
						q = *qq
					}
				}
				ch, stop, err := s.be.SubscribeLogs(ctx, &q)
				if err != nil {
					_ = conn.WriteJSON(RespErr(req.ID, -32000, publicErrorMessage(ctx, err)))
					continue
				}
				storeSub(subs, &mu, sid, stop)
				_ = conn.WriteJSON(RespOK(req.ID, sid))
				FacadeLocalGRO.Go(GRO.FacadeThread, func(ctx context.Context) error {
					forwardLogs(conn, sid, ch)
					return nil
				})

			case "newPendingTransactions":
				ch, stop, err := s.be.SubscribePendingTxs(ctx)
				if err != nil {
					_ = conn.WriteJSON(RespErr(req.ID, -32000, publicErrorMessage(ctx, err)))
					continue
				}
				storeSub(subs, &mu, sid, stop)
				_ = conn.WriteJSON(RespOK(req.ID, sid))
				FacadeLocalGRO.Go(GRO.FacadeThread, func(ctx context.Context) error {
					forwardPending(conn, sid, ch)
					return nil
				})

			default:
				_ = conn.WriteJSON(RespErr(req.ID, -32602, "unsupported subscription"))
			}
			continue
		}

		if req.Method == "eth_unsubscribe" {
			if len(req.Params) < 1 {
				_ = conn.WriteJSON(RespErr(req.ID, -32602, "missing id"))
				continue
			}
			id, _ := req.Params[0].(string)
			mu.Lock()
			if s, ok := subs[id]; ok {
				s.stop()
				delete(subs, id)
			}
			mu.Unlock()
			_ = conn.WriteJSON(RespOK(req.ID, true))
			continue
		}

		// regular RPC via WS
		resp, _ := s.h.Handle(ctx, req)
		_ = conn.WriteJSON(resp)
	}
}

func storeSub(m map[string]*sub, mu *sync.Mutex, id string, stop func()) {
	mu.Lock()
	defer mu.Unlock()
	m[id] = &sub{id: id, stop: stop}
}

type subMsg struct {
	Jsonrpc string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  struct {
		Subscription string `json:"subscription"`
		Result       any    `json:"result"`
	} `json:"params"`
}

func forwardBlocks(conn *wsConn, sid string, ch <-chan *Types.Block, chainID *big.Int) {
	for b := range ch {
		msg := subMsg{Jsonrpc: "2.0", Method: "eth_subscription"}
		msg.Params.Subscription = sid
		msg.Params.Result = marshalBlock(b, false, chainID)
		_ = conn.WriteJSON(msg)
	}
}
func forwardLogs(conn *wsConn, sid string, ch <-chan Types.Log) {
	for l := range ch {
		msg := subMsg{Jsonrpc: "2.0", Method: "eth_subscription"}
		msg.Params.Subscription = sid
		msg.Params.Result = marshalLogs([]Types.Log{l})[0]
		_ = conn.WriteJSON(msg)
	}
}
func forwardPending(conn *wsConn, sid string, ch <-chan string) {
	for h := range ch {
		msg := subMsg{Jsonrpc: "2.0", Method: "eth_subscription"}
		msg.Params.Subscription = sid
		msg.Params.Result = h
		_ = conn.WriteJSON(msg)
	}
}

// wsConn serializes writes to a websocket connection (gorilla/websocket
// supports a single concurrent writer).
type wsConn struct {
	*websocket.Conn
	wmu sync.Mutex
}

// WriteJSON writes v as one message; safe for concurrent use.
func (c *wsConn) WriteJSON(v any) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.Conn.WriteJSON(v)
}

// newSubscriptionID returns a random 16-byte hex subscription ID, the format
// Ethereum clients use.
func newSubscriptionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("0x%032x", subscriptionFallbackSeq.Add(1))
	}
	return "0x" + hex.EncodeToString(b[:])
}

var subscriptionFallbackSeq atomic.Uint64
