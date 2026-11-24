package messaging

import (
	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
)

var (
	BroadcastLocalGRO        interfaces.LocalGoroutineManagerInterface
	BlockPropagationLocalGRO interfaces.LocalGoroutineManagerInterface
	ContractLocalGRO         interfaces.LocalGoroutineManagerInterface
)
