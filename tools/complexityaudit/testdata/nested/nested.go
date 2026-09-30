// Package nested is a fixture of the complexity gate: control flow nested one
// level deeper than the budget allows. The decision count stays low, so only the
// nesting rule can refuse it — the two measures are not the same measure written
// twice.
package nested

// tooDeep nests the branches to be refused.
func tooDeep(value int) int {
	if value > 0 {
		if value > 1 {
			if value > 2 {
				if value > 3 {
					if value > 4 {
						return 5
					}
					return 4
				}
				return 3
			}
			return 2
		}
		return 1
	}
	return 0
}
