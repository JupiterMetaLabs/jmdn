package settings

// GRPCReflectionAllowed reports whether gRPC servers may register the
// reflection service, which publishes every service and method they expose.
//
// It defers to ProductionPosture rather than comparing network.environment
// itself. A direct `!= "mainnet"` is case- and whitespace-sensitive, and
// ValidateEnvironment lowercases only a local copy, so a config carrying
// "Mainnet" or "mainnet " passes validation while Network.Environment keeps
// the original spelling — and the reflection service would be published on a
// mainnet node. ProductionPosture folds case and trims, and also treats
// security.strict_posture as production, which is the behaviour wanted here.
//
// Off whenever configuration has not been loaded.
func GRPCReflectionAllowed() bool {
	return IsLoaded() && !Get().ProductionPosture()
}
