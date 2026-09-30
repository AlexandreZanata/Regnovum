// Command coverageaudit is the gate of P24-T11: risk-based line-coverage
// floors with a diff ratchet.
//
// It reads the versioned policy in quality/coverage-floors.json and holds
// the tree to it: every domain/application package of the covered modules
// is listed with a floor it may only rise above, the risk, global and diff
// thresholds are the ones the phase names (Q0 95, Q1 90, Q2 80, global 85,
// diff 95), debt below a risk floor is explicit with a reason and a P45
// gate, and generated outputs use the versioned allowlist. It measures with
// the standard Go coverprofile (no third-party tool to pin), judges the
// diff from git (added statement lines must be covered), and reports the
// mutation thresholds beside the coverage numbers to prove the two gates
// are independent signals. It never writes.
package main

import (
	"flag"
	"fmt"
	"os"
)

// Exit statuses, as a vocabulary the tests can name.
const (
	exitOK        = 0
	exitViolation = 1
)

// RegisterPath is where the coverage policy lives, relative to the root.
const RegisterPath = "quality/coverage-floors.json"

// CatalogPath is where the rule catalog lives, relative to the root.
const CatalogPath = "quality/catalog.json"

// MutationsPath is where the mutation policy lives, relative to the root.
// The gate reads its thresholds only to print them beside the coverage
// numbers: proving the two gates answer different questions with different
// signals (executed lines versus killed faults).
const MutationsPath = "quality/mutations.json"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("coverageaudit", flag.ContinueOnError)
	root := flags.String("root", ".", "repository root the register is judged against")
	registerPath := flags.String("register", RegisterPath, "coverage register to judge, relative to the root")
	if err := flags.Parse(args); err != nil {
		return exitViolation
	}
	if err := os.Chdir(*root); err != nil {
		fmt.Fprintf(os.Stderr, "coverageaudit: enter %s: %v\n", *root, err)
		return exitViolation
	}
	violations := audit(*registerPath, runPackageCover, runGit)
	for _, violation := range violations {
		fmt.Fprintf(os.Stderr, "coverageaudit: %s\n", violation)
	}
	if len(violations) > 0 {
		fmt.Fprintf(os.Stderr, "coverageaudit: %d violation(s) — a lowered floor, a missing package or an uncovered new line is a test the suite does not keep\n", len(violations))
		return exitViolation
	}
	mutation := mutationThresholds(".")
	fmt.Printf("coverageaudit: floors hold and the diff is covered (mutation Q0>=%.0f Q1>=%.0f is a separate gate: covered lines host lived mutants, see quality/mutations.json)\n", mutation["Q0"], mutation["Q1"])
	return exitOK
}
