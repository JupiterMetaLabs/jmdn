package GRO

import (
	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
)

type appmanager struct {
	Apps map[string]interfaces.AppGoroutineManagerInterface `json:"app_manager"`
}
