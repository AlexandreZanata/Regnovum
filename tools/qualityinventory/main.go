// Command qualityinventory is the semantic coverage inventory (P21-T06).
//
// It crosses three documents — the rule catalog, the evidence register and the
// traceability matrix — with the six artifact families the phase names: the
// routes the contract serves, the migrations the schema carries, the use cases
// the modules hold, the workload types the job domain declares, the subcommands
// the binary offers, and the test references the documents cite. What it
// produces is one report in two formats: `quality/coverage.json` for machines
// and `docs/quality/COVERAGE.md` for people.
//
// Two decisions are worth stating, because both are refusals:
//
//   - Coverage is counted per **rule**, never per line. A rule is covered when
//     an anchor it declares resolves — a test that proves it, a route, use case
//     or migration that carries it, an evidence identity that declares it. No
//     amount of file execution makes a rule covered, and the tool reads no
//     coverage profile at all.
//   - The report is **versioned**, and the command fails when the committed
//     report diverges from the tree. Coverage cannot change in silence: the day
//     an evidence is removed, the diff is the record of the decision, and the
//     gate is what forces that record to exist.
//
// Without -write it judges and refuses; with -write it regenerates the two
// files. It never runs a test.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Exit statuses, as a vocabulary the tests can name.
const (
	exitOK        = 0
	exitViolation = 1
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the whole command, with its status as a value: the contract of a gate
// is about exit statuses, and a contract that only lives inside main is one no
// test can hold.
func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("qualityinventory", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root the inventory is generated from")
	write := flags.Bool("write", false, "regenerate the report and the document instead of judging the committed ones")
	reportPath := flags.String("report", ReportPath, "path of the machine report, relative to the root")
	docPath := flags.String("doc", DocPath, "path of the rendered document, relative to the root")
	if err := flags.Parse(args); err != nil {
		return exitViolation
	}

	records, violations := ReadRecords(*root)
	if len(violations) == 0 {
		var report Report
		report, violations = Join(records)
		if len(violations) == 0 {
			violations = append(violations, Verify(report)...)
		}
		if len(violations) == 0 {
			if *write {
				violations = append(violations, writeReport(*root, *reportPath, *docPath, report)...)
				if len(violations) == 0 {
					fmt.Fprintf(stdout, "qualityinventory: wrote %s and %s — %s\n", *reportPath, *docPath, tally(report))
				}
			} else {
				violations = append(violations, checkCommitted(*root, *reportPath, *docPath, report)...)
				if len(violations) == 0 {
					fmt.Fprintf(stdout, "qualityinventory: %s, and the committed report is the one this tree generates\n", tally(report))
				}
			}
		}
	}

	if len(violations) == 0 {
		return exitOK
	}
	for _, violation := range violations {
		fmt.Fprintf(stderr, "qualityinventory: %s\n", violation)
	}
	fmt.Fprintf(stderr, "qualityinventory: %d violation(s) — a rule nothing proves is a rule nobody applies, and a citation nobody can resolve is a promise the tree stopped keeping\n", len(violations))
	return exitViolation
}

// tally is the one line the command prints when it is happy: what it read and
// what it found, so that the CI log says what was judged.
func tally(report Report) string {
	return fmt.Sprintf("%d rule(s) in %d of %d famil(ies) and %d citation(s): %d covered, %d absent, %d obsolete, %d orphan(s)",
		report.Catalog.Rules, tracedFamilies(report), len(report.Families), report.Summary.Citations,
		report.Summary.Covered, report.Summary.Absent, report.Summary.Obsolete, report.Summary.Orphans)
}

// tracedFamilies counts the families that have artifacts, so that a family the
// readers stopped being able to read is visible in the log rather than absent
// from it.
func tracedFamilies(report Report) int {
	count := 0
	for _, family := range report.Families {
		if family.Artifacts > 0 {
			count++
		}
	}
	return count
}

// writeReport regenerates both files. It writes the report first: the document
// is rendered from the same value, and a half-written pair is worse than
// neither — the next run reports the drift.
func writeReport(root, reportPath, docPath string, report Report) Violations {
	encoded, err := Render(report)
	if err != nil {
		return Violations{{Row: "report", Code: codeReportMissing, Detail: err.Error()}}
	}
	var violations Violations
	for _, file := range []struct {
		path    string
		content []byte
	}{
		{reportPath, encoded},
		{docPath, RenderMarkdown(report)},
	} {
		full := filepath.Join(root, filepath.FromSlash(file.path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			violations = append(violations, Violation{Row: file.path, Code: codeReportMissing, Detail: fmt.Sprintf("`%s` is not writable: %v", file.path, err)})
			continue
		}
		if err := os.WriteFile(full, file.content, 0o644); err != nil {
			violations = append(violations, Violation{Row: file.path, Code: codeReportMissing, Detail: fmt.Sprintf("`%s` is not writable: %v", file.path, err)})
		}
	}
	return violations
}

// checkCommitted judges the report that is in the checkout against the one the
// tree generates. The comparison is a byte comparison, because both files are
// generated: anything that differs is either a change nobody regenerated or an
// edit somebody made by hand, and neither is allowed to pass quietly.
func checkCommitted(root, reportPath, docPath string, report Report) Violations {
	generated, err := Render(report)
	if err != nil {
		return Violations{{Row: "report", Code: codeReportMissing, Detail: err.Error()}}
	}
	var violations Violations
	committed := map[string][]byte{}
	for _, file := range []struct {
		path string
		want []byte
	}{
		{reportPath, generated},
		{docPath, RenderMarkdown(report)},
	} {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file.path)))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				violations = append(violations, Violation{
					Row:    file.path,
					Code:   codeReportMissing,
					Detail: fmt.Sprintf("`%s` is not in the checkout: run `make quality-inventory-write` and commit the inventory", file.path),
				})
				continue
			}
			violations = append(violations, Violation{Row: file.path, Code: codeReportMissing, Detail: fmt.Sprintf("`%s` is unreadable: %v", file.path, err)})
			continue
		}
		committed[file.path] = raw
		if diff := firstDifference(raw, file.want); diff != "" {
			violations = append(violations, Violation{
				Row:    file.path,
				Code:   codeReportDrift,
				Detail: fmt.Sprintf("the committed file is not the one this tree generates (%s); run `make quality-inventory-write` and commit the inventory", diff),
			})
		}
	}
	if raw, ok := committed[reportPath]; ok {
		parsed, err := Parse(raw)
		if err != nil {
			violations = append(violations, Violation{Row: reportPath, Code: codeReportInconsistent, Detail: err.Error()})
		} else {
			violations = append(violations, Verify(parsed)...)
		}
	}
	return violations
}

// firstDifference names the first line that differs, so that the failure says
// where to look instead of only that something changed.
func firstDifference(committed, generated []byte) string {
	left := strings.Split(string(committed), "\n")
	right := strings.Split(string(generated), "\n")
	for index := 0; index < len(left) || index < len(right); index++ {
		var one, other string
		if index < len(left) {
			one = left[index]
		}
		if index < len(right) {
			other = right[index]
		}
		if one != other {
			return fmt.Sprintf("line %d", index+1)
		}
	}
	return ""
}
