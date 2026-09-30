package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// QuarantineEntry parks one known flake with its owner, issue and
// deadline: the test stays in the tree and in the catalog, keeps running,
// and blocks release until it is fixed or the deadline forces the
// conversation again. A quarantine is triage, never absolution.
type QuarantineEntry struct {
	Test     string `json:"test"`
	Issue    string `json:"issue"`
	Owner    string `json:"owner"`
	Deadline string `json:"deadline"`
	Reason   string `json:"reason"`
}

// judgeQuarantine enforces the register: flakes without an entry fail with
// the order to file one; expired entries fail; entries whose test stayed
// green across this run are stale and fail, because a quarantine that no
// longer describes a flake is wallpaper.
func judgeQuarantine(report *Report, config *Config) error {
	entries, err := loadQuarantine(config)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	byTest := map[string]QuarantineEntry{}
	for index, entry := range entries {
		if entry.Test == "" || entry.Issue == "" || entry.Owner == "" || entry.Reason == "" {
			return fmt.Errorf("quarantine entry %d needs test, issue, owner and reason", index)
		}
		deadline, err := time.Parse("2006-01-02", entry.Deadline)
		if err != nil {
			return fmt.Errorf("quarantine entry %d (%s) has no parseable deadline YYYY-MM-DD: %q", index, entry.Test, entry.Deadline)
		}
		if !deadline.After(now) {
			report.Violations = append(report.Violations, fmt.Sprintf("quarantine for %s expired on %s (owner %s, issue %s)", entry.Test, entry.Deadline, entry.Owner, entry.Issue))
		}
		byTest[entry.Test] = entry
	}
	for _, flake := range report.Flakes {
		if _, ok := byTest[flake]; !ok {
			report.Violations = append(report.Violations, fmt.Sprintf("flake %s has no quarantine entry: file issue, owner and deadline, or fix it", flake))
		}
	}
	failed := map[string]bool{}
	for _, outcome := range report.Outcomes {
		for _, failure := range outcome.Failures {
			failed[failure] = true
		}
	}
	for test := range byTest {
		if !failed[test] {
			report.Violations = append(report.Violations, fmt.Sprintf("quarantine for %s is stale: green across %d runs, remove it or re-file", test, config.Runs))
		}
	}
	return nil
}

// loadQuarantine reads the register. A missing file means no quarantines,
// which is the default: an empty register excuses nothing.
func loadQuarantine(config *Config) ([]QuarantineEntry, error) {
	raw, err := os.ReadFile(config.Quarantine)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read quarantine: %w", err)
	}
	var entries []QuarantineEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("decode quarantine: %w", err)
	}
	return entries, nil
}
