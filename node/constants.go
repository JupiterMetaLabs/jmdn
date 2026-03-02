package node

import (
	"fmt"

	"jmdn/config/GRO"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
)

var (
	LocalGRO interfaces.LocalGoroutineManagerInterface
)

func InitializeNode() (interfaces.LocalGoroutineManagerInterface, error) {
	var err error
	LocalGRO, err = GRO.GetApp(GRO.NodeApp).NewLocalManager(GRO.NodeLocal)
	if err != nil {
		return nil, fmt.Errorf("failed to create local manager: %w", err)
	}
	return LocalGRO, nil
}
