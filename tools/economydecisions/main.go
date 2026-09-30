// Command economydecisions is the P31-T08 gate: it judges only versioned
// documents and exits non-zero while any critical business contract is
// unresolved.
//
// Usage: go run ./tools/economydecisions -root .
//
// Exit 0 holds every contract, 1 lists each violated rule, 2 is a usage
// error. Approval is read only from docs/reino/DECISOES_VIGENTES.md;
// there is no environment variable or flag that can simulate one.
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
	exitUsage     = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("economydecisions", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root holding the versioned documents")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "economydecisions takes no positional arguments")
		return exitUsage
	}

	findings := Audit(*root)
	if len(findings) == 0 {
		fmt.Fprintln(stdout, "economydecisions: contracts hold (decisions ratified, prices approved, time canonical, threats controlled, offers deferred)")
		return exitOK
	}
	for _, finding := range findings {
		fmt.Fprintf(stdout, "economydecisions: %s: %s\n", finding.Rule, finding.Detail)
	}
	return exitViolation
}
