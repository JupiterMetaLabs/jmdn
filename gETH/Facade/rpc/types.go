package rpc

import "encoding/json"

type Request struct {
	Jsonrpc string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []any         `json:"params"`
	ID      any           `json:"id"`
}

// Response follows JSON-RPC 2.0.
// Result is *json.RawMessage so that a nil result serializes as explicit
// "result":null (required by spec for pending/unfound queries like
// eth_getTransactionReceipt). The omitempty on Error removes the error
// field on success; the omitempty on Result removes it on error responses
// (where RespErr leaves Result nil/pointer-nil).
type Response struct {
	Jsonrpc string           `json:"jsonrpc"`
	Result  *json.RawMessage `json:"result,omitempty"`
	Error   *Error           `json:"error,omitempty"`
	ID      any              `json:"id"`
}

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// RespOK builds a success response. v=nil produces "result":null (pending).
func RespOK(id any, v any) Response {
	if v == nil {
		null := json.RawMessage("null")
		return Response{Jsonrpc: "2.0", Result: &null, ID: id}
	}
	data, err := json.Marshal(v)
	if err != nil {
		// Fallback: return null rather than silently dropping the result
		null := json.RawMessage("null")
		return Response{Jsonrpc: "2.0", Result: &null, ID: id}
	}
	raw := json.RawMessage(data)
	return Response{Jsonrpc: "2.0", Result: &raw, ID: id}
}

// RespErr builds an error response with no result field.
func RespErr(id any, code int, msg string) Response {
	return Response{Jsonrpc: "2.0", Error: &Error{Code: code, Message: msg}, ID: id}
}
