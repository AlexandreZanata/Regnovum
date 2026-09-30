// Command mutationaudit is the gate of P24-T10: mutation testing of the
// critical rules.
//
// It reads the versioned policy in quality/mutations.json and holds the
// tree to it: the pinned tool answers its own version, the operators are
// exactly the registered set, every domain/application package of the
// covered modules is measured or explicitly deferred, every measured
// target is executed with the pinned flags, and the report must clear the
// risk thresholds with zero unlisted survivors. A lived mutant without a
// manifest entry is a refusal; a manifest entry without a matching live
// mutant is a refusal (the code moved and the proof lapsed); a timed-out
// mutant without a hangs entry is a refusal, because a low timeout buries
// survivors as timeouts. It never writes and never trusts an old number:
// the scores are measured on every execution.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
)

// Exit statuses, as a vocabulary the tests can name.
const (
	exitOK        = 0
	exitViolation = 1
)

// RegisterPath is where the mutation policy lives, relative to the root.
const RegisterPath = "quality/mutations.json"

// CatalogPath is where the rule catalog lives, relative to the root.
const CatalogPath = "quality/catalog.json"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("mutationaudit", flag.ContinueOnError)
	root := flags.String("root", ".", "repository root the register is judged against")
	registerPath := flags.String("register", RegisterPath, "mutation register to judge, relative to the root")
	toolModule := flags.String("tool-module", "", "the module path of the pinned mutation tool")
	toolVersion := flags.String("tool-version", "", "the pinned mutation tool version")
	timeoutCoefficient := flags.Int("timeout-coefficient", 0, "per-mutant timeout coefficient passed to the tool")
	workers := flags.Int("workers", 0, "parallel workers passed to the tool")
	if err := flags.Parse(args); err != nil {
		return exitViolation
	}
	pins := toolPins{
		module:             *toolModule,
		version:            *toolVersion,
		timeoutCoefficient: *timeoutCoefficient,
		workers:            *workers,
	}
	if pins.module == "" || pins.version == "" || pins.timeoutCoefficient <= 0 || pins.workers <= 0 {
		fmt.Fprintf(os.Stderr, "mutationaudit: the tool pins are required: -tool-module, -tool-version, -timeout-coefficient and -workers hold the machine to the register, and an unpinned gate measures nothing\n")
		return exitViolation
	}
	if err := os.Chdir(*root); err != nil {
		fmt.Fprintf(os.Stderr, "mutationaudit: enter %s: %v\n", *root, err)
		return exitViolation
	}
	violations := audit(pins, *registerPath, runTool)
	for _, violation := range violations {
		fmt.Fprintf(os.Stderr, "mutationaudit: %s\n", violation)
	}
	if len(violations) > 0 {
		fmt.Fprintf(os.Stderr, "mutationaudit: %d violation(s) — a survivor without a proven entry is a test the suite does not keep\n", len(violations))
		return exitViolation
	}
	fmt.Printf("mutationaudit: scores hold and every survivor is listed\n")
	return exitOK
}

// runTool executes one pinned `go run` invocation. The tool resolves and
// builds exactly the pinned version through the module proxy, or it
// refuses to run at all: there is no installed binary to drift.
func runTool(args []string) error {
	command := exec.Command("go", args...)
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
	return command.Run()
}
