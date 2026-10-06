// Command frontendcoverage is the frontend route coverage gate
// (P48-T02).
//
// It judges the versioned inventory quality/frontend-routes.json
// against the normative sources under the root: api/openapi.json,
// the four staged fragments and every routes.go declaration. It
// reads and judges; it never writes.
//
// Usage:
//
//	go run ./tools/frontendcoverage -root . [-mode planning|complete]
//
// FRONTEND_COVERAGE_MODE=planning|complete selects the posture when
// -mode is absent; planning is the default. Planning accepts
// declared pending work without certifying it; complete demands
// zero browser gap and zero mandatory contract gap. Exit 0 holds
// every rule, 1 lists each violated rule, 2 is a usage error.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	exitOK    = 0
	exitAudit = 1
	exitUsage = 2
)

// modeEnv is the environment posture. The flag wins when both name
// a mode; the default stays planning, which records without
// certifying.
const modeEnv = "FRONTEND_COVERAGE_MODE"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("frontendcoverage", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root holding the contract and the inventory")
	mode := flags.String("mode", "", "coverage posture: planning|complete (default planning)")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "frontendcoverage takes no positional arguments")
		return exitUsage
	}
	posture := *mode
	if posture == "" {
		posture = os.Getenv(modeEnv)
	}
	if posture == "" {
		posture = ModePlanning
	}
	if posture != ModePlanning && posture != ModeComplete {
		fmt.Fprintln(stderr, "frontendcoverage mode is planning|complete")
		return exitUsage
	}
	inventory, findings := LoadInventory(filepath.Join(*root, "quality", "frontend-routes.json"))
	if len(findings) != 0 {
		for _, finding := range findings {
			fmt.Fprintln(stdout, finding)
		}
		return exitAudit
	}
	sources, err := LoadSources(*root)
	if err != nil {
		fmt.Fprintln(stderr, "frontendcoverage cannot read the normative sources: "+err.Error())
		return exitAudit
	}
	if findings := Judge(*root, inventory, sources, posture); len(findings) != 0 {
		for _, finding := range findings {
			fmt.Fprintln(stdout, finding)
		}
		return exitAudit
	}
	fmt.Fprintln(stdout, "frontendcoverage: coverage holds (mode="+posture+")")
	return exitOK
}
