package BLS_Signer

import (
	"fmt"
	blssign "gossipnode/AVC/BLS/bls-sign"
	"gossipnode/config/utils"
	"strconv"
)

type BLSresponse struct {
	Signature string
	Agree     bool
	PubKey    string
	PeerID    string
}

// Service functions for the BLSresponse struct
func SignMessage(vote int8) (BLSresponse, bool, error) {

	if vote != -1 && vote != 1 {
		return *NewBLSresponseBuilder(nil), false, fmt.Errorf("invalid vote")
	}

	voteString := strconv.Itoa(int(vote))

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

	sig, err := blssign.BLSSign(privKeyBytes, []byte(voteString))
	if err != nil {
		return *NewBLSresponseBuilder(nil), false, err
	}

	return *NewBLSresponseBuilder(nil).
		SetSignature(string(sig)).
		SetAgree(true).
		SetPubKey(pubkey).
		Build(), true, nil
}
