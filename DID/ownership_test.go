package DID

import (
	"testing"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

func signDID(t *testing.T, did string, chainID uint64) (common.Address, string) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	addr := crypto.PubkeyToAddress(key.PublicKey)
	sig, err := crypto.Sign(accounts.TextHash([]byte(DIDRegistrationMessage(did, addr, chainID))), key)
	if err != nil {
		t.Fatal(err)
	}
	sig[64] += 27 // wallets return v in {27,28}
	return addr, hexutil.Encode(sig)
}

func TestVerifyDIDOwnership(t *testing.T) {
	const did, chain = "did:jmdt:alice", uint64(8000800)
	addr, sig := signDID(t, did, chain)

	if err := verifyDIDOwnership(did, addr, sig, chain); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if err := verifyDIDOwnership("did:jmdt:mallory", addr, sig, chain); err == nil {
		t.Fatal("signature reused for another DID")
	}
	if err := verifyDIDOwnership(did, addr, sig, 7000700); err == nil {
		t.Fatal("signature reused on another chain")
	}
	other := common.HexToAddress("0x1111111111111111111111111111111111111111")
	if err := verifyDIDOwnership(did, other, sig, chain); err == nil {
		t.Fatal("signature accepted for an address it does not belong to")
	}
	if err := verifyDIDOwnership(did, addr, "", chain); err == nil {
		t.Fatal("missing signature accepted")
	}
	if err := verifyDIDOwnership(did, addr, "0x1234", chain); err == nil {
		t.Fatal("short signature accepted")
	}
}
