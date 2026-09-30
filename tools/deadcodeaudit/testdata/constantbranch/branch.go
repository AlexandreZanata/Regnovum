// Package constantbranch is the fixture of the decision already made: a branch
// whose condition is the literal `true` or `false`. The negative shape — a
// condition that is a value, including the constants a rule must not resolve —
// lives under testdata/clean/constantbranch.
package constantbranch

// Legacy is refused: the branch is written as a decision and is not one.
func Legacy(input string) string {
	if false {
		return "legacy path"
	}
	return input
}

// Guard is refused as well, and this is the shape that survives a review:
// `if true` reads like a guard and is a return.
func Guard(input string) string {
	if true {
		return input
	}
	return "unreachable by construction"
}
