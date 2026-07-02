package rpc

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"

	"encoding/json"
	"log"

	"gossipnode/DB_OPs/txindex"
	"gossipnode/gETH/Facade/Service"
	"gossipnode/gETH/Facade/Service/Types"

	"github.com/ethereum/go-ethereum/common"
)

type Handlers struct{ service Service.Service }

func NewHandlers(service Service.Service) *Handlers { return &Handlers{service: service} }

func (handler *Handlers) Handle(ctx context.Context, req Request) (Response, error) {
	// Log incoming request
	reqJSON, _ := json.Marshal(req)
	log.Printf("⚡️RPC Request: %s", string(reqJSON))

	switch req.Method {
	case "web3_clientVersion":
		v, err := handler.service.ClientVersion(ctx)
		resp, _ := finish(req, v, err)
		log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
		return resp, err
	case "net_version":
		id, err := handler.service.ChainID(ctx)
		resp, _ := finish(req, id.String(), err)
		log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
		return resp, err
	case "eth_chainId":
		id, err := handler.service.ChainID(ctx)
		if err != nil {
			resp, _ := finish(req, nil, err)
			log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
			return resp, err
		}
		resp, _ := finish(req, "0x"+id.Text(16), nil)
		log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
		return resp, nil

	case "eth_blockNumber":
		n, err := handler.service.BlockNumber(ctx)
		resp, _ := finish(req, "0x"+n.Text(16), err)
		log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
		return resp, err

	case "eth_getTransactionCount":
		if len(req.Params) < 2 {
			resp, _ := invalidParams(req, "missing address and block tag")
			// log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
			return resp, nil
		}
		addr, _ := req.Params[0].(string)
		block, _ := req.Params[1].(string)
		count, err := handler.service.GetTransactionCount(ctx, addr, block)
		if err != nil {
			resp, _ := finish(req, nil, err)
			// log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
			return resp, err
		}
		resp, _ := finish(req, "0x"+count.Text(16), nil)
		fmt.Println("Called RPC Call -- eth_getTransactionCount")
		// log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
		return resp, nil

	case "eth_getBlockByNumber":
		// params: [blockTag, fullTx(bool)]
		if len(req.Params) < 1 {
			resp, _ := invalidParams(req, "missing block tag")
			log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
			return resp, nil
		}
		fmt.Println("req.Params: ", req.Params)
		tag, _ := req.Params[0].(string)
		full := false

		if len(req.Params) > 1 {
			switch v := req.Params[1].(type) {
			case bool:
				full = v
			case string:
				full = strings.EqualFold(v, "true")
			}
		}

		num, err := parseBlockTag(ctx, handler.service, tag)
		if err != nil {
			resp, _ := finish(req, nil, err)
			log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
			return resp, err
		}
		b, err := handler.service.BlockByNumber(ctx, num, full)
		if err != nil {
			resp, _ := finish(req, nil, err)
			log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
			return resp, err
		}
		resp, _ := finish(req, marshalBlock(b, full, handler.service.GetChainIDValue()), nil)
		log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
		return resp, nil

	case "eth_getBalance":
		if len(req.Params) < 2 {
			resp, _ := invalidParams(req, "need address and block tag")
			log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
			return resp, nil
		}
		addr, _ := req.Params[0].(string)
		num, err := parseBlockTag(ctx, handler.service, mustString(req.Params[1]))
		if err != nil {
			resp, _ := finish(req, nil, err)
			log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
			return resp, err
		}
		bal, err := handler.service.Balance(ctx, addr, num, "jmdt:metamask")
		if err != nil {
			resp, _ := finish(req, nil, err)
			log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
			return resp, err
		}
		resp, _ := finish(req, "0x"+bal.Text(16), nil)
		log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
		return resp, nil
	case "eth_call":
		// Log incoming payload for eth_call
		// log.Printf("📥 eth_call payload: %+v", req.Params)
		// if len(req.Params) < 1 {
		// 	resp, _ := invalidParams(req, "missing call object")
		// 	log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
		// 	return resp, nil
		// }
		// msg, err := toCallMsg(req.Params[0])
		// if err != nil {
		// 	resp, _ := finish(req, nil, err)
		// 	log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
		// 	return resp, err
		// }
		// var num *big.Int
		// if len(req.Params) > 1 {
		// 	num, err = parseBlockTag(ctx, handler.service, mustString(req.Params[1]))
		// 	if err != nil {
		// 		resp, _ := finish(req, nil, err)
		// 		log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
		// 		return resp, err
		// 	}
		// }
		// out, err := handler.service.Call(ctx, msg, num)
		// if err != nil {
		// 	resp, _ := finish(req, nil, err)
		// 	log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
		// 	return resp, err
		// }
		// resp, _ := finish(req, "0x"+hex.EncodeToString(out), nil)
		// Explicitly disabled for security/compliance
		resp := RespErr(req.ID, -32601, "eth_call disabled")
		log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
		return resp, nil

	case "eth_estimateGas":
		if len(req.Params) < 1 {
			resp, _ := invalidParams(req, "missing tx object")
			log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
			return resp, nil
		}
		msg, err := toCallMsg(req.Params[0])
		if err != nil {
			resp, _ := finish(req, nil, err)
			log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
			return resp, err
		}
		g, err := handler.service.EstimateGas(ctx, msg)
		if err != nil {
			resp, _ := finish(req, nil, err)
			log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
			return resp, err
		}
		resp, _ := finish(req, "0x"+big.NewInt(int64(g)).Text(16), nil)
		log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
		return resp, nil

	case "eth_gasPrice":
		p, err := handler.service.GasPrice(ctx)
		resp, _ := finish(req, "0x"+p.Text(16), err)
		log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
		return resp, err

	case "eth_sendRawTransaction":
		if len(req.Params) < 1 {
			resp, _ := invalidParams(req, "missing raw tx")
			log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
			return resp, nil
		}
		raw, _ := req.Params[0].(string)
		// Debugging
		fmt.Println(">>>>>> eth_sendRawTransaction received: ", raw)
		txh, err := handler.service.SendRawTx(ctx, raw)
		resp, _ := finish(req, txh, err)
		log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
		return resp, err

	// Non-standard but widely used by explorers / wallets.
	// params: [address, page (optional, default 1), limit (optional, default 20)]
	case "eth_getTransactionsByAddress":
		if len(req.Params) < 1 {
			resp, _ := invalidParams(req, "missing address")
			return resp, nil
		}
		addr := mustString(req.Params[0])
		if addr == "" || !common.IsHexAddress(addr) {
			resp, _ := invalidParams(req, "address must be a valid hex address")
			return resp, nil
		}
		// Normalize to lowercase — ImmuDB/SQLite stores addresses in lowercase.
		addr = strings.ToLower(addr)

		page := 1
		if len(req.Params) > 1 {
			switch v := req.Params[1].(type) {
			case float64:
				page = int(v)
			case string:
				fmt.Sscanf(v, "%d", &page)
			}
		}
		if page < 1 {
			page = 1
		}
		// JSON numbers arrive as float64 — int(v) on an out-of-range float is
		// implementation-specific (could land anywhere), so clamp explicitly
		// rather than trusting the post-conversion value alone.
		const maxPage = 1_000_000
		if page > maxPage {
			page = maxPage
		}

		limit := 20
		if len(req.Params) > 2 {
			switch v := req.Params[2].(type) {
			case float64:
				limit = int(v)
			case string:
				fmt.Sscanf(v, "%d", &limit)
			}
		}
		if limit < 1 || limit > 100 {
			limit = 20
		}

		offset := (page - 1) * limit

		// Look up total first and short-circuit out-of-range pages before
		// paying for SQLite's O(offset) skip-scan (OFFSET isn't O(1) even with
		// the covering index) — a cheap request for page 1e6 shouldn't force
		// an expensive scan just to come back empty.
		total, totalErr := txindex.CountByAddress(ctx, addr)
		if totalErr != nil {
			resp, _ := finish(req, nil, fmt.Errorf("txindex unavailable: %w", totalErr))
			log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
			return resp, nil
		}
		if offset < 0 || offset >= total {
			resp, _ := finish(req, map[string]any{
				"transactions": []any{},
				"pagination": map[string]any{
					"current_page": page,
					"per_page":     limit,
					"total_pages":  totalPagesOfInt(total, limit),
					"total_items":  total,
					"has_next":     false,
					"has_prev":     page > 1,
				},
			}, nil)
			log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
			return resp, nil
		}

		refs, idxErr := txindex.QueryByAddressOffset(ctx, addr, offset, limit)
		if idxErr != nil {
			resp, _ := finish(req, nil, fmt.Errorf("txindex unavailable: %w", idxErr))
			log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
			return resp, nil
		}

		// Hydrate each ref from ImmuDB by hash in parallel (bounded) — each
		// fetch is an independent point lookup, page size is capped at 100.
		const hydrationConcurrency = 10
		txs := make([]any, len(refs))
		sem := make(chan struct{}, hydrationConcurrency)
		var wg sync.WaitGroup
		for i, ref := range refs {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, ref txindex.TxRef) {
				defer wg.Done()
				defer func() { <-sem }()

				tx, fetchErr := handler.service.TxByHash(ctx, ref.TxHash)
				if fetchErr != nil || tx == nil {
					// Return a skeleton so the caller can see block/hash even if hydration fails.
					txs[i] = map[string]any{
						"blockNumber": "0x" + new(big.Int).SetUint64(ref.BlockNumber).Text(16),
						"hash":        ref.TxHash,
					}
					return
				}
				m := marshalTx(tx, handler.service.GetChainIDValue())
				m["blockNumber"] = "0x" + new(big.Int).SetUint64(ref.BlockNumber).Text(16)
				txs[i] = m
			}(i, ref)
		}
		wg.Wait()

		resp, _ := finish(req, map[string]any{
			"transactions": txs,
			"pagination": map[string]any{
				"current_page": page,
				"per_page":     limit,
				"total_pages":  totalPagesOfInt(total, limit),
				"total_items":  total,
				"has_next":     offset+limit < total,
				"has_prev":     page > 1,
			},
		}, nil)
		log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
		return resp, nil

	case "eth_getTransactionByHash":
		if len(req.Params) < 1 {
			resp, _ := invalidParams(req, "missing tx hash")
			log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
			return resp, nil
		}
		tx, err := handler.service.TxByHash(ctx, mustString(req.Params[0]))
		if err != nil {
			resp, _ := finish(req, nil, err)
			log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
			return resp, err
		}
		resp, _ := finish(req, marshalTx(tx, handler.service.GetChainIDValue()), nil)
		log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
		return resp, nil

	case "eth_getTransactionReceipt":
		if len(req.Params) < 1 {
			resp, _ := invalidParams(req, "missing tx hash")
			log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
			return resp, nil
		}
		rcpt, err := handler.service.ReceiptByHash(ctx, mustString(req.Params[0]))
		resp, _ := finish(req, rcpt, err)
		log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
		return resp, err

	case "eth_getLogs":
		if len(req.Params) < 1 {
			resp, _ := invalidParams(req, "missing filter")
			log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
			return resp, nil
		}
		q, err := toFilterQuery(req.Params[0])
		if err != nil {
			resp, _ := finish(req, nil, err)
			log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
			return resp, err
		}
		logs, err := handler.service.GetLogs(ctx, *q)
		if err != nil {
			resp, _ := finish(req, nil, err)
			log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
			return resp, err
		}
		resp, _ := finish(req, marshalLogs(logs), nil)
		log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
		return resp, nil

	// case "eth_getCode":
	// 	if len(req.Params) < 2 {
	// 		resp, _ := invalidParams(req, "missing address and block tag")
	// 		log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
	// 		return resp, nil
	// 	}
	// 	addr, _ := req.Params[0].(string)
	// 	num, err := parseBlockTag(ctx, handler.service, mustString(req.Params[1]))
	// 	if err != nil {
	// 		resp, _ := finish(req, nil, err)
	// 		log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
	// 		return resp, err
	// 	}
	// 	code, err := handler.service.GetCode(ctx, addr, num)
	// 	if err != nil {
	// 		resp, _ := finish(req, nil, err)
	// 		log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
	// 		return resp, err
	// 	}
	// 	resp, _ := finish(req, code, nil)
	// 	log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
	// 	return resp, nil

	case "eth_feeHistory":
		if len(req.Params) < 2 {
			resp, _ := invalidParams(req, "missing blockCount and newestBlock")
			log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
			return resp, nil
		}

		// Parse blockCount (can be string hex or number)
		var blockCount uint64
		switch v := req.Params[0].(type) {
		case string:
			if strings.HasPrefix(v, "0x") {
				bigVal := new(big.Int)
				bigVal.SetString(v[2:], 16)
				blockCount = bigVal.Uint64()
			} else {
				fmt.Sscanf(v, "%d", &blockCount)
			}
		case float64:
			blockCount = uint64(v)
		case int:
			blockCount = uint64(v)
		default:
			resp, _ := invalidParams(req, "invalid blockCount type")
			log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
			return resp, nil
		}

		// Parse newestBlock (block tag)
		newestBlock, err := parseBlockTag(ctx, handler.service, mustString(req.Params[1]))
		if err != nil {
			resp, _ := finish(req, nil, err)
			log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
			return resp, err
		}

		// Parse rewardPercentiles (optional, third parameter)
		var rewardPercentiles []float64
		if len(req.Params) > 2 {
			if percArray, ok := req.Params[2].([]any); ok {
				rewardPercentiles = make([]float64, 0, len(percArray))
				for _, p := range percArray {
					switch v := p.(type) {
					case float64:
						rewardPercentiles = append(rewardPercentiles, v)
					case string:
						var val float64
						fmt.Sscanf(v, "%f", &val)
						rewardPercentiles = append(rewardPercentiles, val)
					}
				}
			}
		}

		history, err := handler.service.FeeHistory(ctx, blockCount, newestBlock, rewardPercentiles)
		if err != nil {
			resp, _ := finish(req, nil, err)
			log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
			return resp, err
		}
		resp, _ := finish(req, history, nil)
		log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
		return resp, nil

	default:
		resp := RespErr(req.ID, -32601, "Method not found")
		log.Printf("📤 RPC Response: %s -> %+v", req.Method, resp)
		return resp, nil
	}
}

func parseBlockTag(ctx context.Context, be Service.Service, tag string) (*big.Int, error) {
	switch strings.ToLower(strings.TrimSpace(tag)) {
	case "latest", "":
		return be.BlockNumber(ctx)
	case "pending":
		// map to latest for now; refine if you track pending state
		return be.BlockNumber(ctx)
	default:
		if strings.HasPrefix(tag, "0x") {
			n := new(big.Int)
			n.SetString(tag[2:], 16)
			return n, nil
		}
		return nil, errors.New("unsupported block tag")
	}
}

func finish(req Request, v any, err error) (Response, error) {
	if err != nil {
		return RespErr(req.ID, -32000, err.Error()), nil
	}
	return RespOK(req.ID, v), nil
}

func invalidParams(req Request, msg string) (Response, error) {
	return RespErr(req.ID, -32602, msg), nil
}

func mustString(v any) string {
	s, _ := v.(string)
	return s
}

// totalPagesOfInt computes the page count for a given total item count and page size.
func totalPagesOfInt(total, limit int) int {
	if total <= 0 || limit <= 0 {
		return 0
	}
	return (total + limit - 1) / limit
}

func toCallMsg(p any) (Types.CallMsg, error) {
	// Parse call object from JSON-RPC params
	if callObj, ok := p.(map[string]any); ok {
		msg := Types.CallMsg{}

		if from, ok := callObj["from"].(string); ok {
			msg.From = from
		}
		if to, ok := callObj["to"].(string); ok {
			msg.To = to
		}
		if data, ok := callObj["data"].(string); ok {
			if strings.HasPrefix(data, "0x") {
				msg.Data, _ = hex.DecodeString(data[2:])
			} else {
				msg.Data, _ = hex.DecodeString(data)
			}
		}
		if value, ok := callObj["value"].(string); ok {
			if strings.HasPrefix(value, "0x") {
				bigVal := new(big.Int)
				bigVal.SetString(value[2:], 16)
				msg.Value = bigVal
			}
		}
		if gas, ok := callObj["gas"].(string); ok {
			if strings.HasPrefix(gas, "0x") {
				bigGas := new(big.Int)
				bigGas.SetString(gas[2:], 16)
				msg.Gas = bigGas
			}
		}
		if gasPrice, ok := callObj["gasPrice"].(string); ok {
			if strings.HasPrefix(gasPrice, "0x") {
				bigGasPrice := new(big.Int)
				bigGasPrice.SetString(gasPrice[2:], 16)
				msg.GasPrice = bigGasPrice
			}
		}
		if maxFee, ok := callObj["maxFeePerGas"].(string); ok {
			if strings.HasPrefix(maxFee, "0x") {
				v := new(big.Int)
				v.SetString(maxFee[2:], 16)
				msg.MaxFeePerGas = v
			}
		}
		if maxTip, ok := callObj["maxPriorityFeePerGas"].(string); ok {
			if strings.HasPrefix(maxTip, "0x") {
				v := new(big.Int)
				v.SetString(maxTip[2:], 16)
				msg.MaxPriorityFeePerGas = v
			}
		}

		return msg, nil
	}
	return Types.CallMsg{}, errors.New("invalid call object")
}

func toFilterQuery(p any) (*Types.FilterQuery, error) {
	// Parse filter object from JSON-RPC params
	if filterObj, ok := p.(map[string]any); ok {
		query := &Types.FilterQuery{}

		if fromBlock, ok := filterObj["fromBlock"].(string); ok {
			if strings.HasPrefix(fromBlock, "0x") {
				bigFromBlock := new(big.Int)
				bigFromBlock.SetString(fromBlock[2:], 16)
				query.FromBlock = bigFromBlock
			}
		}
		if toBlock, ok := filterObj["toBlock"].(string); ok {
			if strings.HasPrefix(toBlock, "0x") {
				bigToBlock := new(big.Int)
				bigToBlock.SetString(toBlock[2:], 16)
				query.ToBlock = bigToBlock
			}
		}
		if addresses, ok := filterObj["address"].([]any); ok {
			query.Addresses = make([]string, len(addresses))
			for i, addr := range addresses {
				if addrStr, ok := addr.(string); ok {
					query.Addresses[i] = addrStr
				}
			}
		}
		if topics, ok := filterObj["topics"].([]any); ok {
			query.Topics = make([][]string, len(topics))
			for i, topic := range topics {
				if topicArr, ok := topic.([]any); ok {
					query.Topics[i] = make([]string, len(topicArr))
					for j, t := range topicArr {
						if topicStr, ok := t.(string); ok {
							query.Topics[i][j] = topicStr
						}
					}
				} else if topicStr, ok := topic.(string); ok {
					query.Topics[i] = []string{topicStr}
				}
			}
		}

		return query, nil
	}
	return &Types.FilterQuery{}, errors.New("invalid filter object")
}

// sha3UnclesEmpty is the Keccak-256 of an empty uncle list — standard constant for PoS/non-PoW chains.
const sha3UnclesEmpty = "0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347"

func marshalBlock(b *Types.Block, full bool, globalChainID *big.Int) map[string]any {
	result := map[string]any{
		// Core identity
		"number":     "0x" + new(big.Int).SetUint64(b.Header.Number).Text(16),
		"hash":       "0x" + hex.EncodeToString(b.Header.Hash),
		"parentHash": "0x" + hex.EncodeToString(b.Header.ParentHash),
		"timestamp":  "0x" + new(big.Int).SetUint64(b.Header.Timestamp).Text(16),

		// Gas
		"gasLimit": "0x" + new(big.Int).SetUint64(b.Header.GasLimit).Text(16),
		"gasUsed":  "0x" + new(big.Int).SetUint64(b.Header.GasUsed).Text(16),

		// State / receipts — all stored in ZKBlock, surfaced via BlockHeader
		"stateRoot":    "0x" + hex.EncodeToString(b.Header.StateRoot),
		"receiptsRoot": "0x" + hex.EncodeToString(b.Header.ReceiptsRoot),
		"logsBloom":    "0x" + hex.EncodeToString(b.Header.LogsBloom),
		"extraData":    "0x" + hex.EncodeToString(b.Header.ExtraData),

		// Miner / sequencer address (ZKVMAddr in ZKBlock → Miner in BlockHeader)
		"miner": "0x" + hex.EncodeToString(b.Header.Miner),

		// EIP-1559 base fee — computed at read time (35 gwei constant) in ConvertZKBlockToblockheader.
		// Guard against nil/empty for blocks written before this field existed.
		"baseFeePerGas": func() string {
			if len(b.Header.BaseFee) > 0 {
				return "0x" + new(big.Int).SetBytes(b.Header.BaseFee).Text(16)
			}
			return "0x" + big.NewInt(35000000000).Text(16) // fallback: 35 gwei
		}(),

		// PoW fields — this chain has no PoW; use standard empty/zero values
		// so EIP-3675 (PoS) compatible clients don't reject the block envelope
		"sha3Uncles":      sha3UnclesEmpty,
		"nonce":           "0x0000000000000000",
		"difficulty":      "0x0",
		"totalDifficulty": "0x0",
		"mixHash":         "0x" + strings.Repeat("0", 64),
		"uncles":          []string{},

		"transactions": []any{},
	}

	if full && len(b.Transactions) > 0 {
		txs := make([]any, len(b.Transactions))
		for i, tx := range b.Transactions {
			txs[i] = marshalTx(tx, globalChainID)
		}
		result["transactions"] = txs
	} else if len(b.Transactions) > 0 {
		txHashes := make([]string, len(b.Transactions))
		for i, tx := range b.Transactions {
			txHashes[i] = "0x" + hex.EncodeToString(tx.Hash)
		}
		result["transactions"] = txHashes
	}

	return result
}

func marshalTx(tx *Types.Tx, globalChainID *big.Int) map[string]any {
	// to: null for contract deployments, hex address otherwise
	var to any
	if len(tx.To) > 0 {
		to = "0x" + hex.EncodeToString(tx.To)
	}

	// gasPrice: for type 2 use effectiveGasPrice = min(maxFeePerGas, baseFee+tip)
	// For legacy/type1 use GasPrice directly.
	const baseFee = int64(35_000_000_000)
	var gasPriceHex string
	if tx.Type == 2 && len(tx.MaxFeePerGas) > 0 {
		maxFee := new(big.Int).SetBytes(tx.MaxFeePerGas)
		tip := new(big.Int)
		if len(tx.MaxPriorityFeePerGas) > 0 {
			tip.SetBytes(tx.MaxPriorityFeePerGas)
		}
		basePlusTip := new(big.Int).Add(big.NewInt(baseFee), tip)
		effective := maxFee
		if maxFee.Cmp(basePlusTip) > 0 {
			effective = basePlusTip
		}
		gasPriceHex = "0x" + effective.Text(16)
	} else if len(tx.GasPrice) > 0 {
		gasPriceHex = "0x" + new(big.Int).SetBytes(tx.GasPrice).Text(16)
	} else {
		gasPriceHex = "0x0"
	}

	result := map[string]any{
		"hash":     "0x" + hex.EncodeToString(tx.Hash),
		"from":     "0x" + hex.EncodeToString(tx.From),
		"to":       to,
		"input":    "0x" + hex.EncodeToString(tx.Input),
		"value":    "0x" + new(big.Int).SetBytes(tx.Value).Text(16),
		"nonce":    "0x" + new(big.Int).SetUint64(tx.Nonce).Text(16),
		"gas":      "0x" + new(big.Int).SetUint64(tx.Gas).Text(16),
		"gasPrice": gasPriceHex,
		"type":     "0x" + new(big.Int).SetUint64(uint64(tx.Type)).Text(16),
	}

	// EIP-1559 fields (type 2)
	if tx.Type == 2 {
		if len(tx.MaxFeePerGas) > 0 {
			result["maxFeePerGas"] = "0x" + new(big.Int).SetBytes(tx.MaxFeePerGas).Text(16)
		}
		if len(tx.MaxPriorityFeePerGas) > 0 {
			result["maxPriorityFeePerGas"] = "0x" + new(big.Int).SetBytes(tx.MaxPriorityFeePerGas).Text(16)
		}
	}

	// chainId — always emit; fall back to global chain ID if not set on the tx
	chainID := new(big.Int).SetBytes(tx.ChainID)
	if len(tx.ChainID) == 0 {
		chainID = globalChainID
	}
	result["chainId"] = "0x" + chainID.Text(16)

	// Add optional fields if they exist
	if len(tx.R) > 0 {
		result["r"] = "0x" + hex.EncodeToString(tx.R)
	}
	if len(tx.S) > 0 {
		result["s"] = "0x" + hex.EncodeToString(tx.S)
	}
	if tx.V > 0 {
		result["v"] = "0x" + new(big.Int).SetUint64(uint64(tx.V)).Text(16)
	}

	// Add block information if available
	if tx.BlockNumber != nil {
		result["blockNumber"] = "0x" + new(big.Int).SetUint64(*tx.BlockNumber).Text(16)
	}
	if len(tx.BlockHash) > 0 {
		result["blockHash"] = "0x" + hex.EncodeToString(tx.BlockHash)
	}
	if tx.TransactionIndex != nil {
		result["transactionIndex"] = "0x" + new(big.Int).SetUint64(*tx.TransactionIndex).Text(16)
	}

	return result
}

func marshalLogs(logs []Types.Log) []map[string]any {
	result := make([]map[string]any, len(logs))
	for i, log := range logs {
		// Convert topics from [][]byte to []string
		topics := make([]string, len(log.Topics))
		for j, topic := range log.Topics {
			topics[j] = "0x" + hex.EncodeToString(topic)
		}

		result[i] = map[string]any{
			"address":          "0x" + hex.EncodeToString(log.Address),
			"topics":           topics,
			"data":             "0x" + hex.EncodeToString(log.Data),
			"blockNumber":      "0x" + new(big.Int).SetUint64(log.BlockNumber).Text(16),
			"transactionHash":  "0x" + hex.EncodeToString(log.TxHash),
			"logIndex":         "0x" + new(big.Int).SetUint64(log.LogIndex).Text(16),
			"blockHash":        "0x" + hex.EncodeToString(log.BlockHash),
			"transactionIndex": "0x" + new(big.Int).SetUint64(log.TxIndex).Text(16),
			"removed":          log.Removed,
		}
	}
	return result
}
