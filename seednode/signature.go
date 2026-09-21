package seednode

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strings"

	blssign "gossipnode/AVC/BLS/bls-sign"
	"gossipnode/seednode/committee"
	peerpb "gossipnode/seednode/proto"

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

// peerRecordCanonicalMessage builds the exact identity-signed message for a peer
// record: peer_id | multiaddrs… | seq | status [ | bls_pub ]. bls_pub (lowercase,
// trimmed) is appended ONLY when set, so non-committee peers stay byte-for-byte
// backward-compatible. MUST match seedNodes ValidatePeerRecordSignature
// (pkg/peer/vrsSigner.go) exactly — a divergence silently breaks registration.
func peerRecordCanonicalMessage(peerRecord *peerpb.SignedPeerRecord) string {
	var messageParts []string
	messageParts = append(messageParts, peerRecord.PeerId)
	messageParts = append(messageParts, peerRecord.Multiaddrs...)
	messageParts = append(messageParts, fmt.Sprintf("%d", peerRecord.Seq))
	messageParts = append(messageParts, peerRecord.CurrentStatus.String())
	if peerRecord.BlsPub != "" {
		messageParts = append(messageParts, strings.ToLower(strings.TrimSpace(peerRecord.BlsPub)))
	}
	// reward_address (R1): appended AFTER bls_pub and ONLY when present, lowercased
	// + trimmed, so peers that carry neither, or only bls_pub, keep signing the
	// exact bytes they signed before. MUST match seedNodes vrsSigner.go
	// ValidatePeerRecordSignature in lockstep. The two optional trailing fields
	// are unambiguous because their alphabets are disjoint: a bls_pub is bare hex
	// (PoP-verified), a reward_address always begins "0x" (not valid hex input),
	// so no single string is acceptable as both — see the note in the seed's
	// reward_address.go / TestRewardAddress_DisjointFromBLSPub.
	if peerRecord.RewardAddress != "" {
		messageParts = append(messageParts, strings.ToLower(strings.TrimSpace(peerRecord.RewardAddress)))
	}
	return strings.Join(messageParts, "|")
}

// SignPeerRecord signs a peer record using the host's private key
func SignPeerRecord(peerRecord *peerpb.SignedPeerRecord, h host.Host) error {
	// Get the host's private key
	privKey := h.Peerstore().PrivKey(h.ID())
	if privKey == nil {
		return fmt.Errorf("no private key found for host")
	}

	// Create a message to sign (concatenate peer_id, multiaddrs, seq, status[, bls_pub])
	message := peerRecordCanonicalMessage(peerRecord)

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

// EmitCommitteeBLS gates whether this node advertises a committee BLS key at
// registration. Default ON now that the seed committee-source
// enforcement is live: the node registers with bls_pub + proof-of-possession so
// the seed can admit it to the epoch snapshot. Set JMDN_EMIT_COMMITTEE_BLS=0 to
// opt a node out (registers as before, not committee-eligible).
//
// Safe as a mixed-fleet flip: the live seed verifies bls_pub+PoP for records
// that carry them and still accepts records without (backward-compatible). Note
// emission is INDEPENDENT of consumption — jmdn only uses the snapshot for
// consensus once consensus.seed_authority_bls_pub is pinned; until then
// eligibility stays on the legacy getBuddy set.
var EmitCommitteeBLS = os.Getenv("JMDN_EMIT_COMMITTEE_BLS") != "0"

// AttachCommitteeBLS populates bls_pub (lowercase hex) and bls_pop (hex proof of
// possession) on the record from the node's persistent dela/bls key, so the seed
// can admit it to the epoch committee snapshot. No-op when EmitCommitteeBLS is
// off. MUST be called BEFORE SignPeerRecord so the identity signature covers
// bls_pub. The bls_pub is the SAME key used to sign consensus votes (loaded from
// config/bls.json), so committee membership and vote authentication agree.
// rewardAddress is this node's configured operator wallet, set once at startup
// from config.Consensus.RewardAddress via SetRewardAddress (the seednode package
// does not import config/settings, so main.go pushes the value in). Empty = the
// node claims no reward; AttachRewardAddress is then a no-op and registration
// stays byte-identical to a node without the field.
var rewardAddress string

// SetRewardAddress records this node's reward wallet (lowercased+trimmed). Call
// once at startup, before registering with the seed. Idempotent.
func SetRewardAddress(addr string) {
	rewardAddress = strings.ToLower(strings.TrimSpace(addr))
}

// AttachRewardAddress stamps the configured reward address onto a peer record
// BEFORE signing, so the identity signature covers it (mirrors AttachCommitteeBLS
// for bls_pub). No-op when unset. The seed enforces immutability: once bound, a
// later registration carrying a DIFFERENT address is rejected.
func AttachRewardAddress(peerRecord *peerpb.SignedPeerRecord) {
	if rewardAddress == "" {
		return
	}
	peerRecord.RewardAddress = rewardAddress
}

func AttachCommitteeBLS(peerRecord *peerpb.SignedPeerRecord) error {
	if !EmitCommitteeBLS {
		fmt.Printf("🔑 committee bls: emission DISABLED (JMDN_EMIT_COMMITTEE_BLS off) — registering WITHOUT bls_pub\n")
		return nil
	}
	priv, pub, err := blssign.GenerateBLSKeyPair() // persistent: loads/creates config/bls.json
	if err != nil {
		return fmt.Errorf("committee bls key: %w", err)
	}
	pubHex, popHex, err := committee.ProveBLSPossession(peerRecord.PeerId, priv, pub)
	if err != nil {
		return fmt.Errorf("committee bls proof-of-possession: %w", err)
	}
	peerRecord.BlsPub = pubHex
	peerRecord.BlsPop = popHex
	fmt.Printf("🔑 committee bls: attached bls_pub len=%d pop len=%d (emit=on)\n", len(pubHex), len(popHex))
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

// SignNeighbor signs a neighbor record using the host's private key
func SignNeighbor(neighbor *peerpb.PeerNeighbor, h host.Host) error {
	// Get the host's private key
	privKey := h.Peerstore().PrivKey(h.ID())
	if privKey == nil {
		return fmt.Errorf("no private key found for host")
	}

	// Create a message to sign (concatenate peer_id, neighbor_id, created_at, last_seen, is_active)
	var messageParts []string
	messageParts = append(messageParts, neighbor.PeerId)
	messageParts = append(messageParts, neighbor.NeighborId)
	messageParts = append(messageParts, fmt.Sprintf("%d", neighbor.CreatedAt))
	messageParts = append(messageParts, fmt.Sprintf("%d", neighbor.LastSeen))
	messageParts = append(messageParts, fmt.Sprintf("%t", neighbor.IsActive))

	message := strings.Join(messageParts, "|")

	// Hash the message
	hash := sha256.Sum256([]byte(message))

	// Sign the hash using libp2p crypto
	signature, err := privKey.Sign(hash[:])
	if err != nil {
		return fmt.Errorf("failed to sign neighbor message: %w", err)
	}

	// Convert libp2p signature to ECDSA format
	r := new(big.Int).SetBytes(signature[:32])
	s := new(big.Int).SetBytes(signature[32:64])

	// Calculate V component using a deterministic approach
	v := calculateVFromSignature(r, s, hash[:])

	// Convert to hex strings
	neighbor.R = hex.EncodeToString(r.Bytes())
	neighbor.S = hex.EncodeToString(s.Bytes())
	neighbor.V = hex.EncodeToString([]byte{v})

	return nil
}

// ─── Inbound signature verification (JMDN-H01) ───────────────────────────────
//
// Sign* above produces R/S/V hex on outbound records. Matching Validate*
// below MUST run on every production inbound path that trusts peer records,
// heartbeats, aliases, or neighbor edges before storing/using that data.
// Fail closed: missing or invalid signatures return an error; callers drop + log.
//
// Encoding note (audit M11): Sign* hex-encodes big.Int.Bytes() which can drop
// leading zeros. Verify left-pads R/S to 32 bytes so both padded and
// legacy-unpadded encodings verify.

// parseRSComponents decodes hex R/S into a fixed-width 64-byte libp2p ECDSA
// signature (R||S). Rejects empty or oversized components.
func parseRSComponents(rHex, sHex string) ([]byte, error) {
	if strings.TrimSpace(rHex) == "" || strings.TrimSpace(sHex) == "" {
		return nil, fmt.Errorf("missing R/S signature components")
	}
	rBytes, err := hex.DecodeString(rHex)
	if err != nil {
		return nil, fmt.Errorf("invalid R hex: %w", err)
	}
	sBytes, err := hex.DecodeString(sHex)
	if err != nil {
		return nil, fmt.Errorf("invalid S hex: %w", err)
	}
	if len(rBytes) == 0 || len(sBytes) == 0 || len(rBytes) > 32 || len(sBytes) > 32 {
		return nil, fmt.Errorf("R/S component length out of range (r=%d s=%d)", len(rBytes), len(sBytes))
	}
	sig := make([]byte, 64)
	copy(sig[32-len(rBytes):32], rBytes)
	copy(sig[64-len(sBytes):64], sBytes)
	return sig, nil
}

// verifyPeerIDSignature checks that message was signed by the libp2p identity
// key embedded in peerIDStr. V is accepted if present but not cryptographically
// required for libp2p Verify (R||S is the native form).
func verifyPeerIDSignature(peerIDStr, message, rHex, sHex, vHex string) error {
	_ = vHex // retained for wire compatibility / future low-S checks
	if strings.TrimSpace(peerIDStr) == "" {
		return fmt.Errorf("empty peer id")
	}
	pid, err := peer.Decode(peerIDStr)
	if err != nil {
		return fmt.Errorf("invalid peer id: %w", err)
	}
	pub, err := pid.ExtractPublicKey()
	if err != nil {
		return fmt.Errorf("extract public key: %w", err)
	}
	if pub == nil {
		return fmt.Errorf("peer id has no embedded public key")
	}
	sig, err := parseRSComponents(rHex, sHex)
	if err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(message))
	ok, err := pub.Verify(hash[:], sig)
	if err != nil {
		return fmt.Errorf("signature verify: %w", err)
	}
	if !ok {
		return fmt.Errorf("invalid signature for peer %s", peerIDStr)
	}
	return nil
}

// ValidatePeerRecordSignature verifies the identity signature on a peer record
// before the record's multiaddrs/status/bls_pub are trusted. Fail closed.
func ValidatePeerRecordSignature(peerRecord *peerpb.SignedPeerRecord) error {
	if peerRecord == nil {
		return fmt.Errorf("nil peer record")
	}
	message := peerRecordCanonicalMessage(peerRecord)
	return verifyPeerIDSignature(peerRecord.PeerId, message, peerRecord.R, peerRecord.S, peerRecord.V)
}

// ValidateHeartbeatSignature verifies the identity signature on a heartbeat
// before status/multiaddr updates are trusted. Fail closed.
func ValidateHeartbeatSignature(heartbeat *peerpb.HeartbeatMessage) error {
	if heartbeat == nil {
		return fmt.Errorf("nil heartbeat")
	}
	var messageParts []string
	messageParts = append(messageParts, heartbeat.PeerId)
	messageParts = append(messageParts, heartbeat.Status.String())
	messageParts = append(messageParts, heartbeat.Multiaddrs...)
	message := strings.Join(messageParts, "|")
	return verifyPeerIDSignature(heartbeat.PeerId, message, heartbeat.R, heartbeat.S, heartbeat.V)
}

// ValidateAliasSignature verifies the identity signature on a peer alias
// before the name→peer_id binding is trusted. Fail closed.
func ValidateAliasSignature(alias *peerpb.PeerAlias) error {
	if alias == nil {
		return fmt.Errorf("nil alias")
	}
	message := strings.Join([]string{alias.Name, alias.PeerId}, "|")
	return verifyPeerIDSignature(alias.PeerId, message, alias.R, alias.S, alias.V)
}

// ValidateNeighborSignature verifies the identity signature on a neighbor edge
// before topology is trusted. The signing peer is PeerId (the reporter).
// Fail closed.
func ValidateNeighborSignature(neighbor *peerpb.PeerNeighbor) error {
	if neighbor == nil {
		return fmt.Errorf("nil neighbor")
	}
	var messageParts []string
	messageParts = append(messageParts, neighbor.PeerId)
	messageParts = append(messageParts, neighbor.NeighborId)
	messageParts = append(messageParts, fmt.Sprintf("%d", neighbor.CreatedAt))
	messageParts = append(messageParts, fmt.Sprintf("%d", neighbor.LastSeen))
	messageParts = append(messageParts, fmt.Sprintf("%t", neighbor.IsActive))
	message := strings.Join(messageParts, "|")
	return verifyPeerIDSignature(neighbor.PeerId, message, neighbor.R, neighbor.S, neighbor.V)
}
