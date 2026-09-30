package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// MutantStatus is the gremlins verdict the judge understands. Anything
// else is a refusal: a status the gate cannot name is a measurement it
// cannot judge.
const (
	statusKilled     = "KILLED"
	statusLived      = "LIVED"
	statusTimedOut   = "TIMED OUT"
	statusNotCovered = "NOT COVERED"
	statusNotViable  = "NOT VIABLE"
	statusSkipped    = "SKIPPED"
)

// Mutant is one judged mutation: its kind, where it lives, and what the
// suite did with it.
type Mutant struct {
	Type   string
	File   string
	Line   int
	Status string
}

// PackageReport is the machine-readable outcome of one measured target.
type PackageReport struct {
	Package string
	Mutants []Mutant
	Killed  int
}

// ReadReport decodes one gremlins JSON report. Unknown statuses are kept
// and refused downstream, never dropped in silence.
func ReadReport(path, packageName string) (PackageReport, []string) {
	report := PackageReport{Package: packageName}
	raw, err := os.ReadFile(path)
	if err != nil {
		return report, []string{fmt.Sprintf("report-unreadable: %s: %v", path, err)}
	}
	var decoded struct {
		Files []struct {
			FileName  string `json:"file_name"`
			Mutations []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
				Line   int    `json:"line"`
				Column int    `json:"column"`
			} `json:"mutations"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return report, []string{fmt.Sprintf("report-malformed: %s: %v", path, err)}
	}
	for _, file := range decoded.Files {
		for _, mutation := range file.Mutations {
			switch mutation.Status {
			case statusKilled, statusLived, statusTimedOut, statusNotCovered, statusNotViable, statusSkipped:
			default:
				return report, []string{fmt.Sprintf("unknown-status: %s %s:%d reports %q", packageName, file.FileName, mutation.Line, mutation.Status)}
			}
			if mutation.Status == statusKilled {
				report.Killed++
			}
			report.Mutants = append(report.Mutants, Mutant{
				Type:   mutation.Type,
				File:   file.FileName,
				Line:   mutation.Line,
				Status: mutation.Status,
			})
		}
	}
	return report, nil
}
