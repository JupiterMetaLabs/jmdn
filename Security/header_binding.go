package Security

// Header binding (consensus.header_binding_from_height).
//
// Before the activation height, BlockHash covers the transactions only and
// the committee certificate does not cover CoinbaseAddr, ZKVMAddr,
// Timestamp or FeeRecipients, so whoever supplies a block can change them.
// From the activation height on:
//
//   - BlockHash = Keccak256(headerHashDomain || txsHash || coinbase || zkvm
//     || u64 timestamp), where txsHash is the transactions-only hash
//     (Keccak256 over the concatenated tx hashes) and a nil address encodes
//     as 20 zero bytes. The orchestrator builds it with the same formula.
//   - ConsensusHash also covers CoinbaseAddr, ZKVMAddr, Timestamp and
//     FeeRecipients (consensus_fields_hash.go).
//   - CheckHeaderFields rejects a block whose addresses differ from the
//     configured ones, whose timestamp is before its parent's, or whose
//     timestamp is further in the future than the configured drift.
//
// The activation height is fleet-agreed: every node and the orchestrator
// must carry the same value, set to a height the chain has not reached.

import (
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"gossipnode/config"
)

const headerHashDomain = "jmdn/block-header-hash/v1"

// HeaderBindingConfig is the node's header-binding policy.
type HeaderBindingConfig struct {
	// FromHeight is the first block height the rules apply to; 0 = off.
	FromHeight uint64
	// Coinbase and ZKVM are the addresses every bound block must carry; nil
	// = not checked.
	Coinbase *common.Address
	ZKVM     *common.Address
	// MaxFutureDrift is how far past this node's clock a bound block's
	// timestamp may be; 0 = not checked.
	MaxFutureDrift time.Duration
}

var (
	headerBindingMu sync.RWMutex
	headerBinding   HeaderBindingConfig
)

// SetHeaderBinding installs the policy. Called once by main after settings
// load; tests call it directly.
func SetHeaderBinding(c HeaderBindingConfig) {
	headerBindingMu.Lock()
	defer headerBindingMu.Unlock()
	headerBinding = c
}

func currentHeaderBinding() HeaderBindingConfig {
	headerBindingMu.RLock()
	defer headerBindingMu.RUnlock()
	return headerBinding
}

// HeaderBound reports whether the header-binding rules apply at height.
func HeaderBound(height uint64) bool {
	from := currentHeaderBinding().FromHeight
	return from > 0 && height >= from
}

func addressBytes(a *common.Address) []byte {
	if a == nil {
		return make([]byte, common.AddressLength)
	}
	return a.Bytes()
}

// HeaderBlockHash is the bound BlockHash: Keccak256(domain || txsHash ||
// coinbase || zkvm || u64 big-endian timestamp). Every field after the
// domain has a fixed width, so the preimage is unambiguous.
func HeaderBlockHash(txsHash common.Hash, coinbase, zkvm *common.Address, timestamp int64) common.Hash {
	buf := make([]byte, 0, len(headerHashDomain)+32+2*common.AddressLength+8)
	buf = append(buf, headerHashDomain...)
	buf = append(buf, txsHash.Bytes()...)
	buf = append(buf, addressBytes(coinbase)...)
	buf = append(buf, addressBytes(zkvm)...)
	buf = binary.BigEndian.AppendUint64(buf, uint64(timestamp))
	return crypto.Keccak256Hash(buf)
}

// ExpectedBlockHash returns the BlockHash block must carry, given its
// transactions-only hash: txsHash itself below the activation height,
// HeaderBlockHash from it on. Every BlockHash check goes through this.
func ExpectedBlockHash(b *config.ZKBlock, txsHash common.Hash) common.Hash {
	if b == nil || !HeaderBound(b.BlockNumber) {
		return txsHash
	}
	return HeaderBlockHash(txsHash, b.CoinbaseAddr, b.ZKVMAddr, b.Timestamp)
}

// CheckHeaderFields applies the header rules to a bound block: configured
// coinbase and ZKVM addresses, a timestamp not before parentTimestamp, and
// not more than the configured drift past now. Nil below the activation
// height.
func CheckHeaderFields(b *config.ZKBlock, parentTimestamp int64, now time.Time) error {
	if b == nil {
		return fmt.Errorf("header: nil block")
	}
	c := currentHeaderBinding()
	if c.FromHeight == 0 || b.BlockNumber < c.FromHeight {
		return nil
	}
	if c.Coinbase != nil && (b.CoinbaseAddr == nil || *b.CoinbaseAddr != *c.Coinbase) {
		return fmt.Errorf("header: block %d coinbase %s is not the configured %s",
			b.BlockNumber, addrString(b.CoinbaseAddr), c.Coinbase.Hex())
	}
	if c.ZKVM != nil && (b.ZKVMAddr == nil || *b.ZKVMAddr != *c.ZKVM) {
		return fmt.Errorf("header: block %d zkvm address %s is not the configured %s",
			b.BlockNumber, addrString(b.ZKVMAddr), c.ZKVM.Hex())
	}
	if b.Timestamp < parentTimestamp {
		return fmt.Errorf("header: block %d timestamp %d is before its parent's %d",
			b.BlockNumber, b.Timestamp, parentTimestamp)
	}
	if c.MaxFutureDrift > 0 {
		if limit := now.Add(c.MaxFutureDrift).Unix(); b.Timestamp > limit {
			return fmt.Errorf("header: block %d timestamp %d is more than %s ahead of this node's clock",
				b.BlockNumber, b.Timestamp, c.MaxFutureDrift)
		}
	}
	return nil
}

func addrString(a *common.Address) string {
	if a == nil {
		return "<none>"
	}
	return a.Hex()
}
