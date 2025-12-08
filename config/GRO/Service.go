package GRO

/*
Eager Loading of App and Local GoRoutine Managers for all the packages
*/

import (
	"fmt"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/global"
	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
)

func InitGlobal() {
	if GlobalGRO == nil {
		GlobalGRO = global.NewGlobalManager()
	}
    if apps == nil {
		apps = &appmanager{
			Apps: make(map[string]interfaces.AppGoroutineManagerInterface),
		}
	}
}

// You have to populate the apps in the constants.go file - so that from the main funciton all the apps are loaded into the memory
// Keep it ready for the executions
func EagerLoading() error {
	if apps == nil || GlobalGRO == nil {
		InitGlobal()
	}

	appsToLoad := getAllAppNames()

	for _, app := range appsToLoad {
		initApp(app)
	}
	return nil
}

func GetApp(appName string) interfaces.AppGoroutineManagerInterface {
	if apps.Apps[appName] == nil {
		return nil
	}
	return apps.Apps[appName]
}

func initApp(appName string) {
	if GlobalGRO == nil || apps == nil {
		InitGlobal()
	}

	if apps.Apps[appName] == nil {
		AppGRO, err := GlobalGRO.NewAppManager(appName)
		if err != nil {
			panic(fmt.Sprintf("failed to create AppGRO for %s: %v", appName, err))
		}
		apps.Apps[appName] = AppGRO
	}
}

func getAllAppNames() []string {
	return []string{
		MainAM,
		SequencerApp,
		SeedApp,
		PubsubApp,
		NodeApp,
	}
}