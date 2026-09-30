// Command dast is the reproducible backend DAST scanner (P26-T10,
// docs/adr/ADR-018-dast-scanner.md). It drives the operations of an
// OpenAPI document against a disposable stack over loopback with the
// anonymous, user and admin profiles and its own hostile corpus, and it
// judges every answer by transport and protocol rules: no 5xx, no
// reflection, no auth bypass, no open redirect, security headers present
// and cookies scoped. Findings carry a severity; critical and high fail
// the run unless a valid waiver names them. The target must be loopback:
// a scanner that could be pointed at production is a weapon, not a gate.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

const (
	exitClean     = 0
	exitFindings  = 1
	exitUsage     = 2
	defaultTarget = ""
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the whole command, with its exit status as a value: the gate is a
// contract about exit codes, and a contract that lives inside main is a
// contract no test can hold.
func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("dast", flag.ContinueOnError)
	flags.SetOutput(stderr)
	target := flags.String("target", defaultTarget, "base URL of the disposable stack under test (loopback only)")
	openapi := flags.String("openapi", "api/openapi.json", "OpenAPI document driving the operations")
	userCookie := flags.String("user-cookie", "", "session cookie value of the user profile (arena_session)")
	adminCookie := flags.String("admin-cookie", "", "session cookie value of the admin profile (arena_session)")
	waivers := flags.String("waivers", "quality/dast-waivers.json", "waiver register (may be empty)")
	timeout := flags.Duration("timeout", 5*time.Minute, "overall scan budget")
	if err := flags.Parse(args); err != nil {
		return exitUsage
	}
	if *target == "" {
		fmt.Fprintln(stderr, "dast: -target is required (a loopback base URL, never production)")
		return exitUsage
	}
	config, err := loadConfig(*target, *openapi, *userCookie, *adminCookie, *waivers, *timeout)
	if err != nil {
		fmt.Fprintf(stderr, "dast: %v\n", err)
		return exitUsage
	}
	report, err := scan(config)
	if err != nil {
		fmt.Fprintf(stderr, "dast: scan: %v\n", err)
		return exitFindings
	}
	writeReport(stdout, report)
	if report.Blocking() {
		return exitFindings
	}
	return exitClean
}
