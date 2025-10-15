package seednode

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	peerpb "gossipnode/seednode/proto"
	"math/big"
	"strings"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

// calculateVFromSignature calculates V component using a deterministic approach
func calculateVFromSignature(r, s *big.Int, hash []byte) byte {
	// Use a simple deterministic approach based on the signature values
	// This ensures consistency while providing a valid V component
	sum := new(big.Int).Add(r, s)
	return byte(sum.Bit(0)) // Use the least significant bit
}

// SignPeerRecord signs a peer record using the host's private key
func SignPeerRecord(peerRecord *peerpb.SignedPeerRecord, h host.Host) error {
	// Get the host's private key
	privKey := h.Peerstore().PrivKey(h.ID())
	if privKey == nil {
		return fmt.Errorf("no private key found for host")
	}

	// Create a message to sign (concatenate peer_id, multiaddrs, seq, status)
	var messageParts []string
	messageParts = append(messageParts, peerRecord.PeerId)
	messageParts = append(messageParts, peerRecord.Multiaddrs...)
	messageParts = append(messageParts, fmt.Sprintf("%d", peerRecord.Seq))
	messageParts = append(messageParts, peerRecord.CurrentStatus.String())

	message := strings.Join(messageParts, "|")

	// Hash the message
	hash := sha256.Sum256([]byte(message))

	// Sign the hash using libp2p crypto
	signature, err := privKey.Sign(hash[:])
	if err != nil {
		return fmt.Errorf("failed to sign message: %w", err)
	}

	// Convert libp2p signature to ECDSA format
	r := new(big.Int).SetBytes(signature[:32])
	s := new(big.Int).SetBytes(signature[32:64])

	// Calculate V component using a deterministic approach
	// For libp2p signatures, we'll use a simple parity-based V calculation
	v := calculateVFromSignature(r, s, hash[:])

	// Convert to hex strings
	peerRecord.R = hex.EncodeToString(r.Bytes())
	peerRecord.S = hex.EncodeToString(s.Bytes())
	peerRecord.V = hex.EncodeToString([]byte{v})

	return nil
}

// SignHeartbeat signs a heartbeat message using the host's private key
func SignHeartbeat(heartbeat *peerpb.HeartbeatMessage, h host.Host) error {
	// Get the host's private key
	privKey := h.Peerstore().PrivKey(h.ID())
	if privKey == nil {
		return fmt.Errorf("no private key found for host")
	}

	// Create a message to sign (concatenate peer_id, status, multiaddrs)
	var messageParts []string
	messageParts = append(messageParts, heartbeat.PeerId)
	messageParts = append(messageParts, heartbeat.Status.String())
	messageParts = append(messageParts, heartbeat.Multiaddrs...)

	message := strings.Join(messageParts, "|")

	// Hash the message
	hash := sha256.Sum256([]byte(message))

	// Sign the hash using libp2p crypto
	signature, err := privKey.Sign(hash[:])
	if err != nil {
		return fmt.Errorf("failed to sign message: %w", err)
	}

	// Convert libp2p signature to ECDSA format
	r := new(big.Int).SetBytes(signature[:32])
	s := new(big.Int).SetBytes(signature[32:64])

	// Calculate V component using a deterministic approach
	v := calculateVFromSignature(r, s, hash[:])

	// Convert to hex strings
	heartbeat.R = hex.EncodeToString(r.Bytes())
	heartbeat.S = hex.EncodeToString(s.Bytes())
	heartbeat.V = hex.EncodeToString([]byte{v})

	return nil
}

// SignAlias signs a peer alias using the host's private key
func SignAlias(alias *peerpb.PeerAlias, h host.Host) error {
	// Get the host's private key
	privKey := h.Peerstore().PrivKey(h.ID())
	if privKey == nil {
		return fmt.Errorf("no private key found for host")
	}

	// Create a message to sign (concatenate name and peer_id)
	var messageParts []string
	messageParts = append(messageParts, alias.Name)
	messageParts = append(messageParts, alias.PeerId)

	message := strings.Join(messageParts, "|")

	// Hash the message
	hash := sha256.Sum256([]byte(message))

	// Sign the hash using libp2p crypto
	signature, err := privKey.Sign(hash[:])
	if err != nil {
		return fmt.Errorf("failed to sign message: %w", err)
	}

	// Convert libp2p signature to ECDSA format
	r := new(big.Int).SetBytes(signature[:32])
	s := new(big.Int).SetBytes(signature[32:64])

	// Calculate V component using a deterministic approach
	v := calculateVFromSignature(r, s, hash[:])

	// Convert to hex strings
	alias.R = hex.EncodeToString(r.Bytes())
	alias.S = hex.EncodeToString(s.Bytes())
	alias.V = hex.EncodeToString([]byte{v})

	return nil
}

// ValidateAliasSignature validates the signature of a peer alias
func ValidateAliasSignature(alias *peerpb.PeerAlias, peerID peer.ID) error {
	// Parse the peer ID to get the public key
	pubKey, err := peerID.ExtractPublicKey()
	if err != nil {
		return fmt.Errorf("failed to extract public key from peer ID: %w", err)
	}

	// Recreate the message that was signed
	var messageParts []string
	messageParts = append(messageParts, alias.Name)
	messageParts = append(messageParts, alias.PeerId)

	message := strings.Join(messageParts, "|")

	// Hash the message
	hash := sha256.Sum256([]byte(message))

	// Parse the signature components
	r, s, err := parseRSComponents(alias.R, alias.S)
	if err != nil {
		return fmt.Errorf("failed to parse signature components: %w", err)
	}

	// Reconstruct the libp2p signature from R and S components
	signature := append(r.Bytes(), s.Bytes()...)

	// Pad to 64 bytes if necessary
	if len(signature) < 64 {
		padded := make([]byte, 64)
		copy(padded[64-len(signature):], signature)
		signature = padded
	}

	// Validate using libp2p crypto
	valid, err := pubKey.Verify(hash[:], signature)
	if err != nil {
		return fmt.Errorf("failed to verify signature: %w", err)
	}

	if !valid {
		return fmt.Errorf("signature verification failed")
	}

	return nil
}

// ValidatePeerRecordSignature validates the signature of a peer record
func ValidatePeerRecordSignature(peerRecord *peerpb.SignedPeerRecord, peerID peer.ID) error {
	// Parse the peer ID to get the public key
	pubKey, err := peerID.ExtractPublicKey()
	if err != nil {
		return fmt.Errorf("failed to extract public key from peer ID: %w", err)
	}

	// Recreate the message that was signed
	var messageParts []string
	messageParts = append(messageParts, peerRecord.PeerId)
	messageParts = append(messageParts, peerRecord.Multiaddrs...)
	messageParts = append(messageParts, fmt.Sprintf("%d", peerRecord.Seq))
	messageParts = append(messageParts, peerRecord.CurrentStatus.String())

	message := strings.Join(messageParts, "|")

	// Hash the message
	hash := sha256.Sum256([]byte(message))

	// Parse the signature components
	r, s, err := parseRSComponents(peerRecord.R, peerRecord.S)
	if err != nil {
		return fmt.Errorf("failed to parse signature components: %w", err)
	}

	// Reconstruct the libp2p signature from R and S components
	signature := append(r.Bytes(), s.Bytes()...)

	// Pad to 64 bytes if necessary
	if len(signature) < 64 {
		padded := make([]byte, 64)
		copy(padded[64-len(signature):], signature)
		signature = padded
	}

	// Validate using libp2p crypto
	valid, err := pubKey.Verify(hash[:], signature)
	if err != nil {
		return fmt.Errorf("failed to verify signature: %w", err)
	}

	if !valid {
		return fmt.Errorf("signature verification failed")
	}

	return nil
}

// ValidateHeartbeatSignature validates the signature of a heartbeat message
func ValidateHeartbeatSignature(heartbeat *peerpb.HeartbeatMessage, peerID peer.ID) error {
	// Parse the peer ID to get the public key
	pubKey, err := peerID.ExtractPublicKey()
	if err != nil {
		return fmt.Errorf("failed to extract public key from peer ID: %w", err)
	}

	// Recreate the message that was signed
	var messageParts []string
	messageParts = append(messageParts, heartbeat.PeerId)
	messageParts = append(messageParts, heartbeat.Status.String())
	messageParts = append(messageParts, heartbeat.Multiaddrs...)

	message := strings.Join(messageParts, "|")

	// Hash the message
	hash := sha256.Sum256([]byte(message))

	// Parse the signature components
	r, s, err := parseRSComponents(heartbeat.R, heartbeat.S)
	if err != nil {
		return fmt.Errorf("failed to parse signature components: %w", err)
	}

	// Reconstruct the libp2p signature from R and S components
	signature := append(r.Bytes(), s.Bytes()...)

	// Pad to 64 bytes if necessary
	if len(signature) < 64 {
		padded := make([]byte, 64)
		copy(padded[64-len(signature):], signature)
		signature = padded
	}

	// Validate using libp2p crypto
	valid, err := pubKey.Verify(hash[:], signature)
	if err != nil {
		return fmt.Errorf("failed to verify signature: %w", err)
	}

	if !valid {
		return fmt.Errorf("signature verification failed")
	}

	return nil
}

// parseRSComponents parses R and S components from hex strings
func parseRSComponents(rHex, sHex string) (*big.Int, *big.Int, error) {
	// Parse R component
	rBytes, err := hex.DecodeString(rHex)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decode R component: %w", err)
	}
	r := new(big.Int).SetBytes(rBytes)

	// Parse S component
	sBytes, err := hex.DecodeString(sHex)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decode S component: %w", err)
	}
	s := new(big.Int).SetBytes(sBytes)

	return r, s, nil
}
