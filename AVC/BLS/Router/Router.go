package Router

import (
	"fmt"
	blssign "gossipnode/AVC/BLS/bls-sign"
)

type BLSRouter struct {
	bls *blssign.MultiSigManager
}

func NewBLSRouter() *BLSRouter {
	return &BLSRouter{
		bls: blssign.NewMultiSigManager(3),
	}
}

func (r *BLSRouter) SignMessage(agree bool, message string, signerID string) (string, error) {
	if agree {
		sig, err := r.bls.SignAsParticipant(signerID, []byte(message))
		if err != nil {
			return "", err
		}
		return string(sig), nil
	}
	return "", fmt.Errorf("not agreed")
}

func (r *BLSRouter) AddSigner(signerID string, publicKey []byte, privateKey []byte) error {
	return r.bls.AddSigner(signerID, publicKey, privateKey)
}

func (r *BLSRouter) RemoveSigner(signerID string) {
	r.bls.RemoveSigner(signerID)
}

func (r *BLSRouter) GetSigner(signerID string) (*blssign.Signer, error) {
	return r.bls.GetSigner(signerID)
}

func (r *BLSRouter) CollectAndAggregateSignatures(messageKey string, message string, signerIDs []string) ([]byte, [][]byte, error) {
	msgBytes := []byte(message)
	for _, signerID := range signerIDs {
		sig, err := r.bls.SignAsParticipant(signerID, msgBytes)
		if err != nil {
			return nil, nil, err
		}
		signer, err := r.bls.GetSigner(signerID)
		if err != nil {
			return nil, nil, err
		}
		share := blssign.SignatureShare{
			SignerID:  signerID,
			PublicKey: signer.PublicKey,
			Signature: sig,
			Message:   msgBytes,
		}
		err = r.bls.AddSignatureShare(messageKey, share)
		if err != nil {
			return nil, nil, err
		}
	}
	// Aggregate
	aggSig, pubKeys, _, err := r.bls.AggregateSignatures(messageKey)
	if err != nil {
		return nil, nil, err
	}
	return aggSig, pubKeys, nil
}

// VerifyAggregated checks the aggregated signature for messageKey+message
// and also returns who signed and who hasn't.
func (r *BLSRouter) VerifyAggregated(messageKey, message string, aggSig []byte) (bool, []string, []string, error) {
	// Progress info
	signed := r.bls.SignedSignerIDs(messageKey)
	unsigned := r.bls.UnsignedSignerIDs(messageKey)

	// Optional: enforce threshold before verifying
	if !r.bls.HasReachedThreshold(messageKey) {
		return false, signed, unsigned, fmt.Errorf(
			"threshold not reached: have %d, need %d",
			r.bls.GetCurrentSignatureCount(messageKey),
			r.bls.Threshold(),
		)
	}

	// Core BLS aggregate verify (uses the pubkeys from collected shares)
	ok, err := r.bls.VerifyAggregatedSignature(messageKey, aggSig)
	if err != nil {
		return false, signed, unsigned, err
	}
	return ok, signed, unsigned, nil
}

// GenerateKeyPair provides a public key generation interface wrapped from blssign for use by router consumers and tests.
func GenerateKeyPair() (priv, pub []byte, err error) {
	return blssign.GenerateBLSKeyPair()
}
