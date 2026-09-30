// Command regressionpack is the gate of the fast regression pack (P27-T10).
//
//	regressionpack [-root .] [-catalog quality/catalog.json]
//	  [-pack quality/regression-pack.json] [-schema quality/regression-pack.schema.json]
//	regressionpack [-root .] -exec
//
// Without -exec it judges the pack without running anything: schema, scope,
// budget, coverage of every Q0/Q1 catalog rule, test existence in fast
// packages, no skip or test-retry in the bodies, and every historical defect
// pinned to a test. With -exec it additionally runs the pack, one go test
// per package, measuring the documented budget. One run is one attempt per
// package: a failure stops the run instead of repeating it, because a retry
// is how a green run would be bought.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
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
	flags := flag.NewFlagSet("regressionpack", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root the pack is judged against")
	catalog := flags.String("catalog", CatalogPath, "rule catalog, relative to the root")
	pack := flags.String("pack", PackPath, "fast pack, relative to the root")
	schema := flags.String("schema", SchemaPath, "pack schema, relative to the root")
	modules := flags.String("modules", strings.Join(RequiredModules, ","), "comma-separated business modules coverage must show")
	execute := flags.Bool("exec", false, "run the fast pack after judging it")
	if err := flags.Parse(args); err != nil {
		return exitViolation
	}
	findings := Validate(*root, *catalog, *pack, *schema, strings.Split(*modules, ","))
	if len(findings) != 0 {
		for _, finding := range findings {
			fmt.Fprintf(stderr, "regressionpack: %s — %s\n", finding.Rule, finding.Detail)
		}
		return exitViolation
	}
	parsed, err := ReadPack(*root, *pack)
	if err != nil {
		fmt.Fprintf(stderr, "regressionpack: %v\n", err)
		return exitViolation
	}
	planned := PlanPack(parsed)
	tests := 0
	for _, run := range planned {
		tests += len(run.Tests)
	}
	fmt.Fprintf(stdout, "regressionpack: %d rules, %d tests in %d packages, budget %ds (fast pack: not a substitute for nightly/release)\n",
		len(parsed.Rules), tests, len(planned), parsed.BudgetSeconds)
	if !*execute {
		return exitOK
	}
	return runPack(*root, planned, parsed.BudgetSeconds, stdout, stderr)
}

// runPack executes one go test per package, once each, and judges the total
// against the budget. It never repeats a package: the first failure is the
// verdict.
func runPack(root string, planned []PackageRun, budget int, stdout, stderr io.Writer) int {
	started := time.Now()
	for _, run := range planned {
		expression := "^(" + strings.Join(run.Tests, "|") + ")$"
		command := exec.Command("go", "test", "-count=1", "-timeout", fmt.Sprintf("%ds", budget), "-run", expression, "./"+run.Dir)
		command.Dir = root
		output, err := command.CombinedOutput()
		fmt.Fprintf(stdout, "regressionpack: %s: %d tests\n%s", run.Dir, len(run.Tests), output)
		if err != nil {
			fmt.Fprintf(stderr, "regressionpack: %s failed (no retry: the failure is the verdict)\n", run.Dir)
			return exitViolation
		}
	}
	elapsed := time.Since(started)
	fmt.Fprintf(stdout, "regressionpack: pack ran in %s against a %ds budget\n", elapsed.Round(time.Second), budget)
	if elapsed > time.Duration(budget)*time.Second {
		fmt.Fprintf(stderr, "regressionpack: pack exceeded its budget\n")
		return exitViolation
	}
	return exitOK
}
