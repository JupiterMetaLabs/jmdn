package BLS_Signer

import (
	"encoding/hex"
	"fmt"
	blssign "gossipnode/AVC/BLS/bls-sign"
	"strconv"
	"sync"
	"gossipnode/config/utils"
)

type BLSresponse struct {
	Signature string
	Agree     bool
	PubKey    string
	PeerID    string
}

// cached BLS keypair (per-process)
var (
	blsOnce sync.Once
	blsPriv []byte
	blsPub  []byte
	blsErr  error
)

func getBLSKeypair(rawPriv []byte) ([]byte, []byte, error) {
	blsOnce.Do(func() {
		blsPriv, blsPub, blsErr = blssign.GenerateBLSKeyPairFromRawPrivKey(rawPriv)
	})
	return blsPriv, blsPub, blsErr
}

// Service functions for the BLSresponse struct
// Signs canonical message "vote:<v>" for BOTH 1 and -1
func SignMessage(vote int8) (BLSresponse, bool, error) {
	if vote != -1 && vote != 1 {
		return *NewBLSresponseBuilder(nil), false, fmt.Errorf("invalid vote")
	}

	privKey, err := utils.ReturnPrivateKey()
	if err != nil {
		return *NewBLSresponseBuilder(nil), false, err
	}

	rawPriv, err := privKey.Raw()
	if err != nil {
		return *NewBLSresponseBuilder(nil), false, err
	}

	priv, pub, err := getBLSKeypair(rawPriv)
	if err != nil {
		return *NewBLSresponseBuilder(nil), false, err
	}

	msg := []byte("vote:" + strconv.Itoa(int(vote)))
	sig, err := blssign.BLSSign(priv, msg)
	if err != nil {
		return *NewBLSresponseBuilder(nil), false, err
	}

	pubHex := hex.EncodeToString(pub)

	return *NewBLSresponseBuilder(nil).
		SetSignature(hex.EncodeToString(sig)).
		SetAgree(vote == 1).
		SetPubKey(pubHex).
		Build(), true, nil
}
