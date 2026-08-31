package DB_OPs

// thebe_conversions.go — conversions between thebegateway record types and config domain types.
// Used by the ThebeHandle-based reimplementations of immuclient.go and account_immuclient.go.

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"gossipnode/DB_OPs/thebegateway"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
)

// blockRecordToZKBlock converts a thebegateway.BlockRecord back to a config.ZKBlock.
// Transaction slices are NOT populated here (use GetTransactionsByBlock separately).
func blockRecordToZKBlock(r *thebegateway.BlockRecord) (*config.ZKBlock, error) {
	if r == nil {
		return nil, fmt.Errorf("blockRecordToZKBlock: nil record")
	}

	var coinbase *common.Address
	if r.CoinbaseAddr != "" {
		a := common.HexToAddress(r.CoinbaseAddr)
		coinbase = &a
	}
	var zkvm *common.Address
	if r.ZKVMAddr != "" {
		a := common.HexToAddress(r.ZKVMAddr)
		zkvm = &a
	}

	extraData := ""
	if ed, ok := r.ExtraData["extra_data"]; ok {
		if s, ok2 := ed.(string); ok2 {
			extraData = s
		}
	}

	// CommitteeCertificate: hydrate the persisted committee vote set (JSON) from
	// ExtraData so a synced/read block can re-verify its certificate (P-cert /
	// ThebeSync). Absent on the legacy prefix (blocks predating P-cert).
	committeeCert := ""
	if cc, ok := r.ExtraData["committee_certificate"]; ok {
		if s, ok2 := cc.(string); ok2 {
			committeeCert = s
		}
	}

	// StateFingerprint: hydrate the persisted P2.5 fingerprint so a synced/read
	// block carries it and ProcessBlockTransactions compares (halts on divergence)
	// rather than re-stamping. Absent on blocks stored before P2 persistence.
	stateFingerprint := ""
	if sf, ok := r.ExtraData["state_fingerprint"]; ok {
		if s, ok2 := sf.(string); ok2 {
			stateFingerprint = s
		}
	}

	// AccountNonces: hydrate the persisted canonical ART identities so a synced/read
	// block carries them and ProcessBlockTransactions creates new accounts with the
	// exact sequencer-assigned identity (no per-node recomputation). Absent on blocks
	// stored before this persistence landed.
	var accountNonces []config.AccountNonce
	if an, ok := r.ExtraData["account_nonces"]; ok {
		if s, ok2 := an.(string); ok2 && s != "" {
			_ = json.Unmarshal([]byte(s), &accountNonces)
		}
	}

	// FeeRecipients: hydrate the persisted frozen buddy-reward split so a
	// synced/read block carries the IDENTICAL (address, weight) set the sequencer
	// stamped, and the apply path credits the same buddies with the same shares as
	// the live-gossip path. Absent on blocks stored before reward-split or with no
	// split. Consensus-critical for sync consistency — see STAKING-REWARDS-DESIGN.md.
	var feeRecipients []config.FeeRecipient
	if fr, ok := r.ExtraData["fee_recipients"]; ok {
		if s, ok2 := fr.(string); ok2 && s != "" {
			_ = json.Unmarshal([]byte(s), &feeRecipients)
		}
	}

	blk := &config.ZKBlock{
		BlockNumber:  r.BlockNumber,
		BlockHash:    common.HexToHash(r.BlockHash),
		PrevHash:     common.HexToHash(r.ParentHash),
		Timestamp:    r.Timestamp.Unix(),
		TxnsRoot:     r.TxsRoot,
		StateRoot:    common.HexToHash(r.StateRoot),
		LogsBloom:    r.LogsBloom,
		CoinbaseAddr: coinbase,
		ZKVMAddr:     zkvm,
		GasLimit:             r.GasLimit,
		GasUsed:              r.GasUsed,
		ExtraData:            extraData,
		CommitteeCertificate: committeeCert,
		StateFingerprint:     stateFingerprint,
		AccountNonces:        accountNonces,
		FeeRecipients:        feeRecipients,
		Transactions:         []config.Transaction{},
	}
	if v, ok := r.ExtraData["slot"]; ok {
		blk.Slot = extraDataUint64(v)
	}
	if v, ok := r.ExtraData["period"]; ok {
		blk.Period = extraDataUint64(v)
	}
	if v, ok := r.ExtraData["seed_epoch"]; ok {
		blk.SeedEpoch = extraDataUint64(v)
	}
	if v, ok := r.ExtraData["voting_snapshot_epoch"]; ok {
		blk.VotingSnapshotEpoch = extraDataUint64(v)
	}

	// The four non-scalar consensus fields FAIL CLOSED on a malformed value,
	// unlike extraDataUint64's lenient zero above. The asymmetry is
	// deliberate. A corrupt slot decoding to 0 is caught downstream — the
	// slot-recovery gate refuses to vote or propose on an implausible value.
	// A corrupt PrevAggCert decoding to nil is NOT caught anywhere: it is
	// indistinguishable from "this block legitimately carried no signers", so
	// the fallback fold would silently treat corrupt data as a real gap and
	// produce a wrong seed rather than refusing. Returning the error hands
	// that decision to the caller, which is the only place it can be made
	// correctly.
	//
	// A key that is simply ABSENT is never an error — that is every record
	// written before this fix, and it decodes to the zero value exactly as
	// before.
	if v, ok := r.ExtraData["randao_reveals"]; ok {
		if err := decodeExtraDataJSON(v, &blk.RandaoReveals); err != nil {
			return nil, fmt.Errorf("blockRecordToZKBlock: block %d: decoding randao_reveals: %w", r.BlockNumber, err)
		}
	}
	if v, ok := r.ExtraData["prev_agg_cert"]; ok {
		if err := decodeExtraDataJSON(v, &blk.PrevAggCert); err != nil {
			return nil, fmt.Errorf("blockRecordToZKBlock: block %d: decoding prev_agg_cert: %w", r.BlockNumber, err)
		}
	}
	if v, ok := r.ExtraData["vdf_proof"]; ok {
		b, err := extraDataBytes(v)
		if err != nil {
			return nil, fmt.Errorf("blockRecordToZKBlock: block %d: decoding vdf_proof: %w", r.BlockNumber, err)
		}
		blk.VdfProof = b
	}
	if v, ok := r.ExtraData["committee_snapshot_hash"]; ok {
		b, err := extraDataBytes(v)
		if err != nil {
			return nil, fmt.Errorf("blockRecordToZKBlock: block %d: decoding committee_snapshot_hash: %w", r.BlockNumber, err)
		}
		blk.CommitteeSnapshotHash = b
	}
	return blk, nil
}

// decodeExtraDataJSON decodes a struct-slice field that round-tripped through
// ExtraData's map[string]any into dst.
//
// Re-marshalling and unmarshalling rather than type-asserting is what makes
// this work for BOTH shapes the value can arrive in: []any of map[string]any
// after a real JSON round-trip through JSONB, and the original typed slice
// when an in-process writer put it in the map directly (tests, and the cache
// decorator's write-through path). A type switch would have to enumerate both,
// and would silently miss the first — which is the shape production actually
// uses.
//
// Order is preserved, which is required rather than incidental:
// RecordCommitCertificate hash-covers a certificate in array order, so a decode
// that reordered signers would change the derived aggregate.
func decodeExtraDataJSON(v any, dst any) error {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

// extraDataBytes decodes a []byte field that round-tripped through ExtraData.
//
// encoding/json renders []byte as a base64 string, so after a JSONB round-trip
// the value arrives as a string, never as bytes. Both forms are accepted for
// the same reason extraDataUint64 accepts the narrower numeric types: an
// in-process writer that never touches JSON puts the []byte in directly.
//
// An unexpected type is an error rather than a silent nil — see the fail-closed
// note at the call sites.
func extraDataBytes(v any) ([]byte, error) {
	switch b := v.(type) {
	case nil:
		return nil, nil
	case []byte:
		return b, nil
	case string:
		if b == "" {
			return nil, nil
		}
		return base64.StdEncoding.DecodeString(b)
	default:
		return nil, fmt.Errorf("expected a base64 string or []byte, got %T", v)
	}
}

// extraDataUint64 decodes a uint64 that round-tripped through ExtraData's
// map[string]any (itself round-tripped through JSON — see
// thebegateway/reader.go's scanBlock, and the cache decorator in
// DB_OPs/store/cache/block.go, both of which json.Unmarshal into this map).
// JSON numbers decode to float64 by default, never uint64/int64 directly, so
// a plain type-assertion to uint64 always misses — this is the one correct
// place that fact needs handling, rather than every caller re-discovering it.
// Also accepts the narrower numeric types directly, for any future writer
// that bypasses JSON (e.g. an in-process test building the map by hand).
func extraDataUint64(v any) uint64 {
	switch n := v.(type) {
	case float64:
		if n < 0 {
			return 0
		}
		return uint64(n)
	case uint64:
		return n
	case int64:
		if n < 0 {
			return 0
		}
		return uint64(n)
	case int:
		if n < 0 {
			return 0
		}
		return uint64(n)
	case json.Number:
		u, err := strconv.ParseUint(n.String(), 10, 64)
		if err != nil {
			return 0
		}
		return u
	default:
		return 0
	}
}

// txRecordToTransaction converts a thebegateway.TransactionRecord to a config.Transaction.
func txRecordToTransaction(r *thebegateway.TransactionRecord) *config.Transaction {
	if r == nil {
		return nil
	}

	tx := &config.Transaction{
		Hash:  common.HexToHash(r.TxHash),
		Type:  uint8(r.Type),
		Nonce: func() uint64 { n, _ := strconv.ParseUint(r.Nonce, 10, 64); return n }(),
	}

	if r.FromAddr != "" {
		a := common.HexToAddress(r.FromAddr)
		tx.From = &a
	}
	if r.ToAddr != nil && *r.ToAddr != "" {
		a := common.HexToAddress(*r.ToAddr)
		tx.To = &a
	}

	if v, ok := new(big.Int).SetString(r.ValueWei, 10); ok {
		tx.Value = v
	}
	gasLimit, _ := strconv.ParseUint(r.GasLimit, 10, 64)
	tx.GasLimit = gasLimit

	if r.GasPriceWei != "" && r.GasPriceWei != "0" {
		if p, ok := new(big.Int).SetString(r.GasPriceWei, 10); ok {
			tx.GasPrice = p
		}
	}
	if r.MaxFeeWei != "" && r.MaxFeeWei != "0" {
		if p, ok := new(big.Int).SetString(r.MaxFeeWei, 10); ok {
			tx.MaxFee = p
		}
	}
	if r.MaxPriorityFeeWei != "" && r.MaxPriorityFeeWei != "0" {
		if p, ok := new(big.Int).SetString(r.MaxPriorityFeeWei, 10); ok {
			tx.MaxPriorityFee = p
		}
	}

	tx.V = new(big.Int).SetUint64(r.SigV)
	// SigR/SigS are stored as base-16 (no 0x) via big.Int.Text(16) and CHAR(66)
	// pads with trailing spaces — trim before parsing. Without this, block full-tx
	// responses (eth_getBlockByNumber) return r=s=0 even though the row is signed.
	parseSig := func(s string) *big.Int {
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "0x")
		s = strings.TrimPrefix(s, "0X")
		if s == "" {
			return nil
		}
		n, ok := new(big.Int).SetString(s, 16)
		if !ok {
			return nil
		}
		return n
	}
	if n := parseSig(r.SigR); n != nil {
		tx.R = n
	}
	if n := parseSig(r.SigS); n != nil {
		tx.S = n
	}

	return tx
}

// zkProofRecordToZKBlock fills ZK proof fields on an existing ZKBlock from a ZKProofRecord.
func zkProofRecordToZKBlock(z *thebegateway.ZKProofRecord, block *config.ZKBlock) {
	if z == nil || block == nil {
		return
	}
	block.ProofHash = z.ProofHash
	block.StarkProof = z.StarkProof
	if len(z.Commitment) > 0 && len(z.Commitment)%4 == 0 {
		block.Commitment = make([]uint32, len(z.Commitment)/4)
		for i := range block.Commitment {
			block.Commitment[i] = binary.BigEndian.Uint32(z.Commitment[i*4:])
		}
	}
}

// nowNano returns the current time as Unix nanoseconds.
func nowNano() int64 { return time.Now().UTC().UnixNano() }
