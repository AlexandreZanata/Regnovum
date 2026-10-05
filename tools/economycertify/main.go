// Command economycertify judges one financial certification bundle
// file and emits the verdict: PASS with no reasons, or FAIL with
// every reason in a stable vocabulary.
//
// Usage: go run ./tools/economycertify -bundle <file> [-commit <sha>]
//
// The bundle is the only input: the optional commit holds the
// bundle's own SHA, never the decision. Exit 0 is PASS, 1 is FAIL
// with the reasons listed, 2 is a usage error.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
)

// Exit statuses, as a vocabulary the tests can name.
const (
	exitPass  = 0
	exitFail  = 1
	exitUsage = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("economycertify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	bundle := flags.String("bundle", "", "certification bundle file to judge")
	commit := flags.String("commit", "", "expected bundle commit SHA to hold")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if flags.NArg() != 0 || *bundle == "" {
		fmt.Fprintln(stderr, "economycertify needs -bundle")
		return exitUsage
	}
	decision := Decide(*bundle, *commit)
	encoded, err := json.MarshalIndent(decision, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, "economycertify cannot render the decision")
		return exitUsage
	}
	fmt.Fprintln(stdout, string(encoded))
	if decision.Decision == DecisionPass {
		return exitPass
	}
	return exitFail
}
