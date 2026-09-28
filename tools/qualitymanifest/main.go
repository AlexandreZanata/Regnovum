// Command qualitymanifest builds and verifies deterministic quality
// evidence bundles: manifest.json, its checksum and every artifact the
// certification reads, with nothing else.
//
// Usage:
//
//	go run ./tools/qualitymanifest -root . -evidence <dir> -out <bundle>
//	go run ./tools/qualitymanifest -check <bundle>
//
// The build refuses a missing field, a missing artifact, an empty
// evidence set, a dirty tree and any PII or secret; the check re-hashes
// every file and fails on any drift. Neither mode touches the tree or
// the evidence source. Exit 0 holds, 1 lists every finding, 2 is a usage
// error.
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
	flags := flag.NewFlagSet("qualitymanifest", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root the bundle describes")
	evidence := flags.String("evidence", "", "directory of run-result files to bundle")
	out := flags.String("out", "", "bundle directory to write")
	check := flags.String("check", "", "bundle directory to verify instead of building")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "qualitymanifest takes no positional arguments")
		return exitUsage
	}

	if *check != "" {
		if findings := Verify(*check); len(findings) != 0 {
			for _, finding := range findings {
				fmt.Fprintf(stdout, "qualitymanifest: %s: %s\n", finding.Rule, finding.Detail)
			}
			return exitViolation
		}
		fmt.Fprintln(stdout, "qualitymanifest: bundle holds")
		return exitOK
	}
	if *evidence == "" || *out == "" {
		fmt.Fprintln(stderr, "qualitymanifest build needs -evidence and -out")
		return exitUsage
	}
	if findings := Build(*out, Collect(*root, *evidence)); len(findings) != 0 {
		for _, finding := range findings {
			fmt.Fprintf(stdout, "qualitymanifest: %s: %s\n", finding.Rule, finding.Detail)
		}
		return exitViolation
	}
	fmt.Fprintln(stdout, "qualitymanifest: bundle written")
	return exitOK
}
