// Command qualitytaxonomy is the executable test-evidence taxonomy (P21-T05).
//
// It reads quality/evidence.json and judges every identity in it: which kind of
// test the evidence is, which suite it runs in, which rules it proves, in which
// class and in which environment. The classification is declared data, never
// inferred from a Go function name — a name that carries "Integration" is not an
// integration test, and the day somebody renames it is the day an inferred
// taxonomy would quietly change its mind.
//
// The classes come from quality/catalog.json, so a rule's class lives in one
// place; the tests come from the tree, so an identity that names a test nobody
// can run is refused; and the published schema is held to the loader's own
// vocabulary, because two contracts that disagree are worse than none.
//
// It never writes and it never runs a test. What it produces is the judgement
// the inventory of P21-T06 will read.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

// Exit statuses, as a vocabulary the tests can name.
const (
	exitOK        = 0
	exitViolation = 1
)

// registerPath is the register, relative to the repository root.
const registerPath = "quality/evidence.json"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the whole command, with its status as a value: the contract of a gate
// is about exit statuses, and a contract that only lives inside main is one no
// test can hold.
func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("qualitytaxonomy", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root the register is judged against")
	if err := flags.Parse(args); err != nil {
		return exitViolation
	}

	records, violations := ReadRecords(*root)
	var register Register
	if len(violations) == 0 {
		register, violations = ReadRegister(*root, registerPath, records)
	}
	if len(violations) == 0 {
		violations = ReadSchema(*root)
	}
	if len(violations) == 0 {
		covered := map[string]bool{}
		for _, evidence := range register.Evidence {
			covered[evidence.Kind] = true
		}
		fmt.Fprintf(stdout, "qualitytaxonomy: %d evidence row(s) of %s in %d suite(s), classified in %d of %d kind(s) against %d catalog rule(s), with %s agreed\n",
			len(register.Evidence), registerPath, len(register.Suites), len(covered), len(Kinds), len(records.Rules), SchemaPath)
		return exitOK
	}

	for _, violation := range violations {
		fmt.Fprintf(stderr, "qualitytaxonomy: %s\n", violation)
	}
	fmt.Fprintf(stderr, "qualitytaxonomy: %d violation(s) — evidence nobody can find is evidence that does not guard anything\n", len(violations))
	return exitViolation
}
