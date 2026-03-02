package common

import (
	"fmt"

	"jmdn/config/GRO"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
)

var (
	LocalGRO interfaces.LocalGoroutineManagerInterface
)

func InitializeGRO(Local string) (interfaces.LocalGoroutineManagerInterface, error) {
	var err error
	LocalGRO, err = GRO.GetApp(GRO.CRDTSyncApp).NewLocalManager(Local)
	if err != nil {
		return nil, fmt.Errorf("failed to create local manager: %w", err)
	}
	return LocalGRO, nil
}
