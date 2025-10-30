package BLS_Signer

import (
	"fmt"
	blssign "gossipnode/AVC/BLS/bls-sign"
	"gossipnode/config/utils"
)

type BLSresponse struct {
	Signature string
	Agree     bool
	PubKey    string
	PeerID    string
}

// Service functions for the BLSresponse struct
func SignMessage(message string, vote int8) (BLSresponse, bool, error) {
	privKey, err := utils.ReturnPrivateKey()
	if err != nil {
		return *NewBLSresponseBuilder(nil), false, err
	}

	privKeyBytes, err := privKey.Raw()
	if err != nil {
		return *NewBLSresponseBuilder(nil), false, err
	}

	pubkey, err := utils.ReturnPublicKeyString()
	if err != nil {
		return *NewBLSresponseBuilder(nil), false, err
	}

	switch vote {
	case -1:
		return *NewBLSresponseBuilder(nil), false, nil
	case 1:
		sig, err := blssign.BLSSign(privKeyBytes, []byte(message))
		if err != nil {
			return *NewBLSresponseBuilder(nil), false, err
		}
		return *NewBLSresponseBuilder(nil).
			SetSignature(string(sig)).
			SetAgree(true).
			SetPubKey(pubkey).
			Build(), true, nil
	default:
		return *NewBLSresponseBuilder(nil), false, fmt.Errorf("invalid vote")
	}
}
