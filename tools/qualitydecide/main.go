// Command qualitydecide judges one verified evidence bundle and emits
// the release certification verdict: PASS with no reasons, or FAIL with
// every reason in a stable vocabulary.
//
// Usage: go run ./tools/qualitydecide -bundle <dir>
//
// The bundle is the only input: tiers, waivers and verdicts all come
// from inside it, after the seal and every checksum verify. There is no
// manual input, no environment override and no flag that can flip the
// verdict — the flags name the bundle, never the decision. Exit 0 is
// PASS, 1 is FAIL with the reasons listed, 2 is a usage error.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

// Exit statuses, as a vocabulary the tests can name.
const (
	exitPass  = 0
	exitFail  = 1
	exitUsage = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, time.Now().UTC()))
}

func run(args []string, stdout, stderr io.Writer, now time.Time) int {
	flags := flag.NewFlagSet("qualitydecide", flag.ContinueOnError)
	flags.SetOutput(stderr)
	bundle := flags.String("bundle", "", "evidence bundle directory to judge")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if flags.NArg() != 0 || *bundle == "" {
		fmt.Fprintln(stderr, "qualitydecide needs -bundle")
		return exitUsage
	}

	decision := Decide(*bundle, now)
	encoded, err := json.MarshalIndent(decision, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, "qualitydecide cannot render the decision")
		return exitUsage
	}
	fmt.Fprintln(stdout, string(encoded))
	if decision.Decision == DecisionPass {
		return exitPass
	}
	return exitFail
}
