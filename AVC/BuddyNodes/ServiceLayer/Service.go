package ServiceLayer

import (
	"sync"

	"gossipnode/AVC/BuddyNodes/DataLayer"
	"gossipnode/AVC/BuddyNodes/Types"
)

var (
	Once              sync.Once
	ServiceController *Types.Controller
)

func GetServiceController() *Types.Controller {
	Once.Do(func() {
		ServiceController = DataLayer.GetCRDTLayer()
	})
	return ServiceController
}
