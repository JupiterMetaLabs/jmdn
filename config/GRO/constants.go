package GRO

import(
	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
)

var(
	GlobalGRO	*interfaces.GlobalGoroutineManagerInterface
)

// This is the variable tracking for all pulled up app manager
const(
	MainAM = "main"
)