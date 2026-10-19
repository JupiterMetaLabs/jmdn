package settings

// GRPCReflectionAllowed reports whether gRPC servers may register the
// reflection service, which publishes every service and method they expose.
// It is off on mainnet and whenever configuration has not been loaded.
func GRPCReflectionAllowed() bool {
	return IsLoaded() && Get().Network.Environment != "mainnet"
}
