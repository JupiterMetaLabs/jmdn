package messaging

import (
	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
)

var (
	DIDLocalGRO interfaces.LocalGoroutineManagerInterface
	BroadcastLocalGRO interfaces.LocalGoroutineManagerInterface
	BlockPropagationLocalGRO interfaces.LocalGoroutineManagerInterface
)