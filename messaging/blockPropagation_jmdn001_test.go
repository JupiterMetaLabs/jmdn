package messaging

// Adversarial regression tests for JMDN-001 (P2P block validation bypass).
// These assert that the fail-closed gate (validateRemoteBlock /
// verifyBlockCertificate) rejects crafted blocks BEFORE any forwarding,
// mutation, or persistence, and accepts a well-formed block.

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"math/big"
	"os"
	"testing"

	BLS_Signer "gossipnode/AVC/BuddyNodes/MessagePassing/BLS_Signer"
	"gossipnode/config"
	"gossipnode/config/settings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// TestMain loads default node settings so the Security package's async logger
// (which reads settings during OTEL setup) can initialize under test.
//
// These tests call BLS_Signer.SignMessage, which lazily generates and PERSISTS a
// BLS keypair to config.BLSFile (a cwd-relative "./config/bls.json"). Under
// `go test ./messaging/` the cwd is the package dir, so the key would land at
// messaging/config/bls.json. We remove it afterward so a real private key is
// never left in the working tree where it could be committed.
func TestMain(m *testing.M) {
	_, _ = settings.Load()
	code := m.Run()
	_ = os.Remove("config/bls.json")
	_ = os.Remove("config/peer.json")
	_ = os.Remove("config") // best-effort: only succeeds if now empty
	os.Exit(code)
}

var testChainID = big.NewInt(1337)

// signedTx builds a config.Transaction with a real EIP-1559 signature from key.
func signedTx(t *testing.T, key *ecdsa.PrivateKey, nonce uint64) config.Transaction {
	t.Helper()
	from := crypto.PubkeyToAddress(key.PublicKey)
	to := common.HexToAddress("0x00000000000000000000000000000000000000ff")
	inner := &types.DynamicFeeTx{
		ChainID:   testChainID,
		Nonce:     nonce,
		To:        &to,
		Value:     big.NewInt(1),
		GasTipCap: big.NewInt(1),
		GasFeeCap: big.NewInt(1),
		Gas:       21000,
	}
	signer := types.LatestSignerForChainID(testChainID)
	signed, err := types.SignNewTx(key, signer, inner)
	if err != nil {
		t.Fatalf("sign tx: %v", err)
	}
	v, r, s := signed.RawSignatureValues()
	return config.Transaction{
		Hash:           signed.Hash(),
		From:           &from,
		To:             &to,
		Value:          big.NewInt(1),
		Type:           types.DynamicFeeTxType,
		ChainID:        testChainID,
		Nonce:          nonce,
		GasLimit:       21000,
		MaxFee:         big.NewInt(1),
		MaxPriorityFee: big.NewInt(1),
		V:              v, R: r, S: s,
	}
}

// yesCertData returns a Data map carrying a certificate of valid +1 votes, one
// per distinct PeerID supplied.
func yesCertData(t *testing.T, peerIDs ...string) map[string]string {
	t.Helper()
	var resps []BLS_Signer.BLSresponse
	for _, pid := range peerIDs {
		r, ok, err := BLS_Signer.SignMessage(1)
		if err != nil || !ok {
			t.Fatalf("bls sign vote: ok=%v err=%v", ok, err)
		}
		r.PeerID = pid
		resps = append(resps, r)
	}
	b, err := json.Marshal(resps)
	if err != nil {
		t.Fatalf("marshal cert: %v", err)
	}
	return map[string]string{"bls_results": string(b)}
}

func TestVerifyBlockCertificate(t *testing.T) {
	valid := yesCertData(t, "peerA", "peerB", "peerC") // 3 distinct == quorum

	cases := []struct {
		name       string
		data       map[string]string
		wantReason string // "" == accept
	}{
		{"omitted certificate", map[string]string{}, "no_certificate"},
		{"empty certificate", map[string]string{"bls_results": ""}, "no_certificate"},
		{"malformed json", map[string]string{"bls_results": "{not json"}, "malformed_certificate"},
		{"empty array", map[string]string{"bls_results": "[]"}, "no_certificate"},
		{"below quorum (2 of 3)", yesCertData(t, "peerA", "peerB"), "quorum_not_met"},
		{"ballot stuffing (same signer x3)", yesCertData(t, "peerA", "peerA", "peerA"), "quorum_not_met"},
		{"valid quorum", valid, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := config.BlockMessage{Block: &config.ZKBlock{}, Data: tc.data}
			rej := verifyBlockCertificate(msg)
			if tc.wantReason == "" {
				if rej != nil {
					t.Fatalf("expected accept, got reject reason=%s err=%v", rej.reason, rej.err)
				}
				return
			}
			if rej == nil {
				t.Fatalf("expected reject reason=%s, got accept", tc.wantReason)
			}
			if rej.reason != tc.wantReason {
				t.Fatalf("reason=%s, want %s (err=%v)", rej.reason, tc.wantReason, rej.err)
			}
		})
	}
}

func TestValidateRemoteBlock(t *testing.T) {
	ctx := context.Background()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	cert := yesCertData(t, "peerA", "peerB", "peerC")

	t.Run("happy path accepted", func(t *testing.T) {
		msg := config.BlockMessage{
			Block: &config.ZKBlock{Transactions: []config.Transaction{signedTx(t, key, 0)}},
			Data:  cert,
		}
		if rej := validateRemoteBlock(ctx, msg); rej != nil {
			t.Fatalf("expected accept, got reason=%s err=%v", rej.reason, rej.err)
		}
	})

	t.Run("nil block rejected", func(t *testing.T) {
		if rej := validateRemoteBlock(ctx, config.BlockMessage{}); rej == nil || rej.reason != "nil_block" {
			t.Fatalf("want nil_block, got %v", rej)
		}
	})

	t.Run("empty block rejected", func(t *testing.T) {
		msg := config.BlockMessage{Block: &config.ZKBlock{}, Data: cert}
		if rej := validateRemoteBlock(ctx, msg); rej == nil || rej.reason != "empty_block" {
			t.Fatalf("want empty_block, got %v", rej)
		}
	})

	t.Run("forged/tampered tx signature rejected", func(t *testing.T) {
		bad := signedTx(t, key, 0)
		bad.R = new(big.Int).Add(bad.R, big.NewInt(1)) // corrupt signature
		msg := config.BlockMessage{
			Block: &config.ZKBlock{Transactions: []config.Transaction{bad}},
			Data:  cert,
		}
		if rej := validateRemoteBlock(ctx, msg); rej == nil || rej.reason != "bad_signature" {
			t.Fatalf("want bad_signature, got %v", rej)
		}
	})

	t.Run("non-ascending sender nonce rejected", func(t *testing.T) {
		msg := config.BlockMessage{
			Block: &config.ZKBlock{Transactions: []config.Transaction{
				signedTx(t, key, 5),
				signedTx(t, key, 5), // duplicate nonce from same sender
			}},
			Data: cert,
		}
		if rej := validateRemoteBlock(ctx, msg); rej == nil || rej.reason != "bad_nonce" {
			t.Fatalf("want bad_nonce, got %v", rej)
		}
	})

	t.Run("valid txns but missing certificate rejected", func(t *testing.T) {
		msg := config.BlockMessage{
			Block: &config.ZKBlock{Transactions: []config.Transaction{signedTx(t, key, 0)}},
			Data:  map[string]string{},
		}
		if rej := validateRemoteBlock(ctx, msg); rej == nil || rej.reason != "no_certificate" {
			t.Fatalf("want no_certificate, got %v", rej)
		}
	})
}
