// Package unreachable is the fixture of the code that cannot run: the statement
// after an unconditional terminator in the same block. The label that is a jump
// target — the shape the rule has to leave alone — lives under
// testdata/clean/unreachable.
package unreachable

// Answer is refused: the assignment and the second return after the first return
// never run.
func Answer() int {
	return 1
	count := 2
	return count
}

// Refuse is refused for the same reason and names a panic as the terminator.
func Refuse() int {
	panic("the caller asked for something this function does not serve")
	return 2
}

// Switch names the third terminator of the rule, and it is a fixture the Go
// compiler would reject: the language requires a fallthrough to close its
// clause. The rule reads syntax and not the compiler, so this is the only shape
// that can prove the branch bites — and a rule with a terminator nobody exercises
// is a rule that would stop refusing it without anybody noticing.
func Switch(kind string) int {
	switch kind {
	case "one":
		fallthrough
		return 1
	default:
		return 0
	}
}
