// Package correctness is a fixture of the static analysis gate (P23-T02): the
// printf analyzer of `go vet` must refuse it, so a source that prints a string
// with the integer verb lives here and nowhere else. This directory is under
// testdata/, which the go tool skips when a package pattern ends in ./..., so
// the fixture is never judged as delivered code — it exists to be refused.
package correctness

import "fmt"

func report(name string) {
	// %d receives a string: the printf analyzer names the verb and the argument.
	fmt.Printf("arena %d\n", name)
}
