package Service

import (
	"context"
	"math/big"

	"gossipnode/messaging"
)

// FinalizedBlockNumber implements Service. See docs/finality.md for what
// "finalized" promises.
func (s *ServiceImpl) FinalizedBlockNumber(ctx context.Context) (*big.Int, error) {
	h, err := messaging.FinalizedHeight(ctx)
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetUint64(h), nil
}

// CertifiedBlock implements Service (jmdt_getCertifiedBlock).
func (s *ServiceImpl) CertifiedBlock(_ context.Context, height uint64) (*messaging.CertifiedBlockReport, error) {
	return messaging.BuildCertifiedBlockReport(height)
}
