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
//   - CheckHeaderOrder (every path) rejects a timestamp before the parent's;
//     CheckHeaderPolicy (proposal intake and voting) rejects addresses other
//     than the ones configured for the height and a timestamp further in the
//     future than the configured drift.
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

// AddressRange is the coinbase and zkvm address every bound block carries
// from FromHeight up to the next range.
type AddressRange struct {
	FromHeight uint64
	Coinbase   common.Address
	ZKVM       common.Address
}

// HeaderBindingConfig is the node's header-binding policy.
type HeaderBindingConfig struct {
	// FromHeight is the first block height the rules apply to; 0 = off.
	FromHeight uint64
	// Addresses, sorted by FromHeight, give the addresses each bound block
	// must carry. Height-ranged so an address change can be scheduled at a
	// future height without disagreeing about earlier blocks.
	Addresses []AddressRange
	// MaxFutureDrift is how far past this node's clock a block's timestamp
	// may be when it is proposed or voted on; 0 = not checked.
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

// addressesFor returns the address range covering height, if any.
func (c HeaderBindingConfig) addressesFor(height uint64) (AddressRange, bool) {
	var out AddressRange
	found := false
	for _, r := range c.Addresses {
		if r.FromHeight > height {
			break
		}
		out, found = r, true
	}
	return out, found
}

// CheckHeaderOrder is the header rule every path applies, including the
// receive and sync paths of certified blocks: a bound block's timestamp may
// not be before its parent's. It is a pure function of the chain, so every
// node agrees on it whatever its configuration or clock. Nil below the
// activation height.
func CheckHeaderOrder(b *config.ZKBlock, parentTimestamp int64) error {
	if b == nil {
		return fmt.Errorf("header: nil block")
	}
	if !HeaderBound(b.BlockNumber) {
		return nil
	}
	if b.Timestamp < parentTimestamp {
		return fmt.Errorf("header: block %d timestamp %d is before its parent's %d",
			b.BlockNumber, b.Timestamp, parentTimestamp)
	}
	return nil
}

// CheckHeaderPolicy is the header rule applied before a block is certified,
// on proposal intake and before voting: the coinbase and zkvm addresses must
// be the configured ones for the block's height, and the timestamp may not
// be more than the configured drift past now. Certified blocks on the
// receive and sync paths are not re-checked against it: the committee
// already did, and a later address change or a skewed local clock must not
// make a node refuse its own chain. Nil below the activation height.
func CheckHeaderPolicy(b *config.ZKBlock, now time.Time) error {
	if b == nil {
		return fmt.Errorf("header: nil block")
	}
	c := currentHeaderBinding()
	if c.FromHeight == 0 || b.BlockNumber < c.FromHeight {
		return nil
	}
	want, ok := c.addressesFor(b.BlockNumber)
	if !ok {
		return fmt.Errorf("header: no coinbase / zkvm addresses configured for block %d", b.BlockNumber)
	}
	if b.CoinbaseAddr == nil || *b.CoinbaseAddr != want.Coinbase {
		return fmt.Errorf("header: block %d coinbase %s is not the configured %s",
			b.BlockNumber, addrString(b.CoinbaseAddr), want.Coinbase.Hex())
	}
	if b.ZKVMAddr == nil || *b.ZKVMAddr != want.ZKVM {
		return fmt.Errorf("header: block %d zkvm address %s is not the configured %s",
			b.BlockNumber, addrString(b.ZKVMAddr), want.ZKVM.Hex())
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
