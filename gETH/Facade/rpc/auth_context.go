package rpc

// rpcAuthHeaderKey is the context key used to pass the HTTP Authorization
// header into JSON-RPC method handlers for expensive-method admin gating.
type rpcAuthHeaderCtxKey struct{}

var rpcAuthHeaderKey = rpcAuthHeaderCtxKey{}
