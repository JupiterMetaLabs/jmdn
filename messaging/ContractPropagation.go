package messaging

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"gossipnode/SmartContract"
	"gossipnode/config"
	"gossipnode/config/GRO"
	GROHelper "gossipnode/messaging/common"
	"gossipnode/metrics"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/local"
	"github.com/JupiterMetaLabs/ion"
	"github.com/bits-and-blooms/bloom/v3"
	"github.com/ethereum/go-ethereum/common"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
)

// ContractMessage is the gossip payload for a confirmed contract deployment.
// Produced by the sequencer after BFT consensus; received by all other nodes.
type ContractMessage struct {
	ID              string         `json:"id"`
	Sender          string         `json:"sender"`
	Timestamp       int64          `json:"timestamp"`
	Type            string         `json:"type"` // "contract_deployed"
	Hops            int            `json:"hops"`
	ContractAddress common.Address `json:"contract_address"`
	Deployer        common.Address `json:"deployer"`
	TxHash          common.Hash    `json:"tx_hash"`
	BlockNumber     uint64         `json:"block_number"`
	GasUsed         uint64         `json:"gas_used"`
	// ABI is populated when available from the sequencer's registry.
	// Receivers must handle an empty string gracefully (bytecode is always
	// available via EVM execution; only the registry/ABI layer may be absent).
	ABI string `json:"abi,omitempty"`
}

var (
	contractFilter     *bloom.BloomFilter
	contractFilterOnce sync.Once
	contractFilterMu   sync.RWMutex

	// contractGROOnce ensures ContractLocalGRO is initialised exactly once,
	// regardless of how many concurrent goroutines reach the lazy-init path.
	contractGROOnce    sync.Once
	contractGROInitErr error
)

// InitContractPropagation initialises the Bloom filter used for deduplication.
// Must be called once at node startup before any contract propagation streams arrive.
func InitContractPropagation() error {
	contractFilterOnce.Do(func() {
		contractFilter = bloom.NewWithEstimates(100_000, 0.01)
	})
	// Also pre-initialise the GRO so the first incoming stream doesn't race.
	if err := ensureContractLocalGRO(); err != nil {
		return err
	}
	contractLogger().Info(context.Background(), "Contract propagation system initialised")
	return nil
}

// ensureContractLocalGRO initialises ContractLocalGRO exactly once.
// Safe for concurrent callers.
func ensureContractLocalGRO() error {
	contractGROOnce.Do(func() {
		ContractLocalGRO, contractGROInitErr = GROHelper.InitializeGRO(GRO.ContractPropagationLocal)
	})
	return contractGROInitErr
}

func isContractMessageProcessed(id string) bool {
	contractFilterMu.RLock()
	defer contractFilterMu.RUnlock()
	if contractFilter == nil {
		return false
	}
	return contractFilter.Test([]byte(id))
}

func markContractMessageProcessed(id string) {
	contractFilterMu.Lock()
	defer contractFilterMu.Unlock()
	if contractFilter == nil {
		contractFilter = bloom.NewWithEstimates(100_000, 0.01)
	}
	contractFilter.Add([]byte(id))
}

// NOTE(F4 merge): PropagateContractDeployments (push propagation) was retired
// with main's per-tx-atomic Processing.go rewrite — processTransaction no
// longer returns ContractDeploymentInfo. Nodes get contract code by executing
// the deploy transaction in a certified block (cas.KV()); the pull-on-demand
// client is gone too (see pullStreamTimeout).

// HandleContractStream is the libp2p stream handler registered on all nodes for
// the ContractPropagationProtocol.  It deduplicates via Bloom filter, writes the
// contract metadata to the local registry, and hop-limits re-forwarding.
func HandleContractStream(stream network.Stream) {
	if err := ensureContractLocalGRO(); err != nil {
		contractLogger().Error(context.Background(), "ContractPropagation: failed to initialize LocalGRO", err)
		stream.Close()
		return
	}
	defer stream.Close()

	remotePeer := stream.Conn().RemotePeer().String()
	metrics.MessagesReceivedCounter.WithLabelValues("contract", metrics.PeerLabel(remotePeer)).Inc()

	// Cap incoming message size to prevent an OOM attack from a malicious peer.
	const maxPushBytes = 4 * 1024 * 1024 // 4 MB — well above any legitimate ContractMessage
	reader := bufio.NewReader(io.LimitReader(stream, maxPushBytes))
	msgBytes, err := reader.ReadBytes('\n')
	if err != nil && err != io.EOF {
		contractLogger().Error(context.Background(), "ContractPropagation: error reading stream", err,
			ion.String("peer", remotePeer))
		return
	}
	if len(msgBytes) == 0 {
		return
	}

	var msg ContractMessage
	if err := json.Unmarshal(msgBytes, &msg); err != nil {
		contractLogger().Error(context.Background(), "ContractPropagation: failed to unmarshal message", err)
		return
	}

	if isContractMessageProcessed(msg.ID) {
		contractLogger().Debug(context.Background(), "ContractPropagation: duplicate message, dropping",
			ion.String("msg_id", msg.ID))
		return
	}
	markContractMessageProcessed(msg.ID)

	// Write to local registry.
	storeContractFromGossip(msg)

	// Re-forward if within hop limit.
	if msg.Hops < config.MaxContractHops {
		msg.Hops++
		if h := getHostInstance(); h != nil {
			ContractLocalGRO.Go(GRO.ContractPropagationStreamThread, func(ctx context.Context) error {
				forwardContract(h, msg)
				return nil
			})
		} else {
			contractLogger().Error(context.Background(), "ContractPropagation: host instance unavailable for forwarding",
				errors.New("getHostInstance returned nil"))
		}
	} else {
		contractLogger().Debug(context.Background(), "ContractPropagation: max hops reached, not re-forwarding",
			ion.String("msg_id", msg.ID),
			ion.Int("hops", msg.Hops))
	}
}

// storeContractFromGossip persists a received ContractMessage to the local registry.
func storeContractFromGossip(msg ContractMessage) {
	if ContractLocalGRO == nil {
		contractLogger().Error(context.Background(), "ContractPropagation: storeContractFromGossip called before GRO initialised",
			errors.New("ContractLocalGRO is nil"),
			ion.String("contract", msg.ContractAddress.Hex()))
		return
	}
	ContractLocalGRO.Go(GRO.ContractStoreThread, func(ctx context.Context) error {
		err := SmartContract.RegisterContractFromGossip(
			ctx,
			msg.ContractAddress,
			msg.Deployer,
			msg.TxHash,
			msg.BlockNumber,
			msg.ABI,
		)
		if err != nil {
			contractLogger().Error(ctx, "ContractPropagation: failed to register contract from gossip", err,
				ion.String("contract", msg.ContractAddress.Hex()))
			return err
		}
		contractLogger().Info(ctx, "ContractPropagation: contract registered from gossip",
			ion.String("contract", msg.ContractAddress.Hex()),
			ion.Uint64("block", msg.BlockNumber))
		return nil
	})
}

// forwardContract re-sends a ContractMessage to all currently-connected peers
// (excluding the original sender).
func forwardContract(h host.Host, msg ContractMessage) {
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		contractLogger().Error(context.Background(), "ContractPropagation: failed to marshal message for forwarding", err)
		return
	}
	msgBytes = append(msgBytes, '\n')

	peers := h.Network().Peers()

	wg, err := ContractLocalGRO.NewFunctionWaitGroup(context.Background(), GRO.ContractForwardWG)
	if err != nil {
		contractLogger().Error(context.Background(), "ContractPropagation: failed to create wait group for forwarding", err)
		return
	}

	var successCount int
	var successMu sync.Mutex

	for _, peerID := range peers {
		if peerID.String() == msg.Sender {
			continue // don't echo back to original sender
		}
		p := peerID
		if err := ContractLocalGRO.Go(GRO.ContractForwardThread, func(ctx context.Context) error {
			ctxT, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			stream, err := h.NewStream(ctxT, p, config.ContractPropagationProtocol)
			if err != nil {
				contractLogger().Debug(ctx, "ContractPropagation: forward stream failed",
					ion.Err(err), ion.String("peer", p.String()))
				return err
			}
			defer stream.Close()
			if _, err := stream.Write(msgBytes); err != nil {
				return err
			}
			successMu.Lock()
			successCount++
			successMu.Unlock()
			metrics.MessagesSentCounter.WithLabelValues("contract", metrics.PeerLabel(p.String())).Inc()
			return nil
		}, local.AddToWaitGroup(GRO.ContractForwardWG)); err != nil {
			contractLogger().Error(context.Background(), "ContractPropagation: failed to start forwarding goroutine", err,
				ion.String("peer", p.String()))
		}
	}

	wg.Wait()

	contractLogger().Info(context.Background(), "ContractPropagation: message forwarded",
		ion.String("msg_id", msg.ID),
		ion.String("contract", msg.ContractAddress.Hex()),
		ion.Int("peers_forwarded", successCount))
}

// ============================================================================
// Phase 2: Pull-on-demand
// ============================================================================

// ContractPullRequest is the payload written by a node that needs contract
// metadata it never received via gossip.
type ContractPullRequest struct {
	ContractAddress common.Address `json:"contract_address"`
}

// ContractPullResponse is the payload returned by a peer that has the contract.
// Bytecode is included so the requester can write it to its local KVStore and
// pass the HasCode check before block processing begins.
type ContractPullResponse struct {
	Found           bool           `json:"found"`
	ContractAddress common.Address `json:"contract_address"`
	Deployer        common.Address `json:"deployer"`
	TxHash          common.Hash    `json:"tx_hash"`
	BlockNumber     uint64         `json:"block_number"`
	ABI             string         `json:"abi,omitempty"`
	// Bytecode holds the raw EVM contract bytecode.  base64-encoded by JSON.
	Bytecode []byte `json:"bytecode,omitempty"`
	Error    string `json:"error,omitempty"`
}

// HandleContractPullStream is the libp2p stream handler registered on every
// node for ContractPullProtocol.  It answers pull requests from peers that
// missed the original gossip message.
func HandleContractPullStream(stream network.Stream) {
	defer stream.Close()
	// A peer that opens a pull stream and sends nothing must not hold this
	// handler's goroutine.
	_ = stream.SetDeadline(time.Now().Add(pullStreamTimeout))

	remotePeer := stream.Conn().RemotePeer().String()

	// Read request (single JSON line). Cap size to prevent a malicious peer from OOMing us.
	const maxPullReqBytes = 512 // ContractPullRequest is tiny — just a 20-byte address
	reader := bufio.NewReader(io.LimitReader(stream, maxPullReqBytes))
	reqBytes, err := reader.ReadBytes('\n')
	if err != nil && err != io.EOF {
		contractLogger().Error(context.Background(), "ContractPull: error reading request", err,
			ion.String("peer", remotePeer))
		return
	}
	if len(reqBytes) == 0 {
		return
	}

	var req ContractPullRequest
	if err := json.Unmarshal(reqBytes, &req); err != nil {
		contractLogger().Error(context.Background(), "ContractPull: failed to unmarshal request", err)
		respondPullError(stream, req.ContractAddress, "bad request")
		return
	}

	contractLogger().Debug(context.Background(), "ContractPull: received pull request",
		ion.String("contract", req.ContractAddress.Hex()),
		ion.String("peer", remotePeer))

	resp := buildPullResponse(req.ContractAddress)

	respBytes, err := json.Marshal(resp)
	if err != nil {
		contractLogger().Error(context.Background(), "ContractPull: failed to marshal response", err)
		return
	}
	respBytes = append(respBytes, '\n')

	if _, err := stream.Write(respBytes); err != nil {
		contractLogger().Error(context.Background(), "ContractPull: failed to write response", err,
			ion.String("peer", remotePeer))
	}
}

// buildPullResponse constructs a ContractPullResponse from local state.
// Bytecode is always included when available so the requester can write it to
// its own KVStore and satisfy HasCode checks before block processing begins.
//
// On this build "when available" is never: GetCodeBytes reads a singleton that
// is never assigned, so this returns Found=false for every address. See
// pullStreamTimeout.
func buildPullResponse(addr common.Address) ContractPullResponse {
	resp := ContractPullResponse{ContractAddress: addr}

	// Bytecode must be present — otherwise we can't help the requester.
	code, hasCode := SmartContract.GetCodeBytes(addr)
	if !hasCode {
		return resp // Found=false
	}
	resp.Found = true
	resp.Bytecode = code

	// Enrich with registry metadata when available.
	if abi, ok := SmartContract.GetContractABI(addr); ok {
		resp.ABI = abi
	}

	// Retrieve full metadata from the registry.
	if meta, ok := SmartContract.GetContractMeta(addr); ok {
		resp.Deployer = meta.Deployer
		resp.TxHash = meta.DeployTxHash
		resp.BlockNumber = meta.DeployBlock
	}

	return resp
}

// respondPullError writes a minimal error response when request parsing fails.
func respondPullError(stream network.Stream, addr common.Address, msg string) {
	resp := ContractPullResponse{Found: false, ContractAddress: addr, Error: msg}
	b, _ := json.Marshal(resp)
	b = append(b, '\n')
	_, _ = stream.Write(b)
}

// pullStreamTimeout bounds a contract-pull request and its answer on the
// serving side (HandleContractPullStream).
//
// This node no longer pulls: the client (PullContractIfMissing and
// PrefetchMissingContracts) ran on the block-apply goroutine, asked every peer
// once per EIP-1559 recipient, and stored into sharedKVStore, which is never
// set up, while execution reads code from cas.KV(); a silent peer froze block
// apply.
//
// The server stays registered (main.go, ContractPullProtocol) so an older node
// that still pulls gets an answer rather than a stream error — but on this
// build that answer is always Found=false, so it is not a working fallback:
// buildPullResponse reads SmartContract.GetCodeBytes -> contractDB.GetCodeBytes,
// which returns (nil, false) while sharedKVStore is nil, and sharedKVStore is
// declared in DB_OPs/contractDB/contractdb.go and never assigned anywhere in
// the tree (main.go notes the same for the HasCode it replaced).
//
// Two ways out, whichever this fleet needs: point the responder at cas.KV() —
// the store deploys commit to and execution reads, see main.go's contractRepo —
// to make the server real, or retire the handler and its registration together
// with contractDB.HasCode and contractDB.StoreCodeBytes, which the client
// removal left with no callers.
var pullStreamTimeout = 5 * time.Second // a variable so tests can shorten it
