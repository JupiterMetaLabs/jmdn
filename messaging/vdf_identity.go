package messaging

// VDF fleet-parameter identity — audit D-39 (difficulty T has no fleet-agreement
// check) + D-54 (the VDF group is bound only implicitly).
//
// The accept path (entropy_vdf_accept.go) rejected `proof.T != local difficulty`
// and let `vdf.Verify` re-derive the challenge from the LOCAL group. So:
//   - a group/modulus disagreement surfaced only as a generic verify failure
//     that named nothing (D-54), and
//   - difficulty T was a per-host env var, gossiped/persisted/hashed nowhere, so
//     two honest nodes on different T each rejected the other's proofs and
//     neither could tell locally which one was wrong (D-39).
//
// This binds all three — group name, modulus digest, difficulty T — into one
// digest. The proposer stamps it on the epoch-boundary block
// (config.ZKBlock.VdfParamsDigest); every adopter compares the block's identity
// against its own and, on mismatch, names the cause with both sides printed.
// Because T is inside the identity, a divergent T is detected here too — D-39 and
// D-54 are the same check.

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
)

const vdfIdentityDomain = "jmdn/vdf-identity/v1"

// VDFIdentityDigest returns the fleet-checked VDF parameter identity:
//
//	sha256( domain ‖ u64:len(group) ‖ group ‖ u64:len(digest) ‖ digest ‖ u64:T )
//
// Length-prefixing makes the concatenation injective, so a group name that
// happens to end in the modulus-digest prefix cannot collide with a different
// split. modulusDigestHex is the lowercase hex SHA-256 of the modulus's
// big-endian bytes (avc/vdf.ModulusDigest — the same preimage the pins use).
//
// Returns "" for an empty group name or digest: a caller without both has
// nothing to bind, and "" is the Stage-1 sentinel the accept-path check skips on.
func VDFIdentityDigest(groupName, modulusDigestHex string, difficultyT uint64) string {
	groupName = strings.TrimSpace(groupName)
	modulusDigestHex = strings.ToLower(strings.TrimSpace(modulusDigestHex))
	if groupName == "" || modulusDigestHex == "" {
		return ""
	}
	h := sha256.New()
	h.Write([]byte(vdfIdentityDomain))
	var u [8]byte
	binary.BigEndian.PutUint64(u[:], uint64(len(groupName)))
	h.Write(u[:])
	h.Write([]byte(groupName))
	binary.BigEndian.PutUint64(u[:], uint64(len(modulusDigestHex)))
	h.Write(u[:])
	h.Write([]byte(modulusDigestHex))
	binary.BigEndian.PutUint64(u[:], difficultyT)
	h.Write(u[:])
	return hex.EncodeToString(h.Sum(nil))
}

var (
	localVDFIdentityMu  sync.RWMutex
	localVDFIdentityVal string
)

// SetLocalVDFIdentity records this node's VDF identity, computed at beacon
// install from the configured group + modulus + difficulty. Left "" on Stage-1
// (no beacon), where the accept-path identity check does not fire.
func SetLocalVDFIdentity(id string) {
	localVDFIdentityMu.Lock()
	localVDFIdentityVal = id
	localVDFIdentityMu.Unlock()
}

// LocalVDFIdentity returns the identity set by SetLocalVDFIdentity, or "".
func LocalVDFIdentity() string {
	localVDFIdentityMu.RLock()
	defer localVDFIdentityMu.RUnlock()
	return localVDFIdentityVal
}

// ErrVDFIdentityMismatch — a boundary block's declared VDF identity differs from
// this node's. This is the error D-54 said was missing: it names the cause
// (group ‖ modulus ‖ T), rather than surfacing as a nameless vdf.Verify failure.
var ErrVDFIdentityMismatch = errors.New("entropy: VDF parameter identity mismatch (group/modulus/T disagreement across the fleet)")
