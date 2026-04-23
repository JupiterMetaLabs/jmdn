package messaging

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/rand"
	"sync"
	"time"

	"gossipnode/SmartContract"
	"gossipnode/config"
	"gossipnode/config/GRO"
	"gossipnode/messaging/BlockProcessing"
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
	Type            string         `json:"type"`            // "contract_deployed"
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

// generateContractMessageID creates a deterministic, short ID for a contract gossip message.
func generateContractMessageID(sender string, addr common.Address, blockNumber uint64) string {
	hasher := sha256.New()
	hasher.Write([]byte(sender))
	hasher.Write(addr.Bytes())
	hasher.Write([]byte{
		byte(blockNumber >> 56), byte(blockNumber >> 48), byte(blockNumber >> 40), byte(blockNumber >> 32),
		byte(blockNumber >> 24), byte(blockNumber >> 16), byte(blockNumber >> 8), byte(blockNumber),
	})
	hash := base64.URLEncoding.EncodeToString(hasher.Sum(nil))
	return hash[:16]
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

// PropagateContractDeployments builds a ContractMessage for each deployment and
// gossips it to every currently-connected peer.
// Called as a goroutine (fire-and-forget) exclusively from the sequencer path.
func PropagateContractDeployments(h host.Host, deployments []BlockProcessing.ContractDeploymentInfo) {
	if err := ensureContractLocalGRO(); err != nil {
		contractLogger().Error(context.Background(), "ContractPropagation: failed to initialize LocalGRO", err)
		return
	}

	peers := h.Network().Peers()
	if len(peers) == 0 {
		contractLogger().Warn(context.Background(), "ContractPropagation: no peers to propagate to")
		return
	}

	senderID := h.ID().String()
	now := time.Now().UTC().Unix()

	for _, dep := range deployments {
		msg := ContractMessage{
			Sender:          senderID,
			Timestamp:       now,
			Type:            "contract_deployed",
			Hops:            0,
			ContractAddress: dep.ContractAddress,
			Deployer:        dep.Deployer,
			TxHash:          dep.TxHash,
			BlockNumber:     dep.BlockNumber,
			GasUsed:         dep.GasUsed,
		}
		msg.ID = generateContractMessageID(senderID, dep.ContractAddress, dep.BlockNumber)

		// Populate ABI from the local registry if available.
		if abi, ok := SmartContract.GetContractABI(dep.ContractAddress); ok {
			msg.ABI = abi
		}

		markContractMessageProcessed(msg.ID)

		msgBytes, err := json.Marshal(msg)
		if err != nil {
			contractLogger().Error(context.Background(), "ContractPropagation: failed to marshal message", err,
				ion.String("contract", dep.ContractAddress.Hex()))
			continue
		}
		msgBytes = append(msgBytes, '\n')

		wg, err := ContractLocalGRO.NewFunctionWaitGroup(context.Background(), GRO.ContractForwardWG)
		if err != nil {
			contractLogger().Error(context.Background(), "ContractPropagation: failed to create wait group", err)
			continue
		}

		var successCount int
		var successMu sync.Mutex

		for _, peerID := range peers {
			p := peerID
			if err := ContractLocalGRO.Go(GRO.ContractPropagationThread, func(ctx context.Context) error {
				ctxT, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				stream, err := h.NewStream(ctxT, p, config.ContractPropagationProtocol)
				if err != nil {
					contractLogger().Debug(ctx, "ContractPropagation: failed to open stream",
						ion.Err(err), ion.String("peer", p.String()))
					return err
				}
				defer stream.Close()
				if _, err := stream.Write(msgBytes); err != nil {
					contractLogger().Debug(ctx, "ContractPropagation: failed to write message",
						ion.Err(err), ion.String("peer", p.String()))
					return err
				}
				successMu.Lock()
				successCount++
				successMu.Unlock()
				metrics.MessagesSentCounter.WithLabelValues("contract", p.String()).Inc()
				return nil
			}, local.AddToWaitGroup(GRO.ContractForwardWG)); err != nil {
				contractLogger().Error(context.Background(), "ContractPropagation: failed to start goroutine", err,
				ion.String("peer", p.String()))
			}
		}

		wg.Wait()

		contractLogger().Info(context.Background(), "Contract deployment propagated",
			ion.String("contract", dep.ContractAddress.Hex()),
			ion.Uint64("block", dep.BlockNumber),
			ion.Int("peers_sent", successCount),
			ion.Int("peers_total", len(peers)))
	}
}

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
	metrics.MessagesReceivedCounter.WithLabelValues("contract", remotePeer).Inc()

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
			metrics.MessagesSentCounter.WithLabelValues("contract", p.String()).Inc()
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

// PullContractIfMissing fetches contract metadata from a peer and stores it
// locally.  Returns true if the contract is already present (HasCode) or was
// successfully fetched.  Returns false when no peer has the contract.
//
// The call is synchronous — callers should wrap it in a goroutine if they
// don't want to block.
func PullContractIfMissing(ctx context.Context, h host.Host, addr common.Address) bool {
	// Fast path — already have it.
	if SmartContract.HasCode(addr) {
		return true
	}

	peers := h.Network().Peers()
	if len(peers) == 0 {
		contractLogger().Warn(ctx, "ContractPull: no peers available for pull",
			ion.String("contract", addr.Hex()))
		return false
	}

	// Copy and shuffle so we don't always hammer the same peer.
	order := make([]int, len(peers))
	for i := range order {
		order[i] = i
	}
	rand.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })

	reqBytes, err := json.Marshal(ContractPullRequest{ContractAddress: addr})
	if err != nil {
		return false
	}
	reqBytes = append(reqBytes, '\n')

	for _, idx := range order {
		p := peers[idx]

		pullCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		stream, err := h.NewStream(pullCtx, p, config.ContractPullProtocol)
		cancel()
		if err != nil {
			contractLogger().Debug(ctx, "ContractPull: stream open failed",
				ion.Err(err), ion.String("peer", p.String()))
			continue
		}

		var resp ContractPullResponse
		func() {
			defer stream.Close()
			if _, err := stream.Write(reqBytes); err != nil {
				contractLogger().Debug(ctx, "ContractPull: write failed",
					ion.Err(err), ion.String("peer", p.String()))
				return
			}
			// ContractPullResponse includes bytecode — cap at 25 MB (EVM max contract size is 24.576 KB, but be generous).
			const maxPullRespBytes = 25 * 1024 * 1024
			reader := bufio.NewReader(io.LimitReader(stream, maxPullRespBytes))
			respBytes, err := reader.ReadBytes('\n')
			if err != nil && err != io.EOF {
				contractLogger().Debug(ctx, "ContractPull: read failed",
					ion.Err(err), ion.String("peer", p.String()))
				return
			}
			if err := json.Unmarshal(respBytes, &resp); err != nil {
				contractLogger().Debug(ctx, "ContractPull: unmarshal response failed",
					ion.Err(err))
			}
		}()

		if !resp.Found {
			continue
		}

		// Write bytecode into the local KVStore first so that HasCode returns
		// true and contract execution can proceed during block processing.
		if len(resp.Bytecode) > 0 {
			if codeErr := SmartContract.StoreCodeBytes(resp.ContractAddress, resp.Bytecode); codeErr != nil {
				contractLogger().Error(ctx, "ContractPull: failed to store pulled bytecode", codeErr,
					ion.String("contract", addr.Hex()))
				// Continue to next peer — bytecode write failure means we can't use this response.
				continue
			}
		} else {
			contractLogger().Warn(ctx, "ContractPull: peer returned found=true but sent no bytecode, skipping",
				ion.String("contract", addr.Hex()), ion.String("peer", p.String()))
			continue
		}

		// Store registry metadata (deployer, ABI, etc.) — best-effort.
		if regErr := SmartContract.RegisterContractFromGossip(
			ctx,
			resp.ContractAddress,
			resp.Deployer,
			resp.TxHash,
			resp.BlockNumber,
			resp.ABI,
		); regErr != nil {
			contractLogger().Warn(ctx, "ContractPull: failed to register contract metadata (bytecode stored successfully)",
				ion.Err(regErr), ion.String("contract", addr.Hex()))
		}

		contractLogger().Info(ctx, "ContractPull: contract bytecode and metadata fetched from peer",
			ion.String("contract", addr.Hex()),
			ion.String("peer", p.String()),
			ion.Uint64("block", resp.BlockNumber),
			ion.Int("bytecode_bytes", len(resp.Bytecode)))
		return true
	}

	contractLogger().Warn(ctx, "ContractPull: no peer had the contract",
		ion.String("contract", addr.Hex()))
	return false
}

// PrefetchMissingContracts scans block transactions and pulls metadata for
// any contract-call addresses whose bytecode isn't in the local KV store.
// Called by HandleBlockStream before ProcessBlockTransactions so that missed
// gossip doesn't cause contract executions to fall through to the regular
// transfer path.
func PrefetchMissingContracts(ctx context.Context, h host.Host, txs []config.Transaction) {
	for _, tx := range txs {
		if tx.To == nil || tx.Type != 2 {
			continue // not a contract call
		}
		if SmartContract.HasCode(*tx.To) {
			continue // already present
		}
		contractLogger().Info(ctx, "ContractPull: pre-fetching missing contract before block processing",
			ion.String("contract", tx.To.Hex()))
		PullContractIfMissing(ctx, h, *tx.To)
	}
}
