package rpc

import (
	"context"
	"encoding/json"
	"gossipnode/config/GRO"
	"gossipnode/gETH/Facade/Service"
	"gossipnode/gETH/Facade/Service/Types"
	"gossipnode/gETH/common"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
	"github.com/gorilla/websocket"
)

var FacadeLocalGRO interfaces.LocalGoroutineManagerInterface

type WSServer struct {
	h   *Handlers
	be  Service.Service
	upg websocket.Upgrader
}

func NewWSServer(h *Handlers, be Service.Service) *WSServer {
	return &WSServer{
		h: h, be: be,
		upg: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
	}
}

func (s *WSServer) Serve(addr string) error {
	return s.ServeWithContext(context.Background(), addr)
}

func (s *WSServer) ServeWithContext(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleWS)
	srv := &http.Server{Addr: addr, Handler: mux}
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
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
	conn, err := s.upg.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	subs := map[string]*sub{}
	var mu sync.Mutex

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}

		// Log incoming WebSocket message
		log.Printf("🔌 WS Message: %s", string(data))

		var req Request
		if err := json.Unmarshal(data, &req); err != nil {
			log.Printf("❌ WS Parse Error: %v", err)
			_ = conn.WriteJSON(RespErr(nil, -32700, "Parse error"))
			continue
		}

		if req.Method == "eth_subscribe" {
			// params: [subscriptionType, (optional) filter]
			if len(req.Params) < 1 {
				_ = conn.WriteJSON(RespErr(req.ID, -32602, "missing subscription type"))
				continue
			}
			typ, _ := req.Params[0].(string)
			sid := "0x" + time.Now().UTC().Format("150405.000000") // simple unique id; replace in prod

			switch typ {
			case "newHeads":
				ch, stop, err := s.be.SubscribeNewHeads(ctx)
				if err != nil {
					_ = conn.WriteJSON(RespErr(req.ID, -32000, err.Error()))
					continue
				}
				storeSub(subs, &mu, sid, stop)
				_ = conn.WriteJSON(RespOK(req.ID, sid))
				FacadeLocalGRO.Go(GRO.FacadeThread, func(ctx context.Context) error {
					forwardBlocks(conn, sid, ch)
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
					_ = conn.WriteJSON(RespErr(req.ID, -32000, err.Error()))
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
					_ = conn.WriteJSON(RespErr(req.ID, -32000, err.Error()))
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
		log.Printf("📤 WS Response: %s -> %+v", req.Method, resp)
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

func forwardBlocks(conn *websocket.Conn, sid string, ch <-chan *Types.Block) {
	for b := range ch {
		msg := subMsg{Jsonrpc: "2.0", Method: "eth_subscription"}
		msg.Params.Subscription = sid
		msg.Params.Result = marshalBlock(b, false)
		_ = conn.WriteJSON(msg)
	}
}
func forwardLogs(conn *websocket.Conn, sid string, ch <-chan Types.Log) {
	for l := range ch {
		msg := subMsg{Jsonrpc: "2.o", Method: "eth_subscription"}
		msg.Params.Subscription = sid
		msg.Params.Result = marshalLogs([]Types.Log{l})[0]
		_ = conn.WriteJSON(msg)
	}
}
func forwardPending(conn *websocket.Conn, sid string, ch <-chan string) {
	for h := range ch {
		msg := subMsg{Jsonrpc: "2.0", Method: "eth_subscription"}
		msg.Params.Subscription = sid
		msg.Params.Result = h
		_ = conn.WriteJSON(msg)
	}
}
