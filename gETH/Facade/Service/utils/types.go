package Utils

import "github.com/ethereum/go-ethereum/common"

type DIDDoc struct{
	Address common.Address `json:"address"`
	DIDAddress string `json:"did,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}