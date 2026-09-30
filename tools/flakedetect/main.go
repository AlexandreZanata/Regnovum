// Command flakedetect hunts intermittent backend tests (P27-T02). It runs
// `go test` N times varying order, parallelism and seeds, records every
// outcome with its failure signature, and judges the quarantine register:
// a flake without an entry fails, an expired entry fails, and a stale
// entry (green across the run it was supposed to fail) fails. CI retry
// is prohibited: a retried red is a hidden red, so the workflows must not
// contain retry steps, which the self-tests assert.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

const (
	exitClean    = 0
	exitFindings = 1
	exitUsage    = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the whole command, with its exit status as a value: the gate is a
// contract about exit codes, and a contract that lives inside main is a
// contract no test can hold.
func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("flakedetect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	packages := flags.String("package", "./...", "package pattern under test")
	tests := flags.String("run", "", "test name filter (passed to go test -run)")
	runs := flags.Int("runs", 10, "executions")
	shuffle := flags.Bool("shuffle", true, "vary test order per run (go test -shuffle)")
	parallel := flags.Int("parallel", 4, "test parallelism per run")
	seeds := flags.String("seeds", "", "comma-separated ARENA_TEST_SEED values, cycled per run (empty keeps the ambient seed)")
	quarantine := flags.String("quarantine", "quality/flake-quarantine.json", "quarantine register")
	timeout := flags.Duration("timeout", 30*time.Minute, "overall detection budget")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if *runs < 2 {
		fmt.Fprintln(stderr, "flakedetect: -runs needs at least 2 (one run cannot disagree with itself)")
		return exitUsage
	}
	config, err := loadConfig(Options{
		Packages: *packages, Tests: *tests, Runs: *runs, Shuffle: *shuffle,
		Parallel: *parallel, Seeds: *seeds, Quarantine: *quarantine, Timeout: *timeout,
	})
	if err != nil {
		fmt.Fprintf(stderr, "flakedetect: %v\n", err)
		return exitUsage
	}
	report, err := detect(config)
	if err != nil {
		fmt.Fprintf(stderr, "flakedetect: %v\n", err)
		return exitFindings
	}
	writeReport(stdout, report)
	if report.Blocking() {
		return exitFindings
	}
	return exitClean
}
