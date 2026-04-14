package messaging

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"sync"
	"time"

	"gossipnode/SmartContract"
	"gossipnode/config"
	"gossipnode/config/GRO"
	"gossipnode/messaging/BlockProcessing"
	GROHelper "gossipnode/messaging/common"
	"gossipnode/metrics"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/local"
	"github.com/bits-and-blooms/bloom/v3"
	"github.com/ethereum/go-ethereum/common"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/rs/zerolog/log"
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
)

// InitContractPropagation initialises the Bloom filter used for deduplication.
// Must be called once at node startup before any contract propagation streams arrive.
func InitContractPropagation() error {
	contractFilterOnce.Do(func() {
		contractFilter = bloom.NewWithEstimates(100_000, 0.01)
	})
	log.Info().Msg("Contract propagation system initialised")
	return nil
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
	if ContractLocalGRO == nil {
		var err error
		ContractLocalGRO, err = GROHelper.InitializeGRO(GRO.ContractPropagationLocal)
		if err != nil {
			log.Error().Err(err).Msg("ContractPropagation: failed to initialize LocalGRO")
			return
		}
	}

	peers := h.Network().Peers()
	if len(peers) == 0 {
		log.Warn().Msg("ContractPropagation: no peers to propagate to")
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
			log.Error().Err(err).Str("contract", dep.ContractAddress.Hex()).Msg("ContractPropagation: failed to marshal message")
			continue
		}
		msgBytes = append(msgBytes, '\n')

		wg, err := ContractLocalGRO.NewFunctionWaitGroup(context.Background(), GRO.ContractForwardWG)
		if err != nil {
			log.Error().Err(err).Msg("ContractPropagation: failed to create wait group")
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
					log.Debug().Err(err).Str("peer", p.String()).Msg("ContractPropagation: failed to open stream")
					return err
				}
				defer stream.Close()
				if _, err := stream.Write(msgBytes); err != nil {
					log.Debug().Err(err).Str("peer", p.String()).Msg("ContractPropagation: failed to write message")
					return err
				}
				successMu.Lock()
				successCount++
				successMu.Unlock()
				metrics.MessagesSentCounter.WithLabelValues("contract", p.String()).Inc()
				return nil
			}, local.AddToWaitGroup(GRO.ContractForwardWG)); err != nil {
				log.Error().Err(err).Str("peer", p.String()).Msg("ContractPropagation: failed to start goroutine")
			}
		}

		wg.Wait()

		log.Info().
			Str("contract", dep.ContractAddress.Hex()).
			Uint64("block", dep.BlockNumber).
			Int("peers_sent", successCount).
			Int("peers_total", len(peers)).
			Msg("Contract deployment propagated")
	}
}

// HandleContractStream is the libp2p stream handler registered on all nodes for
// the ContractPropagationProtocol.  It deduplicates via Bloom filter, writes the
// contract metadata to the local registry, and hop-limits re-forwarding.
func HandleContractStream(stream network.Stream) {
	if ContractLocalGRO == nil {
		var err error
		ContractLocalGRO, err = GROHelper.InitializeGRO(GRO.ContractPropagationLocal)
		if err != nil {
			log.Error().Err(err).Msg("ContractPropagation: failed to initialize LocalGRO")
			return
		}
	}
	defer stream.Close()

	remotePeer := stream.Conn().RemotePeer().String()
	metrics.MessagesReceivedCounter.WithLabelValues("contract", remotePeer).Inc()

	reader := bufio.NewReader(stream)
	msgBytes, err := reader.ReadBytes('\n')
	if err != nil {
		if err != io.EOF {
			log.Error().Err(err).Str("peer", remotePeer).Msg("ContractPropagation: error reading stream")
		}
		return
	}

	var msg ContractMessage
	if err := json.Unmarshal(msgBytes, &msg); err != nil {
		log.Error().Err(err).Msg("ContractPropagation: failed to unmarshal message")
		return
	}

	if isContractMessageProcessed(msg.ID) {
		log.Debug().Str("msg_id", msg.ID).Msg("ContractPropagation: duplicate message, dropping")
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
			log.Error().Msg("ContractPropagation: host instance unavailable for forwarding")
		}
	} else {
		log.Debug().
			Str("msg_id", msg.ID).
			Int("hops", msg.Hops).
			Msg("ContractPropagation: max hops reached, not re-forwarding")
	}
}

// storeContractFromGossip persists a received ContractMessage to the local registry.
func storeContractFromGossip(msg ContractMessage) {
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
			log.Error().
				Err(err).
				Str("contract", msg.ContractAddress.Hex()).
				Msg("ContractPropagation: failed to register contract from gossip")
			return err
		}
		log.Info().
			Str("contract", msg.ContractAddress.Hex()).
			Uint64("block", msg.BlockNumber).
			Msg("ContractPropagation: contract registered from gossip")
		return nil
	})
}

// forwardContract re-sends a ContractMessage to all currently-connected peers
// (excluding the original sender).
func forwardContract(h host.Host, msg ContractMessage) {
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		log.Error().Err(err).Msg("ContractPropagation: failed to marshal message for forwarding")
		return
	}
	msgBytes = append(msgBytes, '\n')

	peers := h.Network().Peers()

	wg, err := ContractLocalGRO.NewFunctionWaitGroup(context.Background(), GRO.ContractForwardWG)
	if err != nil {
		log.Error().Err(err).Msg("ContractPropagation: failed to create wait group for forwarding")
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
				log.Debug().Err(err).Str("peer", p.String()).Msg("ContractPropagation: forward stream failed")
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
			log.Error().Err(err).Str("peer", p.String()).Msg("ContractPropagation: failed to start forwarding goroutine")
		}
	}

	wg.Wait()

	log.Info().
		Str("msg_id", msg.ID).
		Str("contract", msg.ContractAddress.Hex()).
		Int("peers_forwarded", successCount).
		Msg("ContractPropagation: message forwarded")
}
