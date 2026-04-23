package common

import (
	"fmt"

	GRO "gossipnode/config/GRO"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
)

var (
	LocalGRO interfaces.LocalGoroutineManagerInterface
)

func InitializeGRO(Local string) (interfaces.LocalGoroutineManagerInterface, error) {
	var err error
	LocalGRO, err = GRO.GetApp(GRO.ExplorerApp).NewLocalManager(Local)
	if err != nil {
		return nil, fmt.Errorf("failed to create local manager: %w", err)
	}
	return LocalGRO, nil
}
