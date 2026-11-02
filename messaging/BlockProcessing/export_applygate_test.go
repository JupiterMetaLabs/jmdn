//go:build applygate

package BlockProcessing

// SetGroupedApplyForTest pins features.grouped_apply_commit and the group
// commit budget for applygate tests. It returns a restore func.
func SetGroupedApplyForTest(on bool, budget int) func() {
	prevEnabled, prevBudget := groupedApplyEnabled, groupedApplyOpBudget
	groupedApplyEnabled = func() bool { return on }
	if budget > 0 {
		groupedApplyOpBudget = budget
	}
	return func() {
		groupedApplyEnabled, groupedApplyOpBudget = prevEnabled, prevBudget
	}
}
