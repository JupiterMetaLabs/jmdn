package Block

import (
	"context"
	"errors"
	"fmt"

	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/messaging/BlockProcessing"
)

// errProposalPrediction marks a stampProposalAccountNonces failure in the
// contract-execution prediction step (a state read failed during simulation);
// callers map it to "unavailable / retry". Any other error is an enrichment
// failure.
var errProposalPrediction = errors.New("failed to predict contract-created accounts")

// stampProposalAccountNonces is the ONE pre-consensus ART-identity step for every
// proposal ingress — HTTP processZKBlock (Server.go) and gRPC
// BlockServer.ProcessBlock (grpc_server.go). Both MUST call this and nothing else
// for AccountNonces: when the HTTP path moved to predicted enrichment and the
// gRPC path kept the old call, gRPC-proposed blocks lost the execution-created
// accounts and hit the block-936 failure again (D-78).
// TestProposalIngress_UsesSharedNonceStamp pins that both handlers call it.
//
// It stamps a canonical identity for every distinct sender/receiver, buddy
// reward-split recipient (block.FeeRecipients — so it must run AFTER
// attachAVCConsensusFields), and every account the block's contract txs touch
// during EVM execution (internal CALL{value} recipients, SELFDESTRUCT
// beneficiaries, value-funded CREATE/CREATE2 children, deployed contracts),
// predicted by simulating each contract tx without committing. AccountNonces is
// advisory (outside BlockHash and ConsensusHash), so this changes neither.
//
// Fail-closed: any error means the block must not be proposed.
func stampProposalAccountNonces(ctx context.Context, block *config.ZKBlock) error {
	predicted, err := BlockProcessing.PredictContractCreatedAccounts(ctx, block)
	if err != nil {
		return fmt.Errorf("%w: %v", errProposalPrediction, err)
	}
	if err := DB_OPs.EnrichBlockAccountNoncesWithPredicted(block, predicted); err != nil {
		return fmt.Errorf("failed to enrich block with account nonces: %w", err)
	}
	return nil
}
