package main

import (
	"fmt"
	"io"
	"sort"
)

// writeReport renders the scan: every finding with its severity, rule,
// address and detail, waived findings marked, and a summary last. The
// output is stable (sorted) so two runs of the same tree diff cleanly.
func writeReport(stdout io.Writer, report *Report) {
	findings := append([]Finding(nil), report.Findings...)
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Severity != findings[j].Severity {
			return findings[i].Severity < findings[j].Severity
		}
		if findings[i].Rule != findings[j].Rule {
			return findings[i].Rule < findings[j].Rule
		}
		if findings[i].Path != findings[j].Path {
			return findings[i].Path < findings[j].Path
		}
		return findings[i].Detail < findings[j].Detail
	})
	counts := map[Severity]int{}
	waived := 0
	for _, finding := range findings {
		if finding.Waived {
			waived++
			continue
		}
		counts[finding.Severity]++
		fmt.Fprintf(stdout, "%s %s %s %s: %s\n", finding.Severity, finding.Rule, finding.Method, finding.Path, finding.Detail)
	}
	for _, finding := range findings {
		if finding.Waived {
			fmt.Fprintf(stdout, "waived %s %s %s: %s\n", finding.Rule, finding.Method, finding.Path, finding.Detail)
		}
	}
	fmt.Fprintf(stdout, "dast: target %s: %d critical, %d high, %d medium, %d waived\n",
		report.Target, counts[SeverityCritical], counts[SeverityHigh], counts[SeverityMedium], waived)
	if report.Blocking() {
		fmt.Fprintln(stdout, "dast: BLOCKED by unwaived critical/high findings")
		return
	}
	fmt.Fprintln(stdout, "dast: ok — no blocking findings")
}
