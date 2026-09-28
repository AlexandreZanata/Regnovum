// Command capacityratchet is the gate of the capacity baseline (P28-T08).
//
//	capacityratchet [-root .] [-baseline quality/capacity-baseline.json]
//	  [-schema quality/capacity-baseline.schema.json] [-report docs/CAPACITY.md]
//
// It judges the pinned numbers against the checkout — k6 thresholds,
// Go budgets and byte ceilings, product bound constants — plus the
// hardware the comparison runs on and the report citing this baseline.
// Any divergence fails: a loosened bound is a regression, a tightened one
// is a baseline the tree outgrew, and both move only by human commit. It
// never writes, so a green run can never rewrite what it judges.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
)

// Exit statuses, as a vocabulary the tests can name.
const (
	exitOK        = 0
	exitViolation = 1
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the whole command, with its status as a value: the contract of a
// gate is about exit statuses, and a contract that only lives inside main is
// one no test can hold.
func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("capacityratchet", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root the baseline is judged against")
	baseline := flags.String("baseline", BaselinePath, "capacity baseline, relative to the root")
	schema := flags.String("schema", SchemaPath, "baseline schema, relative to the root")
	report := flags.String("report", ReportPath, "capacity report, relative to the root")
	if err := flags.Parse(args); err != nil {
		return exitViolation
	}
	findings := Validate(*root, *baseline, *schema, *report, runtime.GOOS, runtime.GOARCH)
	if len(findings) != 0 {
		for _, finding := range findings {
			fmt.Fprintf(stderr, "capacityratchet: %s — %s\n", finding.Rule, finding.Detail)
		}
		return exitViolation
	}
	parsed, err := ReadBaseline(*root, *baseline)
	if err != nil {
		fmt.Fprintf(stderr, "capacityratchet: %v\n", err)
		return exitViolation
	}
	rules, tests := 0, 0
	for _, file := range parsed.K6Thresholds {
		rules++
		tests += len(file.P95Ms)
	}
	fmt.Fprintf(stdout, "capacityratchet: baseline %s holds (%d k6 files, %d thresholds, commit %s)\n",
		*baseline, rules, tests, parsed.Commit)
	return exitOK
}
