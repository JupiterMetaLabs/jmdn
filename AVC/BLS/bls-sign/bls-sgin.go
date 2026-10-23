package blssign

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gossipnode/config"

	"go.dedis.ch/dela/crypto"
	"go.dedis.ch/dela/crypto/bls"
)

//
// ---- Minimal BLS helpers (only what multisig needs) ----
//

// BLSSign signs a message using the provided private key bytes.
func BLSSign(privateKey, message []byte) ([]byte, error) {
	s, err := bls.NewSignerFromBytes(privateKey)
	if err != nil {
		return nil, err
	}
	signer := s.(bls.Signer)
	sig, err := signer.Sign(message)
	if err != nil {
		return nil, err
	}
	return sig.MarshalBinary()
}

// PublicKeyLen and SignatureLen are the exact encoded sizes on this curve
// (BN256: a public key is a G2 point, a signature a G1 point).
const (
	PublicKeyLen = 128
	SignatureLen = 64
)

var (
	// ErrBadPublicKey rejects a key of the wrong length or the identity point.
	ErrBadPublicKey = errors.New("bls: public key must be 128 bytes and not the identity point")
	// ErrBadSignature rejects a signature of the wrong length or the identity point.
	ErrBadSignature = errors.New("bls: signature must be 64 bytes and not the identity point")
)

// checkPoint enforces the exact encoded length and rejects the identity point,
// which encodes as all zeros. The identity key paired with the identity
// signature verifies EVERY message, and the decoder ignores trailing bytes, so
// without this a malformed key or signature could pass as valid. Every caller
// (votes, committee snapshots and their PoP, checkpoints, entropy aggregates)
// gets the check (2026-10-22 audit, A-6).
func checkPoint(b []byte, n int, errBad error) error {
	if len(b) != n {
		return errBad
	}
	for _, x := range b {
		if x != 0 {
			return nil
		}
	}
	return errBad
}

// BLSVerify verifies (pubKey, message, signature).
func BLSVerify(pubKey, message, signature []byte) error {
	if err := checkPoint(pubKey, PublicKeyLen, ErrBadPublicKey); err != nil {
		return err
	}
	if err := checkPoint(signature, SignatureLen, ErrBadSignature); err != nil {
		return err
	}
	pk, err := bls.NewPublicKey(pubKey)
	if err != nil {
		return err
	}
	sig := bls.NewSignature(signature)
	return pk.Verify(message, sig)
}

// BLSAggregate aggregates multiple BLS signatures.
func BLSAggregate(signatures ...[]byte) ([]byte, error) {
	if len(signatures) == 0 {
		return nil, errors.New("no signatures to aggregate")
	}
	sigs := make([]crypto.Signature, len(signatures))
	for i, buf := range signatures {
		sigs[i] = bls.NewSignature(buf)
	}
	tmp := bls.Generate().(bls.Signer)
	aggSig, err := tmp.Aggregate(sigs...)
	if err != nil {
		return nil, err
	}
	return aggSig.(bls.Signature).MarshalBinary()
}

// BLSFastAggregateVerify verifies aggregated signature against all pubkeys for the same message.
//
// Plain BLS aggregation is only sound if every key was registered with a proof
// of possession; otherwise a rogue key lets its holder forge the aggregate
// alone. The committee source enforces that (seednode/committee PoP check).
func BLSFastAggregateVerify(pubKeys [][]byte, message, aggSig []byte) (bool, error) {
	if len(pubKeys) == 0 {
		return false, errors.New("no pubkeys provided")
	}
	if err := checkPoint(aggSig, SignatureLen, ErrBadSignature); err != nil {
		return false, err
	}
	pubs := make([]crypto.PublicKey, len(pubKeys))
	for i, p := range pubKeys {
		if err := checkPoint(p, PublicKeyLen, ErrBadPublicKey); err != nil {
			return false, err
		}
		pk, err := bls.NewPublicKey(p)
		if err != nil {
			return false, err
		}
		pubs[i] = pk
	}
	tmp := bls.Generate().(bls.Signer)
	verifier, err := tmp.GetVerifierFactory().FromArray(pubs)
	if err != nil {
		return false, err
	}
	sig := bls.NewSignature(aggSig)
	if err := verifier.Verify(message, sig); err != nil {
		return false, nil
	}
	return true, nil
}

// GenerateBLSKeyPair generates and returns a private and public key (as byte slices) for BLS.
// blsKeyFile is the on-disk committee BLS keypair (config/bls.json).
type blsKeyFile struct {
	PeerID  string `json:"peer_id,omitempty"`
	PrivKey string `json:"bls_priv"`
	PubKey  string `json:"bls_pub"`
}

// LoadBLSKeyPair loads the persistent committee BLS keypair from config/bls.json
// WITHOUT generating one. It returns an error when the file is absent or
// invalid. The consensus vote-signing path uses this (never auto-mint): once
// committee membership binds peer_id→bls_pub, a node that silently minted a
// fresh key would self-exclude from the committee. Provision the key explicitly
// (or set JMDN_BLS_AUTOGEN=1 for dev/first-boot) instead.
// BLSKeyFilePath resolves the BLS key file location. It honors the
// JMDN_BLS_KEY_FILE environment variable (an absolute path) so the location does
// NOT depend on the process working directory. This matters because the default
// config.BLSFile is the RELATIVE path "./config/bls.json", and nodes run as a
// systemd service default their working directory to "/", where "./config/bls.json"
// resolves to "/config/bls.json" — not the provisioned key at, e.g.,
// /opt/jmdn/config/bls.json. Set JMDN_BLS_KEY_FILE=/opt/jmdn/config/bls.json (or
// WorkingDirectory=/opt/jmdn in the unit) to point at the real key.
func BLSKeyFilePath() string {
	if p := strings.TrimSpace(os.Getenv("JMDN_BLS_KEY_FILE")); p != "" {
		return p
	}
	return config.BLSFile
}

func LoadBLSKeyPair() ([]byte, []byte, error) {
	path := BLSKeyFilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("bls key not provisioned: cannot read %s: %w", path, err)
	}
	var bf blsKeyFile
	if err := json.Unmarshal(data, &bf); err != nil || bf.PrivKey == "" || bf.PubKey == "" {
		return nil, nil, fmt.Errorf("bls key file %s is invalid or incomplete", path)
	}
	priv, err := base64.StdEncoding.DecodeString(bf.PrivKey)
	if err != nil {
		return nil, nil, fmt.Errorf("bls key file %s: bad private key: %w", path, err)
	}
	pub, err := base64.StdEncoding.DecodeString(bf.PubKey)
	if err != nil {
		return nil, nil, fmt.Errorf("bls key file %s: bad public key: %w", path, err)
	}
	return priv, pub, nil
}

func GenerateBLSKeyPair() ([]byte, []byte, error) {
	// 1) Try to load the persisted key (no mint).
	if priv, pub, err := LoadBLSKeyPair(); err == nil {
		return priv, pub, nil
	}
	// If file exists but invalid, continue to regenerate below
	type blsFile = blsKeyFile

	// 2) Not found or invalid → generate new keypair
	signer := bls.Generate().(bls.Signer)
	privBytes, err := signer.MarshalBinary()
	if err != nil {
		return nil, nil, err
	}
	pub := signer.GetPublicKey()
	pubBytes, err := pub.MarshalBinary()
	if err != nil {
		return nil, nil, err
	}

	// 3) Persist to the resolved key path (best-effort)
	keyPath := BLSKeyFilePath()
	_ = os.MkdirAll(filepath.Dir(keyPath), 0o750)
	// Try to read peer_id from peer.json (optional)
	type peerFile struct {
		PeerID string `json:"peer_id"`
	}
	var pf peerFile
	if pdata, err := os.ReadFile(config.PeerFile); err == nil {
		_ = json.Unmarshal(pdata, &pf)
	}

	bf := blsFile{
		PeerID:  pf.PeerID,
		PrivKey: base64.StdEncoding.EncodeToString(privBytes),
		PubKey:  base64.StdEncoding.EncodeToString(pubBytes),
	}
	if out, err := json.MarshalIndent(bf, "", "  "); err == nil {
		_ = os.WriteFile(keyPath, out, 0o600)
	}

	return privBytes, pubBytes, nil
}

// GenerateBLSKeyPairFromRawPrivKey derives a deterministic BLS keypair from a raw private key byte slice.
// The input should be the raw bytes of the node's private key (e.g., libp2p private key bytes).
// We derive a 32-byte seed using SHA-256 and initialize a BLS signer from that seed.
func GenerateBLSKeyPairFromRawPrivKey(rawPriv []byte) ([]byte, []byte, error) {
	if len(rawPriv) == 0 {
		return nil, nil, fmt.Errorf("raw private key is empty")
	}

	seed := sha256.Sum256(rawPriv)
	s, err := bls.NewSignerFromBytes(seed[:])
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create BLS signer from seed: %w", err)
	}
	signer := s.(bls.Signer)

	privBytes, err := signer.MarshalBinary()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal BLS private key: %w", err)
	}
	pub := signer.GetPublicKey()
	pubBytes, err := pub.MarshalBinary()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal BLS public key: %w", err)
	}
	return privBytes, pubBytes, nil
}

//
// ---- Core multisig-only types & manager ----
//

type Signer struct {
	ID         string
	PublicKey  []byte
	PrivateKey []byte // optional; only for local signer that can produce signatures
}

type SignatureShare struct {
	SignerID  string
	PublicKey []byte
	Signature []byte
	Message   []byte
}
