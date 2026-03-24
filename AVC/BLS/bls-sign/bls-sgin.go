package blssign

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"

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

// BLSVerify verifies (pubKey, message, signature).
func BLSVerify(pubKey, message, signature []byte) error {
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
func BLSFastAggregateVerify(pubKeys [][]byte, message, aggSig []byte) (bool, error) {
	if len(pubKeys) == 0 {
		return false, errors.New("no pubkeys provided")
	}
	pubs := make([]crypto.PublicKey, len(pubKeys))
	for i, p := range pubKeys {
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
func GenerateBLSKeyPair() ([]byte, []byte, error) {
	// 1) Try to load from config/bls.json
	type blsFile struct {
		PeerID  string `json:"peer_id,omitempty"`
		PrivKey string `json:"bls_priv"`
		PubKey  string `json:"bls_pub"`
	}

	if data, err := ioutil.ReadFile(config.BLSFile); err == nil {
		var bf blsFile
		if err := json.Unmarshal(data, &bf); err == nil && bf.PrivKey != "" && bf.PubKey != "" {
			if priv, err := base64.StdEncoding.DecodeString(bf.PrivKey); err == nil {
				if pub, err := base64.StdEncoding.DecodeString(bf.PubKey); err == nil {
					return priv, pub, nil
				}
			}
		}
		// If file exists but invalid, continue to regenerate below
	}

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

	// 3) Persist to config/bls.json (best-effort)
	_ = os.MkdirAll(filepath.Dir(config.BLSFile), 0o750)
	// Try to read peer_id from peer.json (optional)
	type peerFile struct {
		PeerID string `json:"peer_id"`
	}
	var pf peerFile
	if pdata, err := ioutil.ReadFile(config.PeerFile); err == nil {
		_ = json.Unmarshal(pdata, &pf)
	}

	bf := blsFile{
		PeerID:  pf.PeerID,
		PrivKey: base64.StdEncoding.EncodeToString(privBytes),
		PubKey:  base64.StdEncoding.EncodeToString(pubBytes),
	}
	if out, err := json.MarshalIndent(bf, "", "  "); err == nil {
		_ = ioutil.WriteFile(config.BLSFile, out, 0o600)
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

// MultiSigManager collects/validates shares, aggregates, and verifies.
type MultiSigManager struct {
	mu              sync.RWMutex
	signers         map[string]*Signer          // id -> signer
	threshold       int                         // minimum signatures required
	signatureShares map[string][]SignatureShare // messageKey -> shares
}

// NewMultiSigManager creates a new manager with a threshold.
func NewMultiSigManager(threshold int) *MultiSigManager {
	return &MultiSigManager{
		signers:         make(map[string]*Signer),
		threshold:       threshold,
		signatureShares: make(map[string][]SignatureShare),
	}
}

// AddSigner registers a signer (with pubkey; private key only if this process will sign).
func (m *MultiSigManager) AddSigner(id string, publicKey, privateKey []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.signers[id]; exists {
		return fmt.Errorf("signer %s already exists", id)
	}
	m.signers[id] = &Signer{ID: id, PublicKey: publicKey, PrivateKey: privateKey}
	return nil
}

// SignAsParticipant produces a signature for the given message using signer's private key.
func (m *MultiSigManager) SignAsParticipant(signerID string, message []byte) ([]byte, error) {
	m.mu.RLock()
	s, ok := m.signers[signerID]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown signer %s", signerID)
	}
	if s.PrivateKey == nil {
		return nil, fmt.Errorf("signer %s has no private key", signerID)
	}
	return BLSSign(s.PrivateKey, message)
}

// AddSignatureShare verifies and records a signature share for messageKey.
func (m *MultiSigManager) AddSignatureShare(messageKey string, share SignatureShare) error {
	// Verify sig strictly before recording
	if err := BLSVerify(share.PublicKey, share.Message, share.Signature); err != nil {
		return fmt.Errorf("invalid signature from %s: %w", share.SignerID, err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Prevent duplicate signer for the same messageKey
	for _, s := range m.signatureShares[messageKey] {
		if s.SignerID == share.SignerID {
			return fmt.Errorf("signer %s already submitted", share.SignerID)
		}
	}

	m.signatureShares[messageKey] = append(m.signatureShares[messageKey], share)
	return nil
}

// HasReachedThreshold returns true if we have >= threshold shares for messageKey.
func (m *MultiSigManager) HasReachedThreshold(messageKey string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.signatureShares[messageKey]) >= m.threshold
}

// AggregateSignatures aggregates all collected shares for messageKey (requires threshold).
func (m *MultiSigManager) AggregateSignatures(messageKey string) (aggSig []byte, pubKeys [][]byte, msg []byte, err error) {
	m.mu.RLock()
	shares := append([]SignatureShare(nil), m.signatureShares[messageKey]...) // copy
	m.mu.RUnlock()

	if len(shares) < m.threshold {
		return nil, nil, nil, fmt.Errorf("not enough signatures: got %d, need %d", len(shares), m.threshold)
	}

	// Defensive: ensure all shares are for the exact same message bytes
	msg = shares[0].Message
	for i := 1; i < len(shares); i++ {
		if !bytesEqual(shares[i].Message, msg) {
			return nil, nil, nil, errors.New("shares contain different message bytes")
		}
	}

	sigs := make([][]byte, len(shares))
	pubKeys = make([][]byte, len(shares))
	for i, s := range shares {
		sigs[i] = s.Signature
		pubKeys[i] = s.PublicKey
	}

	agg, err := BLSAggregate(sigs...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("aggregate failed: %w", err)
	}
	return agg, pubKeys, msg, nil
}

// VerifyAggregatedSignature verifies an aggregated signature for messageKey.
func (m *MultiSigManager) VerifyAggregatedSignature(messageKey string, aggregatedSig []byte) (bool, error) {
	m.mu.RLock()
	shares := append([]SignatureShare(nil), m.signatureShares[messageKey]...) // copy
	m.mu.RUnlock()

	if len(shares) < m.threshold {
		return false, fmt.Errorf("not enough signatures for verification")
	}

	// Same-message guard
	msg := shares[0].Message
	for i := 1; i < len(shares); i++ {
		if !bytesEqual(shares[i].Message, msg) {
			return false, errors.New("shares contain different message bytes")
		}
	}

	pubKeys := make([][]byte, len(shares))
	for i, s := range shares {
		pubKeys[i] = s.PublicKey
	}
	return BLSFastAggregateVerify(pubKeys, msg, aggregatedSig)
}

// SignedSignerIDs returns signer IDs that have already submitted for messageKey.
func (m *MultiSigManager) SignedSignerIDs(messageKey string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	shares := m.signatureShares[messageKey]
	out := make([]string, 0, len(shares))
	for _, s := range shares {
		out = append(out, s.SignerID)
	}
	return out
}

// UnsignedSignerIDs returns signer IDs that haven't submitted yet for messageKey.
func (m *MultiSigManager) UnsignedSignerIDs(messageKey string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	seen := map[string]struct{}{}
	for _, s := range m.signatureShares[messageKey] {
		seen[s.SignerID] = struct{}{}
	}
	var out []string
	for id := range m.signers {
		if _, ok := seen[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}

// GetCurrentSignatureCount returns how many shares we have for messageKey.
func (m *MultiSigManager) GetCurrentSignatureCount(messageKey string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.signatureShares[messageKey])
}

// Threshold returns the configured threshold.
func (m *MultiSigManager) Threshold() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.threshold
}

// Helper to build a stable key for the message map (sha256 hex of the message).
// __DEAD_CODE_AUDIT_PUBLIC__
func MessageKey(message []byte) string {
	sum := sha256.Sum256(message)
	return hex.EncodeToString(sum[:])
}

// tiny local helper to avoid importing bytes for one use
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (m *MultiSigManager) RemoveSigner(signerID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.signers, signerID)
}

func (m *MultiSigManager) GetSigner(signerID string) (*Signer, error) {
	if signer, ok := m.signers[signerID]; ok {
		return signer, nil
	}
	return nil, fmt.Errorf("signer %s not found", signerID)
}
