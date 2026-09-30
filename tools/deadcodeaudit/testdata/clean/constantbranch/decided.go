// Package constantbranch is the clean half of the rule about a decision already
// made: a condition that is a value is a decision somebody can still take, and
// the flags of the program are exactly that. Refusing these would make the rule a
// rule against configuration, and the fixture says so with the three shapes that
// look closest to the literal: a named constant, a comparison and a call.
package constantbranch

// Truth is a constant, and the rule reads literals in the condition — not the
// values constants hold, which would make it a type-checker.
const Truth = true

// Decided tests a flag the caller passes.
func Decided(input string, verbose bool) string {
	if verbose {
		return "verbose: " + input
	}
	return input
}

// Compared decides with a comparison, which is a decision about two values.
func Compared(input string, width int) string {
	if width > 0 {
		return input[:width]
	}
	return input
}

// Called decides with the answer of another function.
func Called() string {
	if enabled() {
		return "on"
	}
	return "off"
}

// Literal is the nearest miss: the branch is decided by a constant that holds
// the literal, and only a type-checker could see it. The rule reads the syntax of
// the condition and says so, instead of guessing what a constant holds.
func Literal() bool {
	if Truth {
		return true
	}
	return false
}

func enabled() bool { return true }
