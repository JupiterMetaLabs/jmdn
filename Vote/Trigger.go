package Vote

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	MessagePassing "gossipnode/AVC/BuddyNodes/MessagePassing"
	"gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/AVC/BuddyNodes/ServiceLayer"
	"gossipnode/AVC/BuddyNodes/Types"
	"gossipnode/Security"
	"gossipnode/consensus/adapters"

	"time"

	"gossipnode/config"
	"gossipnode/config/PubSubMessages"
	"gossipnode/config/settings"

	avcvotes "github.com/JupiterMetaLabs/avc/crdt/votes"
	"github.com/JupiterMetaLabs/ion"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.opentelemetry.io/otel/attribute"
)

type VoteTrigger struct {
	ConsensusMessage *PubSubMessages.ConsensusMessage
	Vote             *PubSubMessages.Vote
}

func NewVoteTrigger() VoteTrigger {
	return VoteTrigger{
		ConsensusMessage: nil,
		Vote:             nil,
	}
}

func (vt *VoteTrigger) SetConsensusMessage(consensusMessage *PubSubMessages.ConsensusMessage) {
	vt.ConsensusMessage = consensusMessage
}

func (vt *VoteTrigger) setVote(Vote *PubSubMessages.Vote) error {

	if Vote.GetVote() != 1 && Vote.GetVote() != -1 {
		return fmt.Errorf("invalid vote")
	}

	if Vote.BlockHash == "" {
		return fmt.Errorf("block hash required for vote")
	}

	vt.Vote = Vote
	return nil
}

func (vt *VoteTrigger) GetVote() *PubSubMessages.Vote {
	return vt.Vote
}

func (vt *VoteTrigger) ToVoteString(vote *PubSubMessages.Vote) string {
	jsonData, err := json.Marshal(vote)
	if err != nil {
		return ""
	}
	return string(jsonData)
}

func (vt *VoteTrigger) SubmitVote() error {
	// Get the Listener Node
	listenerNode := PubSubMessages.NewGlobalVariables().Get_ForListner()
	if listenerNode == nil {
		return fmt.Errorf("listener node not found")
	}

	// If consensus message is not set, try to get it from global cache
	if vt.ConsensusMessage == nil {
		// This should not happen in normal flow, but handle gracefully
		return fmt.Errorf("consensus message not set for voting")
	}

	// Create trace context for vote submission
	logger_ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tracer := logger().Tracer("Vote")
	spanCtx, span := tracer.Start(logger_ctx, "Vote.SubmitVote")
	defer span.End()

	zkBlock := vt.ConsensusMessage.GetZKBlock()
	blockHash := zkBlock.BlockHash.String()

	// Add span attributes for tracing
	span.SetAttributes(
		attribute.String("peer_id", listenerNode.PeerID.String()),
		attribute.String("block_hash", blockHash),
		attribute.Int("block_number", int(zkBlock.BlockNumber)),
		attribute.Int("transaction_count", len(zkBlock.Transactions)),
	)

	// Check the Three security checks from the Security Module
	status, err := Security.CheckZKBlockValidation(zkBlock)

	// A3 wiring: optionally run the avc-based validator alongside (shadow) or
	// in place of (enforce) the check above. EvaluateShadow is a strict no-op
	// — returns status/err completely unchanged — unless this node has
	// explicitly opted in via config (Features.AvcValidation.Enabled=true AND
	// Network.Environment=="testnet"). See consensus/adapters/shadow.go.
	status, err = adapters.EvaluateShadow(spanCtx, settings.Get(), zkBlock, status, err)

	if !status || err != nil {
		// VOTE REJECTED (-1)
		rejectionReason := "validation returned false"
		if err != nil {
			rejectionReason = err.Error()
		}
		vote := PubSubMessages.Vote{
			Vote:            -1,
			BlockHash:       blockHash,
			RejectionReason: rejectionReason,
			Height:          zkBlock.BlockNumber,
		}
		vt.setVote(&vote)

		span.SetAttributes(
			attribute.Int("vote", -1),
			attribute.String("vote_decision", "REJECT"),
		)

		if err != nil {
			span.RecordError(err)

			// 🔴 DETAILED REJECTION LOGGING WITH STRUCTURED LOGGER
			logger().Error(spanCtx, "VOTE REJECTED: Block validation failed",
				err,
				ion.String("peer_id", listenerNode.PeerID.String()),
				ion.String("block_hash", blockHash),
				ion.Int("block_number", int(zkBlock.BlockNumber)),
				ion.Int("transaction_count", len(zkBlock.Transactions)),
				ion.Int("vote", -1),
				ion.String("vote_decision", "REJECT"),
				ion.String("rejection_reason", err.Error()),
				ion.String("function", "Vote.SubmitVote"))

			// Also log to console via logger
			logger().Info(spanCtx, "VOTE REJECTED (-1)",
				ion.String("peer_id", listenerNode.PeerID.String()),
				ion.String("block_hash", blockHash),
				ion.Int("block_number", int(zkBlock.BlockNumber)),
				ion.Int("transaction_count", len(zkBlock.Transactions)),
				ion.String("rejection_reason", err.Error()))
		} else {
			// Status is false but no error
			logger().Warn(spanCtx, "VOTE REJECTED: Validation returned false without error",
				ion.String("peer_id", listenerNode.PeerID.String()),
				ion.String("block_hash", blockHash),
				ion.Int("block_number", int(zkBlock.BlockNumber)),
				ion.Int("vote", -1),
				ion.String("vote_decision", "REJECT"),
				ion.String("function", "Vote.SubmitVote"))

			logger().Info(spanCtx, "VOTE REJECTED (-1)",
				ion.String("peer_id", listenerNode.PeerID.String()),
				ion.String("block_hash", blockHash),
				ion.Int("block_number", int(zkBlock.BlockNumber)),
				ion.String("rejection_reason", "Validation returned false (no error details)"))
		}
	} else if status {
		// VOTE ACCEPTED (1)
		vote := PubSubMessages.Vote{
			Vote:      1,
			BlockHash: blockHash,
			Height:    zkBlock.BlockNumber,
		}
		vt.setVote(&vote)

		span.SetAttributes(
			attribute.Int("vote", 1),
			attribute.String("vote_decision", "ACCEPT"),
		)

		// ✅ ACCEPTANCE LOGGING
		logger().Info(spanCtx, "VOTE ACCEPTED: Block validation successful",
			ion.String("peer_id", listenerNode.PeerID.String()),
			ion.String("block_hash", blockHash),
			ion.Int("block_number", int(zkBlock.BlockNumber)),
			ion.Int("transaction_count", len(zkBlock.Transactions)),
			ion.Int("vote", 1),
			ion.String("vote_decision", "ACCEPT"),
			ion.String("function", "Vote.SubmitVote"))

		logger().Info(spanCtx, "VOTE ACCEPTED (1)",
			ion.String("peer_id", listenerNode.PeerID.String()),
			ion.String("block_hash", blockHash),
			ion.Int("block_number", int(zkBlock.BlockNumber)))
	} else {
		return fmt.Errorf("failed to vote, as vote is neither 1 or -1")
	}

	// Store own vote in the local CRDT before sending to the sequencer.
	// This ensures that when the sequencer pulls BLS from this node, its
	// CRDT has at least its own vote to sign over — regardless of pubsub
	// propagation timing. Without this, ProcessVotesFromCRDT finds 0 votes
	// and returns an error, producing 0 BLS results on the sequencer side.
	if listenerNode.CRDTLayer != nil {
		ownVoteJSON := vt.ToVoteString(vt.Vote)
		OP := &Types.OP{
			NodeID: listenerNode.PeerID,
			OpType: int8(1),
			KeyValue: Types.KeyValue{
				Key:   listenerNode.PeerID.String(),
				Value: ownVoteJSON,
			},
		}
		if result := ServiceLayer.Controller(listenerNode.CRDTLayer, OP); result != nil {
			if err, ok := result.(error); ok && err != nil {
				logger().Warn(spanCtx, "Failed to store own vote in local CRDT (non-fatal, will still send to sequencer)",
					ion.Err(err),
					ion.String("function", "Vote.SubmitVote"))
			}
		}
		logger().Info(spanCtx, "Stored own vote in local CRDT",
			ion.String("peer_id", listenerNode.PeerID.String()),
			ion.Int("vote", int(vt.Vote.Vote)),
			ion.String("block_hash", vt.Vote.BlockHash),
			ion.String("function", "Vote.SubmitVote"))
	}

	// NEW — additive, flagged. Nothing here may ever affect vt.Vote,
	// blockHash, or this function's return value. A failure here is logged
	// and dropped; the legacy write above remains the only one that matters
	// until Stage 4 rewires the readers.
	if VoteCRDTDualWrite && listenerNode.VoteCRDTLayer != nil {
		// Per-vote BLS signature. Nothing in the codebase signs individual
		// votes before this — the existing signer only produces an
		// AGGREGATED result at tally time (ListenerHandler.go). Same domain,
		// same key material as that path, just invoked at cast time instead
		// of at aggregation time.
		blsResp, signed, blsErr := BLS_Signer.SignMessageForBlock(
			vt.Vote.Vote,
			BLS_Signer.DomainChainID(),
			zkBlock.BlockNumber,
			blockHash,
		)
		signingOK := blsErr == nil && signed

		// A node that cannot sign is a NORMAL (non-Buddy) validator in the
		// approved design: it submits an unsigned vote rather than no vote at
		// all. With the flag off this stays a skip, exactly as before —
		// signing failure meant no v2 write, and it still does.
		if !signingOK && !avcvotes.AllowUnsignedValidatorVotes {
			logger().Warn(spanCtx, "v2 vote CRDT: per-vote BLS signing failed, skipping v2 write (old path unaffected)",
				ion.String("block_hash", blockHash),
				ion.Err(blsErr),
				ion.String("function", "Vote.SubmitVote"))
		} else {
			rec := avcvotes.VoteRecord{
				PeerID:          listenerNode.PeerID.String(),
				Vote:            vt.Vote.Vote,
				BlockHash:       blockHash,
				Height:          zkBlock.BlockNumber,
				RejectionReason: vt.Vote.RejectionReason,
			}
			if signingOK {
				// Buddy (or any node holding BLS key material): signed exactly
				// as before. BLSSignature/BLSPubKeyHex are left empty ONLY on
				// the unsigned path below — never silently dropped here.
				rec.BLSSignature = blsResp.Signature
				rec.BLSPubKeyHex = blsResp.PubKey
			} else {
				logger().Info(spanCtx, "v2 vote CRDT: writing UNSIGNED normal-validator vote (no BLS key material)",
					ion.String("block_hash", blockHash),
					ion.String("peer_id", listenerNode.PeerID.String()),
					ion.String("function", "Vote.SubmitVote"))
			}
			if err := avcvotes.AddVote(listenerNode.VoteCRDTLayer, listenerNode.PeerID, rec); err != nil {
				if !errors.Is(err, avcvotes.ErrHeightCompacted) {
					logger().Warn(spanCtx, "v2 vote CRDT write failed (old path unaffected)",
						ion.Err(err),
						ion.String("block_hash", blockHash),
						ion.String("function", "Vote.SubmitVote"))
				}
				// ErrHeightCompacted is expected/harmless — a late vote for
				// an already-converged height. Not logged as an error.
			}
		}
	}

	// Create proper message with ACK stage for vote submission
	voteMessage := PubSubMessages.NewMessageBuilder(nil).
		SetSender(listenerNode.PeerID).
		SetMessage(vt.ToVoteString(vt.Vote)).
		SetTimestamp(time.Now().UTC().Unix()).
		SetACK(PubSubMessages.NewACKBuilder().True_ACK_Message(listenerNode.PeerID, config.Type_SubmitVote))

	// Marshal the message to JSON
	messageBytes, err := json.Marshal(voteMessage)
	if err != nil {
		return fmt.Errorf("failed to marshal vote message: %v", err)
	}

	// Reuse existing logger_ctx from above (already created with tracer)

	// Try to send to multiple nodes if first attempt fails
	maxAttempts := 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Pick up the listener node using the consistent hashing with offset
		NodeToSendTo := vt.PickListnerWithOffset(listenerNode.PeerID, attempt)

		// Check if trying to send to self - skip and try next
		if NodeToSendTo.PeerID == listenerNode.PeerID && attempt < maxAttempts-1 {
			continue
		}

		// Send the message to the listener node
		err := MessagePassing.NewListenerStruct(listenerNode).
			SendMessageToPeer(logger_ctx, NodeToSendTo.PeerID, string(messageBytes))

		if err != nil {
			// If this is not the last attempt, try again
			if attempt < maxAttempts-1 {
				fmt.Printf("⚠️ Failed to send vote to %s (attempt %d/%d): %v\n", NodeToSendTo.PeerID, attempt+1, maxAttempts, err)
				continue
			}
			// Last attempt failed
			return fmt.Errorf("failed to send vote to %s after %d attempts: %v", NodeToSendTo.PeerID, maxAttempts, err)
		}

		// Success!
		fmt.Printf("✅ Vote sent to %s\n", NodeToSendTo.PeerID)
		return nil
	}

	return fmt.Errorf("failed to submit vote after %d attempts", maxAttempts)
}

// __DEAD_CODE_AUDIT_PUBLIC__
func (vt *VoteTrigger) PickListner(PeerID peer.ID) PubSubMessages.Buddy_PeerMultiaddr {
	return vt.PickListnerWithOffset(PeerID, 0)
}

// PickListnerWithOffset picks a listener node using consistent hashing with an offset
func (vt *VoteTrigger) PickListnerWithOffset(PeerID peer.ID, offset int) PubSubMessages.Buddy_PeerMultiaddr {
	// Node should hash its own peerID  pick one from all the keys in buddies map
	buddies := vt.ConsensusMessage.GetBuddies()
	numKeys := len(buddies)

	if numKeys == 0 {
		// Return empty buddy if no buddies exist
		return PubSubMessages.Buddy_PeerMultiaddr{}
	}

	// Get the initial selected key using consistent hashing
	baseKey := consistentHashing(PeerID, numKeys)

	// Add offset to try different nodes
	selectedKey := (baseKey + offset) % numKeys

	// if the selected Key not in the buddies map, return the first peer
	if _, ok := buddies[selectedKey]; !ok {
		if offset < numKeys {
			// Try the next key
			return vt.PickListnerWithOffset(PeerID, (offset+1)%numKeys)
		}
		return buddies[0]
	}
	return buddies[selectedKey]
}

func consistentHashing(PeerID peer.ID, num int) int {
	// Node should hash its own peerID  pick one from all the keys in buddies map
	hasher := sha256.New()
	hasher.Write([]byte(PeerID.String()))
	hashBytes := hasher.Sum(nil)
	hashInt := binary.BigEndian.Uint64(hashBytes[:8])
	return int(hashInt % uint64(num)) // 0 index would be the first peer
}
