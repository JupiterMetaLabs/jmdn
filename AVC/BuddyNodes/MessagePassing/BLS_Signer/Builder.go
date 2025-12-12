package BLS_Signer

// Builder functions for the BLSresponse struct
func NewBLSresponseBuilder(blsresponse *BLSresponse) *BLSresponse {
	if blsresponse != nil {
		return &BLSresponse{
			Signature: blsresponse.Signature,
			Agree:     blsresponse.Agree,
			PubKey:    blsresponse.PubKey,
			PeerID:    blsresponse.PeerID,
		}
	}
	return &BLSresponse{}
}

func (blsresponse *BLSresponse) SetSignature(signature string) *BLSresponse {
	blsresponse.Signature = signature
	return blsresponse
}

func (blsresponse *BLSresponse) SetAgree(agree bool) *BLSresponse {
	blsresponse.Agree = agree
	return blsresponse
}

func (blsresponse *BLSresponse) SetPubKey(pubkey string) *BLSresponse {
	blsresponse.PubKey = pubkey
	return blsresponse
}

func (blsresponse *BLSresponse) SetPeerID(peerID string) *BLSresponse {
	blsresponse.PeerID = peerID
	return blsresponse
}

func (blsresponse *BLSresponse) Build() *BLSresponse {
	return blsresponse
}

// Get functions for the BLSresponse struct
func (blsresponse *BLSresponse) GetSignature() string {
	return blsresponse.Signature
}

func (blsresponse *BLSresponse) GetAgree() bool {
	return blsresponse.Agree
}

func (blsresponse *BLSresponse) GetPubKey() string {
	return blsresponse.PubKey
}

func (blsresponse *BLSresponse) GetPeerID() string {
	return blsresponse.PeerID
}
