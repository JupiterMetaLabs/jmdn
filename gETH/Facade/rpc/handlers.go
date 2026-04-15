package rpc

import (
	"context"
	"encoding/hex"

	"github.com/JupiterMetaLabs/ion"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"encoding/json"

	"gossipnode/gETH/Facade/Service"
	"gossipnode/gETH/Facade/Service/Types"
)

type Handlers struct{ service Service.Service }

func NewHandlers(service Service.Service) *Handlers { return &Handlers{service: service} }

func (handler *Handlers) Handle(ctx context.Context, req Request) (Response, error) {
	// Log incoming request
	reqJSON, _ := json.Marshal(req)
	logger().Info(ctx, "RPC Request", ion.String("request", string(reqJSON)))

	switch req.Method {
	case "web3_clientVersion":
		v, err := handler.service.ClientVersion(ctx)
		resp, _ := finish(req, v, err)
		logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
		return resp, err
	case "solc_compile":
		// Expected params[0]: { "source": "...", "optimize": true, "runs": 200 }
		if len(req.Params) == 0 {
			resp, _ := invalidParams(req, "missing params")
			return resp, nil
		}
		var compileReq struct {
			Source   string `json:"source"`
			Optimize bool   `json:"optimize"`
			Runs     uint32 `json:"runs"`
		}
		// Marshal the any type back to JSON and then unmarshal into our struct
		paramJSON, _ := json.Marshal(req.Params[0])
		if err := json.Unmarshal(paramJSON, &compileReq); err != nil {
			resp, _ := invalidParams(req, "invalid params: "+err.Error())
			return resp, nil
		}
		if compileReq.Runs == 0 {
			compileReq.Runs = 200
		}
		result, err := handler.service.CompileSolidity(ctx, compileReq.Source, compileReq.Optimize, compileReq.Runs)
		resp, _ := finish(req, result, err)
		logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
		return resp, err
	case "net_version":
		id, err := handler.service.ChainID(ctx)
		resp, _ := finish(req, id.String(), err)
		logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
		return resp, err
	case "eth_chainId":
		id, err := handler.service.ChainID(ctx)
		if err != nil {
			resp, _ := finish(req, nil, err)
			logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, err
		}
		resp, _ := finish(req, "0x"+id.Text(16), nil)
		logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
		return resp, nil

	case "eth_blockNumber":
		n, err := handler.service.BlockNumber(ctx)
		resp, _ := finish(req, "0x"+n.Text(16), err)
		logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
		return resp, err

	case "eth_getTransactionCount":
		if len(req.Params) < 2 {
			resp, _ := invalidParams(req, "missing address and block tag")
			// logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, nil
		}
		addr, _ := req.Params[0].(string)
		block, _ := req.Params[1].(string)
		count, err := handler.service.GetTransactionCount(ctx, addr, block)
		if err != nil {
			resp, _ := finish(req, nil, err)
			// logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, err
		}
		resp, _ := finish(req, "0x"+count.Text(16), nil)
		logger().Debug(ctx, "Called RPC Call", ion.String("method", "eth_getTransactionCount"))
		// logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
		return resp, nil

	case "eth_getBlockByNumber":
		// params: [blockTag, fullTx(bool)]
		if len(req.Params) < 1 {
			resp, _ := invalidParams(req, "missing block tag")
			logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, nil
		}
		logger().Debug(ctx, "Request parameters", ion.String("method", "eth_getBlockByNumber"))
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
			logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, err
		}
		b, err := handler.service.BlockByNumber(ctx, num, full)
		if err != nil {
			resp, _ := finish(req, nil, err)
			logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, err
		}
		resp, _ := finish(req, marshalBlock(b, full), nil)
		logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
		return resp, nil

	case "eth_getBalance":
		if len(req.Params) < 2 {
			resp, _ := invalidParams(req, "need address and block tag")
			logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, nil
		}
		addr, _ := req.Params[0].(string)
		num, err := parseBlockTag(ctx, handler.service, mustString(req.Params[1]))
		if err != nil {
			resp, _ := finish(req, nil, err)
			logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, err
		}
		bal, err := handler.service.Balance(ctx, addr, num, "jmdt:metamask")
		if err != nil {
			resp, _ := finish(req, nil, err)
			logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, err
		}
		resp, _ := finish(req, "0x"+bal.Text(16), nil)
		logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
		return resp, nil
	case "eth_call":
		if len(req.Params) < 1 {
			resp, _ := invalidParams(req, "missing call object")
			logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, nil
		}
		msg, err := toCallMsg(req.Params[0])
		if err != nil {
			resp, _ := finish(req, nil, err)
			logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, err
		}
		var num *big.Int
		if len(req.Params) > 1 {
			num, err = parseBlockTag(ctx, handler.service, mustString(req.Params[1]))
			if err != nil {
				resp, _ := finish(req, nil, err)
				logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
				return resp, err
			}
		}
		out, err := handler.service.Call(ctx, msg, num)
		if err != nil {
			resp, _ := finish(req, nil, err)
			logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, err
		}
		resp, _ := finish(req, "0x"+hex.EncodeToString(out), nil)
		logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
		return resp, nil

	case "eth_estimateGas":
		if len(req.Params) < 1 {
			resp, _ := invalidParams(req, "missing tx object")
			logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, nil
		}
		msg, err := toCallMsg(req.Params[0])
		if err != nil {
			resp, _ := finish(req, nil, err)
			logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, err
		}
		g, err := handler.service.EstimateGas(ctx, msg)
		if err != nil {
			resp, _ := finish(req, nil, err)
			logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, err
		}
		resp, _ := finish(req, "0x"+big.NewInt(int64(g)).Text(16), nil)
		logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
		return resp, nil



	case "eth_sendRawTransaction":
		if len(req.Params) < 1 {
			resp, _ := invalidParams(req, "missing raw tx")
			logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, nil
		}
		raw, _ := req.Params[0].(string)
		// Debugging
		logger().Debug(ctx, "eth_sendRawTransaction received", ion.String("raw_tx_length", fmt.Sprintf("%d", len(raw))))
		txh, err := handler.service.SendRawTx(ctx, raw)
		resp, _ := finish(req, txh, err)
		logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
		return resp, err

	case "eth_getTransactionByHash":
		if len(req.Params) < 1 {
			resp, _ := invalidParams(req, "missing tx hash")
			logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, nil
		}
		tx, err := handler.service.TxByHash(ctx, mustString(req.Params[0]))
		if err != nil {
			resp, _ := finish(req, nil, err)
			logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, err
		}
		resp, _ := finish(req, marshalTx(tx), nil)
		logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
		return resp, nil

	case "eth_getTransactionReceipt":
		if len(req.Params) < 1 {
			resp, _ := invalidParams(req, "missing tx hash")
			logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, nil
		}
		rcpt, err := handler.service.ReceiptByHash(ctx, mustString(req.Params[0]))
		resp, _ := finish(req, rcpt, err)
		logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
		return resp, err

	case "eth_getLogs":
		if len(req.Params) < 1 {
			resp, _ := invalidParams(req, "missing filter")
			logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, nil
		}
		q, err := toFilterQuery(req.Params[0])
		if err != nil {
			resp, _ := finish(req, nil, err)
			logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, err
		}
		logs, err := handler.service.GetLogs(ctx, *q)
		if err != nil {
			resp, _ := finish(req, nil, err)
			logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, err
		}
		resp, _ := finish(req, marshalLogs(logs), nil)
		logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
		return resp, nil

	case "eth_getCode":
		if len(req.Params) < 2 {
			resp, _ := invalidParams(req, "missing address and block tag")
			logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, nil
		}
		addr, _ := req.Params[0].(string)
		num, err := parseBlockTag(ctx, handler.service, mustString(req.Params[1]))
		if err != nil {
			resp, _ := finish(req, nil, err)
			logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, err
		}
		code, err := handler.service.GetCode(ctx, addr, num)
		if err != nil {
			resp, _ := finish(req, nil, err)
			logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, err
		}
		resp, _ := finish(req, code, nil)
		logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
		return resp, nil

	case "eth_getStorageAt":
		if len(req.Params) < 2 {
			resp, _ := invalidParams(req, "missing address and slot")
			return resp, nil
		}
		address, _ := req.Params[0].(string)
		slot, _ := req.Params[1].(string)
		blockNum := "latest"
		if len(req.Params) > 2 {
			if b, ok := req.Params[2].(string); ok {
				blockNum = b
			}
		}
		result, err := handler.service.GetStorageAt(ctx, address, slot, blockNum)
		resp, _ := finish(req, result, err)
		return resp, err

	case "eth_gasPrice":
		result, err := handler.service.GetGasPrice(ctx)
		resp, _ := finish(req, result, err)
		return resp, err

	case "eth_feeHistory":
		if len(req.Params) < 2 {
			resp, _ := invalidParams(req, "missing blockCount and newestBlock")
			return resp, nil
		}
		var blockCount int
		switch v := req.Params[0].(type) {
		case string:
			if strings.HasPrefix(v, "0x") {
				var count uint64
				fmt.Sscanf(v[2:], "%x", &count)
				blockCount = int(count)
			} else {
				fmt.Sscanf(v, "%d", &blockCount)
			}
		case float64:
			blockCount = int(v)
		case int:
			blockCount = v
		}

		newestBlock, _ := req.Params[1].(string)

		var rewardPercentiles []float64
		if len(req.Params) > 2 {
			if arr, ok := req.Params[2].([]any); ok {
				for _, val := range arr {
					if f, ok := val.(float64); ok {
						rewardPercentiles = append(rewardPercentiles, f)
					}
				}
			}
		}

		result, err := handler.service.GetFeeHistory(ctx, blockCount, newestBlock, rewardPercentiles)
		resp, _ := finish(req, result, err)
		return resp, err

	case "eth_maxPriorityFeePerGas":
		result, err := handler.service.GetMaxPriorityFeePerGas(ctx)
		resp, _ := finish(req, result, err)
		return resp, err

	case "net_listening":
		result, err := handler.service.IsListening(ctx)
		resp, _ := finish(req, result, err)
		return resp, err

	case "net_peerCount":
		result, err := handler.service.GetPeerCount(ctx)
		resp, _ := finish(req, result, err)
		return resp, err

	case "debug_traceTransaction":
		if len(req.Params) < 1 {
			resp, _ := invalidParams(req, "missing tx hash")
			logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, nil
		}
		txHash, _ := req.Params[0].(string)
		result, err := handler.service.TraceTransaction(ctx, txHash)
		if err != nil {
			resp, _ := finish(req, nil, err)
			logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
			return resp, err
		}
		resp, _ := finish(req, result, nil)
		logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
		return resp, nil

	default:
		resp := RespErr(req.ID, -32601, "Method not found")
		logger().Info(ctx, "RPC Response", ion.String("method", req.Method), ion.String("response", fmt.Sprintf("%+v", resp)))
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

func marshalBlock(b *Types.Block, full bool) map[string]any {
	result := map[string]any{
		"number":       "0x" + new(big.Int).SetUint64(b.Header.Number).Text(16),
		"hash":         "0x" + hex.EncodeToString(b.Header.Hash),
		"parentHash":   "0x" + hex.EncodeToString(b.Header.ParentHash),
		"timestamp":    "0x" + new(big.Int).SetUint64(b.Header.Timestamp).Text(16),
		"gasLimit":     "0x" + new(big.Int).SetUint64(b.Header.GasLimit).Text(16),
		"gasUsed":      "0x" + new(big.Int).SetUint64(b.Header.GasUsed).Text(16),
		"transactions": []any{},
	}

	// Add baseFeePerGas at top-level from header (EIP-1559)
	// if b.Header != nil && len(b.Header.BaseFee) > 0 {
	// 	baseFeeBig := new(big.Int).SetBytes(b.Header.BaseFee)
	// 	result["baseFeePerGas"] = "0x" + baseFeeBig.Text(16)
	// } else {
	// 	// If no base fee (pre-EIP-1559 blocks), set to null or omit
	// 	result["baseFeePerGas"] = nil
	// }

	if full && len(b.Transactions) > 0 {
		txs := make([]any, len(b.Transactions))
		for i, tx := range b.Transactions {
			txs[i] = marshalTx(tx)
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

func marshalTx(tx *Types.Tx) map[string]any {
	result := map[string]any{
		"hash":     "0x" + hex.EncodeToString(tx.Hash),
		"from":     "0x" + hex.EncodeToString(tx.From),
		"to":       "0x" + hex.EncodeToString(tx.To),
		"input":    "0x" + hex.EncodeToString(tx.Input),
		"value":    "0x" + new(big.Int).SetBytes(tx.Value).Text(16),
		"nonce":    "0x" + new(big.Int).SetUint64(tx.Nonce).Text(16),
		"gas":      "0x" + new(big.Int).SetUint64(tx.Gas).Text(16),
		"gasPrice": "0x" + new(big.Int).SetBytes(tx.GasPrice).Text(16),
		"type":     "0x" + new(big.Int).SetUint64(uint64(tx.Type)).Text(16),
	}

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

// logger returns the ion logger instance for the Facade package
func logger() *ion.Ion {
	logInstance, err := log.NewAsyncLogger().Get().NamedLogger(log.Facade, "")
	if err != nil {
		return nil
	}
	return logInstance.GetNamedLogger()
}
