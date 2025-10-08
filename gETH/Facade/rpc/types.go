package rpc

type Request struct {
	Jsonrpc string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []any         `json:"params"`
	ID      any           `json:"id"`
}
type Response struct {
	Jsonrpc string `json:"jsonrpc"`
	Result  any    `json:"result,omitempty"`
	Error   *Error `json:"error,omitempty"`
	ID      any    `json:"id"`
}
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func RespOK(id any, v any) Response  { return Response{Jsonrpc: "2.0", Result: v, ID: id} }
func RespErr(id any, code int, msg string) Response {
	return Response{Jsonrpc: "2.0", Error: &Error{Code: code, Message: msg}, ID: id}
}
