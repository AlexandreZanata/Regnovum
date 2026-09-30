package main

import (
	"fmt"
	"io"
	"sort"
)

// writeReport renders the detection: per-run outcomes, the flakes with
// their signatures, and every violation. The output is stable (sorted) so
// two runs of the same tree diff cleanly.
func writeReport(stdout io.Writer, report *Report) {
	for _, outcome := range report.Outcomes {
		verdict := "pass"
		if !outcome.Passed {
			verdict = "FAIL"
		}
		fmt.Fprintf(stdout, "run %d/%d seed=%s shuffle=%s parallel=%d: %s",
			outcome.Index, len(report.Outcomes), outcome.Seed, outcome.Shuffle, outcome.Parallel, verdict)
		if len(outcome.Failures) > 0 {
			sorted := append([]string(nil), outcome.Failures...)
			sort.Strings(sorted)
			fmt.Fprintf(stdout, " %s (signature %s)", sorted, outcome.Signature)
		}
		fmt.Fprintln(stdout)
	}
	flakes := append([]string(nil), report.Flakes...)
	sort.Strings(flakes)
	for _, flake := range flakes {
		fmt.Fprintf(stdout, "flake: %s\n", flake)
	}
	violations := append([]string(nil), report.Violations...)
	sort.Strings(violations)
	for _, violation := range violations {
		fmt.Fprintf(stdout, "violation: %s\n", violation)
	}
	fmt.Fprintf(stdout, "flakedetect: %d runs, %d flakes, %d violations\n", len(report.Outcomes), len(flakes), len(violations))
	if report.Blocking() {
		fmt.Fprintln(stdout, "flakedetect: BLOCKED by flakes or violations")
		return
	}
	fmt.Fprintln(stdout, "flakedetect: ok — every run agreed")
}
