package DID

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

// DIDRegistrationMessage is the text a registrant signs with personal_sign
// (EIP-191) to prove control of address. Binding the DID and chain ID stops a
// signature from being reused for another DID or on another network.
func DIDRegistrationMessage(did string, address common.Address, chainID uint64) string {
	return fmt.Sprintf("JMDT DID registration\nDID: %s\nAddress: %s\nChain ID: %d", did, address.Hex(), chainID)
}

var errBadDIDSignature = errors.New("signature does not prove control of public key")

// verifyDIDOwnership checks that sigHex is a personal_sign by address over the
// registration message for did on chainID.
func verifyDIDOwnership(did string, address common.Address, sigHex string, chainID uint64) error {
	if strings.TrimSpace(sigHex) == "" {
		return errors.New("signature is required")
	}
	sig, err := hexutil.Decode(sigHex)
	if err != nil || len(sig) != crypto.SignatureLength {
		return errors.New("signature must be 65 bytes of 0x-prefixed hex")
	}
	sig = append([]byte(nil), sig...)
	if sig[crypto.RecoveryIDOffset] >= 27 {
		sig[crypto.RecoveryIDOffset] -= 27
	}
	hash := accounts.TextHash([]byte(DIDRegistrationMessage(did, address, chainID)))
	pub, err := crypto.SigToPub(hash, sig)
	if err != nil {
		return errBadDIDSignature
	}
	if crypto.PubkeyToAddress(*pub) != address {
		return errBadDIDSignature
	}
	return nil
}
