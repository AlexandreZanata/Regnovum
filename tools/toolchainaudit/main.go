// Command toolchainaudit judges the production toolchain pins against
// quality/toolchain.json: any drift fails, so production can only move
// versions through the declaration.
//
// Usage:
//
//	go run ./tools/toolchainaudit -root .          # gate: exit 1 on drift
//	go run ./tools/toolchainaudit -root . -report  # monitor: always exit 0
//
// The report mode feeds the informative toolchain job: it prints which
// pins hold, which drifted and which next versions are approved, without
// touching a lockfile. Exit 0 holds or reports, 1 lists drift findings,
// 2 is a usage error.
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
	flags := flag.NewFlagSet("toolchainaudit", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root holding quality/toolchain.json")
	report := flags.Bool("report", false, "print the informative verdict instead of judging")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "toolchainaudit takes no positional arguments")
		return exitUsage
	}

	if *report {
		for _, line := range Report(*root) {
			fmt.Fprintln(stdout, line)
		}
		return exitOK
	}
	findings := Audit(*root)
	if len(findings) == 0 {
		fmt.Fprintln(stdout, "toolchainaudit: production pins hold")
		return exitOK
	}
	for _, finding := range findings {
		fmt.Fprintf(stdout, "toolchainaudit: %s: %s\n", finding.Rule, finding.Detail)
	}
	return exitViolation
}
