// Package placeholder is the clean half of the panic rule: a panic that states a
// truth about what the program does not serve is not a placeholder, and refusing
// it would push the message out of the code and into a log nobody reads. The
// vocabulary is what the rule reads: "not supported" describes the program,
// "not implemented" describes the work.
package placeholder

import "fmt"

// Unsupported names the command it will not serve.
func Unsupported(command string) string {
	switch command {
	case "open":
		return "open"
	default:
		panic(fmt.Sprintf("command %q is not supported", command))
	}
}

// Inconsistent refuses a state that cannot be true, with an expression instead
// of a literal: the gate reads the message of a literal and reports that it
// could not read an expression, so a panic built by fmt.Sprintf proves the rule
// does not guess.
func Inconsistent(carrier any) {
	if carrier != nil {
		panic(fmt.Sprintf("carrier %v was already set", carrier))
	}
}
