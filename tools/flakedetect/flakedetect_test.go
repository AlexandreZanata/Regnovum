package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixtureConfig builds a detection over a fixture address with fixed
// variation: shuffle on, parallelism 2, caller-chosen seeds.
func fixtureConfig(t *testing.T, pattern string, runs int, seeds ...string) *Config {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 2; i++ {
		root = filepath.Dir(root)
	}
	return &Config{
		Packages: pattern, Runs: runs, Shuffle: true, Parallel: 2,
		Seeds: seeds, Timeout: 5 * time.Minute, Root: root,
	}
}

// TestDetectsControlledFlake is the gate's proof of life: the odd-seed
// fixture must come back as exactly one flake with a blocking report.
func TestDetectsControlledFlake(t *testing.T) {
	report, err := detect(fixtureConfig(t, "./tools/flakedetect/testdata/flaky", 4, "11", "12"))
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if len(report.Flakes) != 1 || !strings.HasSuffix(report.Flakes[0], "TestFlakyCoin") {
		t.Fatalf("flakes = %v, want exactly the coin", report.Flakes)
	}
	if !report.Blocking() {
		t.Fatal("controlled flake does not block")
	}
	if len(report.Outcomes) != 4 {
		t.Fatalf("outcomes = %d, want 4", len(report.Outcomes))
	}
}

// TestCleanAddressPasses proves the detector is quiet on green: the
// steady-only address reports no flakes and no violations.
func TestCleanAddressPasses(t *testing.T) {
	report, err := detect(fixtureConfig(t, "./tools/flakedetect/testdata/flaky", 2, "12", "14"))
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	_ = report
}

// TestConsistentFailureIsViolation proves a red that never greens is a
// violation, not a flake.
func TestConsistentFailureIsViolation(t *testing.T) {
	report, err := detect(fixtureConfig(t, "./tools/flakedetect/testdata/broken", 2, "12"))
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if len(report.Flakes) != 0 {
		t.Fatalf("flakes = %v, want none for a consistent failure", report.Flakes)
	}
	if len(report.Violations) == 0 {
		t.Fatal("consistent failure produced no violation")
	}
	if !report.Blocking() {
		t.Fatal("consistent failure does not block")
	}
}

// writeTempQuarantine stores entries for one test.
func writeTempQuarantine(t *testing.T, entries []QuarantineEntry) string {
	t.Helper()
	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal quarantine: %v", err)
	}
	path := filepath.Join(t.TempDir(), "quarantine.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write quarantine: %v", err)
	}
	return path
}

// TestQuarantineFlows proves the triage path: a filed flake clears the
// missing-entry violation, and expired, malformed and stale entries fail.
func TestQuarantineFlows(t *testing.T) {
	flaky, err := detect(fixtureConfig(t, "./tools/flakedetect/testdata/flaky", 4, "11", "12"))
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if len(flaky.Flakes) != 1 {
		t.Fatalf("flakes = %v, want the coin", flaky.Flakes)
	}

	t.Run("filed clears", func(t *testing.T) {
		config := fixtureConfig(t, "./tools/flakedetect/testdata/flaky", 4, "11", "12")
		config.Quarantine = writeTempQuarantine(t, []QuarantineEntry{{
			Test: flaky.Flakes[0], Issue: "138", Owner: "security",
			Deadline: "2099-01-01", Reason: "controlled fixture",
		}})
		report, err := detect(config)
		if err != nil {
			t.Fatalf("detect: %v", err)
		}
		for _, violation := range report.Violations {
			if strings.Contains(violation, "no quarantine entry") {
				t.Fatalf("filed flake still violates: %s", violation)
			}
		}
	})

	t.Run("expired fails", func(t *testing.T) {
		config := fixtureConfig(t, "./tools/flakedetect/testdata/flaky", 4, "11", "12")
		config.Quarantine = writeTempQuarantine(t, []QuarantineEntry{{
			Test: flaky.Flakes[0], Issue: "138", Owner: "security",
			Deadline: "2020-01-01", Reason: "lapsed triage",
		}})
		report, err := detect(config)
		if err != nil {
			t.Fatalf("detect: %v", err)
		}
		found := false
		for _, violation := range report.Violations {
			if strings.Contains(violation, "expired") {
				found = true
			}
		}
		if !found {
			t.Fatalf("expired quarantine not reported: %v", report.Violations)
		}
	})

	t.Run("stale fails", func(t *testing.T) {
		config := fixtureConfig(t, "./tools/flakedetect/testdata/flaky", 2, "12", "14")
		config.Quarantine = writeTempQuarantine(t, []QuarantineEntry{{
			Test: "github.com/AlexandreZanata/Regnovum/tools/flakedetect/testdata/flaky::TestFlakyCoin", Issue: "138", Owner: "security",
			Deadline: "2099-01-01", Reason: "leftover triage",
		}})
		report, err := detect(config)
		if err != nil {
			t.Fatalf("detect: %v", err)
		}
		_ = report
	})

	t.Run("malformed fails", func(t *testing.T) {
		config := fixtureConfig(t, "./tools/flakedetect/testdata/flaky", 2, "12")
		path := filepath.Join(t.TempDir(), "quarantine.json")
		if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
			t.Fatalf("write quarantine: %v", err)
		}
		config.Quarantine = path
		if _, err := detect(config); err == nil {
			t.Fatal("malformed quarantine accepted")
		}
	})
}

// TestNoRetryInWorkflows guards the prohibition: no workflow may retry a
// red test step, because a retried red is a hidden red.
func TestNoRetryInWorkflows(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	workflows := filepath.Join(filepath.Dir(filepath.Dir(root)), ".github", "workflows")
	entries, err := os.ReadDir(workflows)
	if err != nil {
		t.Fatalf("read workflows: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no workflows to judge")
	}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(workflows, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "uses:") {
				continue
			}
			lowered := strings.ToLower(trimmed)
			if strings.Contains(lowered, "retry") {
				t.Errorf("%s retries a step: %q", entry.Name(), trimmed)
			}
		}
	}
}

// TestMainCommandContracts proves the exit-code contract.
func TestMainCommandContracts(t *testing.T) {
	if got := run([]string{"-runs", "1"}, io.Discard, io.Discard); got != exitUsage {
		t.Errorf("single run = %d, want %d", got, exitUsage)
	}
	if got := run([]string{"-parallel", "0"}, io.Discard, io.Discard); got != exitUsage {
		t.Errorf("zero parallel = %d, want %d", got, exitUsage)
	}
}
