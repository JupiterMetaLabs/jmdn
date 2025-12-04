package GRO

import(
	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
)

var(
	GlobalGRO	interfaces.GlobalGoroutineManagerInterface
)

// This is the variable tracking for all pulled up app manager
const(
	MainAM = "main"
	MainLM = "local"
	FacadeWG = "waitgroup:facade"
	FacadeThread = "thread:facade"
	WSServerThread = "thread:ws"
	CLIThread = "thread:cli"
	ExplorerThread = "thread:explorer"
	GETHgRPCThread = "thread:geth:grpc"
	BlockgRPCThread = "thread:block:grpc"
	BlockgenThread = "thread:block:gen"
	DIDThread = "thread:did"
	ShutdownThread = "thread:shutdown"
	BlockPollerThread = "thread:block:poller"
)