package Security

import (
	"bytes"
	"sync/atomic"

	"github.com/JupiterMetaLabs/avc/committee"
	"github.com/ethereum/go-ethereum/common"

	"gossipnode/config"
)

// headerBindingFromHeight is the first block height whose ConsensusHash also
// covers the apply-affecting header fields (see writeHeaderFields). 0 = never.
// Every node must use the same value: it changes the ConsensusHash of every
// block at or above it, so it is a coordinated, height-activated upgrade.
var headerBindingFromHeight atomic.Uint64

// SetHeaderBindingFromHeight sets the activation height (0 disables).
func SetHeaderBindingFromHeight(h uint64) { headerBindingFromHeight.Store(h) }

// HeaderBindingActive reports whether block height n uses the header-binding
// ConsensusHash preimage.
func HeaderBindingActive(n uint64) bool {
	from := headerBindingFromHeight.Load()
	return from > 0 && n >= from
}

// headerBindingDomain separates the header section from the fields before it.
const headerBindingDomain = "JMDT/consensus-header/v1"

// writeHeaderFields appends the header fields that change what applying the
// block does — fee and reward recipients, the account identities the block
// creates, and the header values receipts and the EVM read — so that a block
// whose certificate verifies cannot be re-published with any of them changed.
//
// Every field here must be set before ConsensusHash is computed on the
// proposer (Block.processZKBlock / the gRPC ingress recompute it after
// account-nonce stamping) and must round-trip through storage unchanged, or
// synced blocks stop matching their ConsensusHash.
//
// Not here: StateFingerprint, which the proposer computes while applying the
// block after the committee has signed (receivers recompute it and halt on a
// mismatch, and require it to be present — see messaging.checkConsensusBinding);
// and ExtraData, which no apply path reads and which does not survive storage
// (it would make every synced block with a non-empty value fail to verify).
func writeHeaderFields(buf *bytes.Buffer, b *config.ZKBlock) {
	committee.WriteField(buf, []byte(headerBindingDomain))
	writeOptAddr(buf, b.CoinbaseAddr)
	writeOptAddr(buf, b.ZKVMAddr)
	committee.WriteU64(buf, uint64(b.Timestamp))
	committee.WriteU64(buf, b.GasLimit)
	committee.WriteU64(buf, b.GasUsed)
	committee.WriteField(buf, b.StateRoot.Bytes())

	committee.WriteU64(buf, uint64(len(b.FeeRecipients)))
	for _, r := range b.FeeRecipients {
		committee.WriteField(buf, r.Addr.Bytes())
		committee.WriteU64(buf, r.Weight)
	}
	committee.WriteU64(buf, uint64(len(b.AccountNonces)))
	for _, n := range b.AccountNonces {
		committee.WriteField(buf, n.Address.Bytes())
		committee.WriteU64(buf, n.Nonce)
	}
}

func writeOptAddr(buf *bytes.Buffer, a *common.Address) {
	if a == nil {
		committee.WriteField(buf, nil)
		return
	}
	committee.WriteField(buf, a.Bytes())
}
