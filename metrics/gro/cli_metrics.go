package gro

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/global"
	"github.com/JupiterMetaLabs/goroutine-orchestrator/manager/interfaces"
	"github.com/JupiterMetaLabs/goroutine-orchestrator/types"
	"github.com/olekukonko/tablewriter"
)

type groMetrics struct {
	globalManager interfaces.GlobalGoroutineManagerInterface
	app           app
	local         local
	goroutines    goroutines
}

type app struct {
	appManagers     []*types.AppManager
	appManagerCount int
}

type local struct {
	localManagers        []*types.LocalManager
	localManagerCount    int
	localmanagerRoutines map[string]int
}

type goroutines struct {
	functionNames   map[string]int
	goroutinesCount int
	waitGroups      map[string]int
}

func getGoroutinesCount(localManager *types.LocalManager) int {
	goroutinesCount := localManager.GetRoutineCount()
	return goroutinesCount
}

// GroMetrics collects and prints all GRO metrics
// All output is buffered and printed atomically to prevent interleaving with log messages
func GroMetrics(detailed bool) error {
	globalManager := global.NewGlobalManager()
	globalMgr, err := globalManager.Get()
	if err != nil || globalMgr == nil {
		return fmt.Errorf("failed to get global manager: %w", err)
	}

	// Initialize groMetrics with global manager interface
	g := &groMetrics{
		globalManager: globalManager,
	}

	// Build metrics into a buffer for atomic printing
	var buf bytes.Buffer
	g.BuildToBuffer(detailed, &buf)

	// Print the entire buffer atomically
	// Write in chunks to ensure complete output even if interrupted
	data := buf.Bytes()
	totalWritten := 0
	for totalWritten < len(data) {
		n, err := os.Stdout.Write(data[totalWritten:])
		if err != nil {
			return fmt.Errorf("failed to write metrics output: %w", err)
		}
		totalWritten += n
	}

	// Ensure output is flushed
	os.Stdout.Sync()
	return nil
}

func (g *groMetrics) getAppManagers() *groMetrics {
	appManagers, err := g.globalManager.GetAllAppManagers()
	if err != nil {
		return g
	}
	appManagerCount := len(appManagers)
	g.app = app{
		appManagers:     appManagers,
		appManagerCount: appManagerCount,
	}
	return g
}

func (g *groMetrics) getLocalManagers() *groMetrics {
	localManagers, err := g.globalManager.GetAllLocalManagers()
	if err != nil {
		return g
	}
	localManagerCount := len(localManagers)
	g.local = local{
		localManagers:        localManagers,
		localManagerCount:    localManagerCount,
		localmanagerRoutines: make(map[string]int),
	}
	return g
}

func (g *groMetrics) getLocalmanagerRoutinesCount() *groMetrics {
	if g.local.localmanagerRoutines == nil {
		g.local.localmanagerRoutines = make(map[string]int)
	}
	for _, localManager := range g.local.localManagers {
		if localManager == nil {
			continue
		}
		localmanagerRoutines := getGoroutinesCount(localManager)
		g.local.localmanagerRoutines[localManager.GetLocalName()] = localmanagerRoutines
	}
	return g
}
func (g *groMetrics) getAllGoroutinesCount() *groMetrics {
	goroutinesCount := g.globalManager.GetGoroutineCount()
	g.goroutines.goroutinesCount = goroutinesCount
	return g
}

func (g *groMetrics) getWaitGroupCount() *groMetrics {
	// Initialize waitGroups map if nil
	if g.goroutines.waitGroups == nil {
		g.goroutines.waitGroups = make(map[string]int)
	}

	// Initialize functionNames map if nil
	if g.goroutines.functionNames == nil {
		g.goroutines.functionNames = make(map[string]int)
	}

	for _, localManager := range g.local.localManagers {
		if localManager == nil {
			continue
		}

		// Get function goroutine counts for this local manager
		functionCounts := getFunctionGoroutinesCount(localManager)

		// Merge function counts into g.goroutines.functionNames
		for functionName, count := range functionCounts {
			g.goroutines.functionNames[localManager.GetLocalName()+":"+functionName] += count
		}

		// Get wait group count for this local manager
		waitGroupCount := localManager.GetFunctionWgCount()
		g.goroutines.waitGroups[localManager.GetLocalName()] = waitGroupCount
	}
	return g
}

// getFunctionGoroutinesCount returns a map of function names to their goroutine counts
// for a given LocalManager
func getFunctionGoroutinesCount(localManager *types.LocalManager) map[string]int {
	// Initialize the result map
	functionCounts := make(map[string]int)

	// Get all routines from the local manager
	routines := localManager.GetRoutines()

	// Count routines per function name
	// The Routines map key is the function name, so we can iterate directly
	for functionName, routine := range routines {
		if routine != nil {
			// If the map key is the function name, we can count occurrences
			// Or if each routine has a function name, we count by that
			actualFunctionName := routine.GetFunctionName()
			if actualFunctionName != "" {
				functionCounts[actualFunctionName]++
			} else if functionName != "" {
				// Fallback to map key if routine doesn't have function name
				functionCounts[functionName]++
			}
		}
	}

	return functionCounts
}

func (g *groMetrics) Build(detailed bool) {
	builder := g.getAppManagers().getLocalManagers().getLocalmanagerRoutinesCount().getAllGoroutinesCount().getWaitGroupCount()

	if detailed {
		// Print all metrics in pretty tables
		builder.printAllMetrics()
	} else {
		// Non-detailed: only print summary, wait groups, and function names
		builder.printSummary()
		builder.printWaitGroups()
		builder.printFunctionNames()
	}
}

// BuildToBuffer builds metrics and writes them to a buffer for atomic printing
func (g *groMetrics) BuildToBuffer(detailed bool, buf *bytes.Buffer) {
	builder := g.getAppManagers().getLocalManagers().getLocalmanagerRoutinesCount().getAllGoroutinesCount().getWaitGroupCount()

	if detailed {
		// Print all metrics in pretty tables to buffer
		builder.printAllMetricsToBuffer(buf)
	} else {
		// Non-detailed: only print summary, wait groups, and function names to buffer
		builder.printSummaryToBuffer(buf)
		builder.printWaitGroupsToBuffer(buf)
		builder.printFunctionNamesToBuffer(buf)
	}
}

// printAllMetrics prints all collected metrics in formatted tables
func (g *groMetrics) printAllMetrics() {
	fmt.Println("\n" + strings.Repeat("=", 80))
	fmt.Println("GRO METRICS REPORT")
	fmt.Println(strings.Repeat("=", 80))

	// 1. App Managers
	g.printAppManagers()

	// 2. Local Managers with Goroutine Counts
	g.printLocalManagers()

	// 3. Wait Groups
	g.printWaitGroups()

	// 4. Function Names with Goroutine Counts
	g.printFunctionNames()

	// 5. Total Summary
	g.printSummary()

	fmt.Println(strings.Repeat("=", 80) + "\n")
}

// printAllMetricsToBuffer prints all collected metrics to a buffer
func (g *groMetrics) printAllMetricsToBuffer(buf *bytes.Buffer) {
	fmt.Fprintf(buf, "\n%s\n", strings.Repeat("=", 80))
	fmt.Fprintf(buf, "GRO METRICS REPORT\n")
	fmt.Fprintf(buf, "%s\n", strings.Repeat("=", 80))

	// 1. App Managers
	g.printAppManagersToBuffer(buf)

	// 2. Local Managers with Goroutine Counts
	g.printLocalManagersToBuffer(buf)

	// 3. Wait Groups
	g.printWaitGroupsToBuffer(buf)

	// 4. Function Names with Goroutine Counts
	g.printFunctionNamesToBuffer(buf)

	// 5. Total Summary
	g.printSummaryToBuffer(buf)

	fmt.Fprintf(buf, "%s\n\n", strings.Repeat("=", 80))
}

func (g *groMetrics) printAppManagers() {
	fmt.Println("\n📱 APP MANAGERS")
	fmt.Println(strings.Repeat("-", 80))
	if g.app.appManagerCount == 0 {
		fmt.Println("  No app managers found")
		return
	}

	fmt.Printf("  Total App Managers: %d\n", g.app.appManagerCount)
	for i, appManager := range g.app.appManagers {
		if appManager != nil {
			fmt.Printf("  [%d] %s\n", i+1, appManager.GetAppName())
		}
	}
}

func (g *groMetrics) printAppManagersToBuffer(buf *bytes.Buffer) {
	fmt.Fprintf(buf, "\n📱 APP MANAGERS\n")
	fmt.Fprintf(buf, "%s\n", strings.Repeat("-", 80))
	if g.app.appManagerCount == 0 {
		fmt.Fprintf(buf, "  No app managers found\n")
		return
	}

	fmt.Fprintf(buf, "  Total App Managers: %d\n", g.app.appManagerCount)
	for i, appManager := range g.app.appManagers {
		if appManager != nil {
			fmt.Fprintf(buf, "  [%d] %s\n", i+1, appManager.GetAppName())
		}
	}
}

func (g *groMetrics) printLocalManagers() {
	fmt.Println("\n🏠 LOCAL MANAGERS & GOROUTINE COUNTS")
	fmt.Println(strings.Repeat("-", 80))

	if g.local.localManagerCount == 0 {
		fmt.Println("  No local managers found")
		return
	}

	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"Local Manager", "Goroutines"})
	table.SetBorder(true)
	table.SetColumnSeparator("│")
	table.SetCenterSeparator("│")
	table.SetRowSeparator("─")
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)

	for _, localManager := range g.local.localManagers {
		if localManager == nil {
			continue
		}
		localName := localManager.GetLocalName()
		routineCount := g.local.localmanagerRoutines[localName]
		table.Append([]string{localName, fmt.Sprintf("%d", routineCount)})
	}

	table.Render()
	fmt.Println()
}

func (g *groMetrics) printLocalManagersToBuffer(buf *bytes.Buffer) {
	fmt.Fprintf(buf, "\n🏠 LOCAL MANAGERS & GOROUTINE COUNTS\n")
	fmt.Fprintf(buf, "%s\n", strings.Repeat("-", 80))

	if g.local.localManagerCount == 0 {
		fmt.Fprintf(buf, "  No local managers found\n")
		return
	}

	table := tablewriter.NewWriter(buf)
	table.SetHeader([]string{"Local Manager", "Goroutines"})
	table.SetBorder(true)
	table.SetColumnSeparator("│")
	table.SetCenterSeparator("│")
	table.SetRowSeparator("─")
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)

	for _, localManager := range g.local.localManagers {
		if localManager == nil {
			continue
		}
		localName := localManager.GetLocalName()
		routineCount := g.local.localmanagerRoutines[localName]
		table.Append([]string{localName, fmt.Sprintf("%d", routineCount)})
	}

	table.Render()
	fmt.Fprintf(buf, "\n")
}

func (g *groMetrics) printWaitGroups() {
	fmt.Println("\n⏳ WAIT GROUPS")
	fmt.Println(strings.Repeat("-", 80))

	if len(g.goroutines.waitGroups) == 0 {
		fmt.Println("  No wait groups found")
		return
	}

	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"Local Manager", "Wait Groups"})
	table.SetBorder(true)
	table.SetColumnSeparator("│")
	table.SetCenterSeparator("│")
	table.SetRowSeparator("─")
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)

	for localName, count := range g.goroutines.waitGroups {
		table.Append([]string{localName, fmt.Sprintf("%d", count)})
	}

	table.Render()
	fmt.Println()
}

func (g *groMetrics) printWaitGroupsToBuffer(buf *bytes.Buffer) {
	fmt.Fprintf(buf, "\n⏳ WAIT GROUPS\n")
	fmt.Fprintf(buf, "%s\n", strings.Repeat("-", 80))

	if len(g.goroutines.waitGroups) == 0 {
		fmt.Fprintf(buf, "  No wait groups found\n")
		return
	}

	table := tablewriter.NewWriter(buf)
	table.SetHeader([]string{"Local Manager", "Wait Groups"})
	table.SetBorder(true)
	table.SetColumnSeparator("│")
	table.SetCenterSeparator("│")
	table.SetRowSeparator("─")
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)

	for localName, count := range g.goroutines.waitGroups {
		table.Append([]string{localName, fmt.Sprintf("%d", count)})
	}

	table.Render()
	fmt.Fprintf(buf, "\n")
}

func (g *groMetrics) printFunctionNames() {
	fmt.Println("\n🔧 FUNCTION NAMES & GOROUTINE COUNTS")
	fmt.Println(strings.Repeat("-", 80))

	if len(g.goroutines.functionNames) == 0 {
		fmt.Println("  No function names found")
		return
	}

	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"Function Name", "Goroutines"})
	table.SetBorder(true)
	table.SetColumnSeparator("│")
	table.SetCenterSeparator("│")
	table.SetRowSeparator("─")
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)

	for functionName, count := range g.goroutines.functionNames {
		table.Append([]string{functionName, fmt.Sprintf("%d", count)})
	}

	table.Render()
	fmt.Println()
}

func (g *groMetrics) printFunctionNamesToBuffer(buf *bytes.Buffer) {
	fmt.Fprintf(buf, "\n🔧 FUNCTION NAMES & GOROUTINE COUNTS\n")
	fmt.Fprintf(buf, "%s\n", strings.Repeat("-", 80))

	if len(g.goroutines.functionNames) == 0 {
		fmt.Fprintf(buf, "  No function names found\n")
		return
	}

	table := tablewriter.NewWriter(buf)
	table.SetHeader([]string{"Function Name", "Goroutines"})
	table.SetBorder(true)
	table.SetColumnSeparator("│")
	table.SetCenterSeparator("│")
	table.SetRowSeparator("─")
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)

	for functionName, count := range g.goroutines.functionNames {
		table.Append([]string{functionName, fmt.Sprintf("%d", count)})
	}

	table.Render()
	fmt.Fprintf(buf, "\n")
}

func (g *groMetrics) printSummary() {
	fmt.Println("\n📊 SUMMARY")
	fmt.Println(strings.Repeat("-", 80))

	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"Metric", "Count"})
	table.SetBorder(true)
	table.SetColumnSeparator("│")
	table.SetCenterSeparator("│")
	table.SetRowSeparator("─")
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)

	table.Append([]string{"Total App Managers", fmt.Sprintf("%d", g.app.appManagerCount)})
	table.Append([]string{"Total Local Managers", fmt.Sprintf("%d", g.local.localManagerCount)})
	table.Append([]string{"Total Goroutines", fmt.Sprintf("%d", g.goroutines.goroutinesCount)})
	table.Append([]string{"Total Functions", fmt.Sprintf("%d", len(g.goroutines.functionNames))})
	table.Append([]string{"Total Wait Groups", fmt.Sprintf("%d", len(g.goroutines.waitGroups))})

	table.Render()
	fmt.Println()
}

func (g *groMetrics) printSummaryToBuffer(buf *bytes.Buffer) {
	fmt.Fprintf(buf, "\n📊 SUMMARY\n")
	fmt.Fprintf(buf, "%s\n", strings.Repeat("-", 80))

	table := tablewriter.NewWriter(buf)
	table.SetHeader([]string{"Metric", "Count"})
	table.SetBorder(true)
	table.SetColumnSeparator("│")
	table.SetCenterSeparator("│")
	table.SetRowSeparator("─")
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetHeaderAlignment(tablewriter.ALIGN_LEFT)

	table.Append([]string{"Total App Managers", fmt.Sprintf("%d", g.app.appManagerCount)})
	table.Append([]string{"Total Local Managers", fmt.Sprintf("%d", g.local.localManagerCount)})
	table.Append([]string{"Total Goroutines", fmt.Sprintf("%d", g.goroutines.goroutinesCount)})
	table.Append([]string{"Total Functions", fmt.Sprintf("%d", len(g.goroutines.functionNames))})
	table.Append([]string{"Total Wait Groups", fmt.Sprintf("%d", len(g.goroutines.waitGroups))})

	table.Render()
	fmt.Fprintf(buf, "\n")
}
