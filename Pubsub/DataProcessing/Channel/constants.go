package Channel

import (
	"fmt"

	"gossipnode/config/GRO"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
)

var (
	LocalGRO interfaces.LocalGoroutineManagerInterface
)

func InitializeGRO() (interfaces.LocalGoroutineManagerInterface, error) {
	var err error
	LocalGRO, err = GRO.GetApp(GRO.PubsubApp).NewLocalManager(GRO.PubsubChannelLocal)
	if err != nil {
		return nil, fmt.Errorf("failed to create local manager: %w", err)
	}
	return LocalGRO, nil
}
