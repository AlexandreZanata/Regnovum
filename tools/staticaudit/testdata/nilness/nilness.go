// Package nilness is a fixture of the static analysis gate (P23-T02): the
// nilness half of the pinned analyzer must refuse it. Writing to a nil map is
// the shape that cannot work at runtime and that a reader does not see.
//
// The directory lives under testdata/, which the go tool skips when a pattern
// ends in ./..., so the fixture is never judged as delivered code: it exists to
// be refused, and the gate fails when it stops being refused.
package nilness

// Record writes into a map that was never made.
func Record() {
	var counts map[string]int
	counts["arena"] = 1
}
