// Command runbookaudit judges docs/RUNBOOKS.md against the tree: every
// incident command must be marked when destructive and scoped to a loopback
// or operator-composed host, and every link, anchor, route, script and make
// target it names must exist.
//
// Usage: go run ./tools/runbookaudit -root .
//
// Exit 0 holds the document, 1 lists every violation, 2 is a usage error.
// The command never rewrites anything: fixing a procedure is a human edit.
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
	flags := flag.NewFlagSet("runbookaudit", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root holding docs/RUNBOOKS.md")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "runbookaudit takes no positional arguments")
		return exitUsage
	}

	findings := Audit(*root)
	if len(findings) == 0 {
		fmt.Fprintf(stdout, "runbookaudit: %s holds (commands marked, hosts loopback, references resolve)\n", runbookPath)
		return exitOK
	}
	for _, finding := range findings {
		fmt.Fprintf(stdout, "runbookaudit: %s: %s\n", finding.Rule, finding.Detail)
	}
	return exitViolation
}
