// Command qualitywaivers is the executable waiver policy (P21-T04).
//
// It reads quality/waivers.json and judges every exception against the records
// the checkout keeps: the catalog, which states how serious the rule a waiver
// covers is; the audits, which state that the finding it accepts exists; and
// the tree, which is where the compensating tests have to be. It then holds the
// published schema to the loader's own vocabulary, because a schema that
// promises less than the loader enforces is a second contract, and two
// contracts that disagree are worse than none.
//
// It never writes, and it prints identifiers and codes and nothing else. A gate
// that echoed the justification of a waiver would copy into every CI log the
// text a waiver exists to keep in one reviewed file.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

// Exit statuses, as a vocabulary the tests can name.
const (
	exitOK        = 0
	exitViolation = 1
)

// waiversPath is the register, relative to the repository root.
const waiversPath = "quality/waivers.json"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, time.Now().UTC()))
}

// run is the whole command, with its status as a value and the day as an
// argument: a policy about expiry that read the clock itself would be a policy
// no test could hold still.
func run(args []string, stdout, stderr io.Writer, today time.Time) int {
	flags := flag.NewFlagSet("qualitywaivers", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root the register is judged against")
	if err := flags.Parse(args); err != nil {
		return exitViolation
	}

	records, violations := ReadRecords(*root)
	var register Waivers
	if len(violations) == 0 {
		register, violations = ReadWaivers(*root, waiversPath, records, today)
	}
	if len(violations) == 0 {
		violations = ReadSchema(*root)
	}
	if len(violations) == 0 {
		fmt.Fprintf(stdout, "qualitywaivers: %d waiver(s) of %s judged against %d rule(s) and %d recorded finding(s), with %s agreed\n",
			len(register.Waivers), waiversPath, len(records.Rules), len(records.Findings), SchemaPath)
		return exitOK
	}

	for _, violation := range violations {
		fmt.Fprintf(stderr, "qualitywaivers: %s\n", violation)
	}
	fmt.Fprintf(stderr, "qualitywaivers: %d violation(s) — an exception nobody can check is a rule silently suspended\n", len(violations))
	return exitViolation
}
