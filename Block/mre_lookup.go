package Block

import (
	"context"
	"fmt"
	"time"

	pb "gossipnode/Mempool/proto"
	"gossipnode/txstatus"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// mreLookupMethod is the real MRE v1 method string. The request/response
// messages in Mempool/proto/mre_lookup.proto are field-for-field identical to
// jmdt.proto.mre.v1.LookupTransaction{Request,Response}, so conn.Invoke against
// this path decodes correctly even though the messages are declared in this
// repo's local `proto` package. Same pattern as PeekPendingTransactions.
const mreLookupMethod = "/jmdt.proto.mre.v1.MREService/LookupTransaction"

// LookupTransaction asks MRE whether a hash is currently pending in the mempool
// fleet, without mutating mempool state.
//
// This is NOT GetTransaction / GetTransactionByHash: those are destructive on
// the mempool side and would delete a pending transaction on every explorer
// query. It is also not PeekTransactionByHash, whose response type cannot
// express "not found" or "could not tell".
func (r *RoutingClient) LookupTransaction(ctx context.Context, hash string) (*pb.LookupTransactionResponse, error) {
	// The hash is sent verbatim: the mempool indexes on the string the
	// transaction was submitted with, so normalising case here could turn a hit
	// into a miss. Callers are expected to pass the canonical lowercase
	// 0x-prefixed form.
	req := &pb.LookupTransactionRequest{Hash: hash}
	out := &pb.LookupTransactionResponse{}

	if err := r.conn.Invoke(ctx, mreLookupMethod, req, out, grpc.StaticMethod()); err != nil {
		return nil, fmt.Errorf("LookupTransaction: %w", err)
	}
	return out, nil
}

// MRELookup adapts the routing client to txstatus.MempoolLookup.
//
// It resolves the routing client per call rather than holding it, because the
// singleton is installed during startup and may not exist yet when the RPC
// facade is constructed.
type MRELookup struct{}

// NewMRELookup returns a txstatus.MempoolLookup backed by the MRE routing
// client singleton.
func NewMRELookup() txstatus.MempoolLookup { return MRELookup{} }

// Lookup implements txstatus.MempoolLookup.
//
// It NEVER returns a non-nil error. Every failure — no client, feature disabled
// on MRE, throttled, deadline, transport error — is reported as a degraded
// result, because a status query must not fail (and must never make
// eth_getTransactionByHash fail) just because the mempool is unwell. The
// resolver turns a degraded result into `unknown`, never into `absent` and
// never into `queued`.
func (MRELookup) Lookup(ctx context.Context, hash string) (*txstatus.MempoolResult, error) {
	client, err := GetRoutingClient()
	if err != nil || client == nil {
		return &txstatus.MempoolResult{
			Degraded: true,
			Detail:   "routing client is not initialised",
		}, nil
	}

	resp, err := client.LookupTransaction(ctx, hash)
	if err != nil {
		return &txstatus.MempoolResult{
			Degraded: true,
			Detail:   describeLookupError(err),
		}, nil
	}

	out := &txstatus.MempoolResult{
		Found:    resp.GetFound(),
		Degraded: resp.GetDegraded(),
		ShardID:  resp.GetShardId(),
		NodeID:   resp.GetNodeId(),
	}
	if resp.GetDegraded() {
		out.Detail = fmt.Sprintf("MRE reported an inconclusive lookup (%d/%d shards failed)",
			resp.GetShardsFailed(), resp.GetShardsQueried())
	}
	if resp.GetFound() {
		out.Tx = pendingTxFromProto(resp.GetTransaction())
	}
	return out, nil
}

// describeLookupError turns a gRPC failure into an operator-readable note.
// Unimplemented is called out specifically because it is the expected response
// from an MRE that has not enabled its lookup feature — an operator needs to
// see "turn it on", not "something broke".
func describeLookupError(err error) string {
	switch status.Code(err) {
	case codes.Unimplemented:
		return "MRE lookup is not enabled on the mempool routing engine (set MRE_LOOKUP_ENABLED=true)"
	case codes.ResourceExhausted:
		return "MRE rejected the lookup: rate limit exceeded"
	case codes.DeadlineExceeded:
		return "MRE lookup exceeded its deadline"
	case codes.Unavailable:
		return "MRE is unreachable"
	default:
		return "MRE lookup failed: " + err.Error()
	}
}

// pendingTxFromProto converts a mempool transaction to the wire-independent
// form txstatus works with.
//
// NOTE on the encryption boundary: the mempool stores from/to/value/nonce/gas/
// data encrypted and keeps only hash/type/timestamp/chain_id/v/r/s in the
// clear. Whether the mempool node decrypts these before answering the lookup is
// a property of the mempool implementation, not of this code. If it does not,
// the fields below arrive empty and callers must not present the result as a
// complete transaction body.
func pendingTxFromProto(p *pb.Transaction) *txstatus.PendingTx {
	if p == nil {
		return nil
	}

	out := &txstatus.PendingTx{
		Hash:           p.GetHash(),
		From:           p.GetFrom(),
		To:             p.GetTo(),
		Value:          p.GetValue(),
		Type:           p.GetType(),
		Timestamp:      p.GetTimestamp(),
		ChainID:        p.GetChainId(),
		Nonce:          p.GetNonce(),
		GasLimit:       p.GetGasLimit(),
		GasPrice:       p.GetGasPrice(),
		MaxFee:         p.GetMaxFee(),
		MaxPriorityFee: p.GetMaxPriorityFee(),
		Data:           p.GetData(),
		V:              p.GetV(),
		R:              p.GetR(),
		S:              p.GetS(),
	}

	if al := p.GetAccessList(); len(al) > 0 {
		out.AccessList = make([]txstatus.AccessTuple, 0, len(al))
		for _, t := range al {
			if t == nil {
				continue
			}
			out.AccessList = append(out.AccessList, txstatus.AccessTuple{
				Address:     t.GetAddress(),
				StorageKeys: t.GetStorageKeys(),
			})
		}
	}

	return out
}

// LookupTimeoutFloor is the smallest per-call deadline the adapter will accept
// from configuration. Below this, a healthy MRE fan-out cannot complete and
// every answer would be degraded — which fails safe but is useless.
const LookupTimeoutFloor = 50 * time.Millisecond
