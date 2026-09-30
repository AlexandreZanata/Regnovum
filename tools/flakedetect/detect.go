package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Config is a validated detection run: what to execute, how to vary it,
// where the quarantine lives and how long to spend.
type Config struct {
	Packages   string
	Tests      string
	Runs       int
	Shuffle    bool
	Parallel   int
	Seeds      []string
	Quarantine string
	Timeout    time.Duration
	Root       string
}

// Outcome is one execution: its parameters, pass/fail and the signature
// of the failure when it failed.
type Outcome struct {
	Index     int
	Seed      string
	Shuffle   string
	Parallel  int
	Passed    bool
	Failures  []string
	Signature string
}

// Report is the whole detection: every outcome, the flakes derived from
// them, and the quarantine verdict.
type Report struct {
	Outcomes   []Outcome
	Flakes     []string
	Violations []string
}

// Blocking reports whether the run fails the gate: flakes, consistent
// failures and quarantine violations all block, because a red that is
// only sometimes red is still red.
func (report *Report) Blocking() bool {
	return len(report.Flakes) > 0 || len(report.Violations) > 0
}

// Options carries the run parameters from the flag layer to validation.
type Options struct {
	Packages   string
	Tests      string
	Runs       int
	Shuffle    bool
	Parallel   int
	Seeds      string
	Quarantine string
	Timeout    time.Duration
}

// loadConfig validates the run parameters.
func loadConfig(options Options) (*Config, error) {
	packages, tests, runs, shuffle, parallel := options.Packages, options.Tests, options.Runs, options.Shuffle, options.Parallel
	seeds, quarantine, timeout := options.Seeds, options.Quarantine, options.Timeout
	if strings.TrimSpace(packages) == "" {
		return nil, fmt.Errorf("package pattern is required")
	}
	if parallel < 1 || parallel > 64 {
		return nil, fmt.Errorf("parallel %d is outside 1..64", parallel)
	}
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	var parsed []string
	if seeds != "" {
		for _, seed := range strings.Split(seeds, ",") {
			trimmed := strings.TrimSpace(seed)
			if trimmed == "" {
				continue
			}
			if _, err := strconv.ParseInt(trimmed, 10, 64); err != nil {
				return nil, fmt.Errorf("seed %q is not an integer", trimmed)
			}
			parsed = append(parsed, trimmed)
		}
		if len(parsed) == 0 {
			return nil, fmt.Errorf("seeds %q names no seed", seeds)
		}
	}
	root, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("working directory: %w", err)
	}
	return &Config{
		Packages: packages, Tests: tests, Runs: runs, Shuffle: shuffle,
		Parallel: parallel, Seeds: parsed, Quarantine: quarantine,
		Timeout: timeout, Root: root,
	}, nil
}

// detect executes the runs and judges outcomes against quarantine.
func detect(config *Config) (*Report, error) {
	deadline := time.Now().Add(config.Timeout)
	report := &Report{}
	failures := map[string]int{}
	signatures := map[string]map[string]bool{}
	for index := 0; index < config.Runs; index++ {
		if time.Now().After(deadline) {
			return report, fmt.Errorf("detection budget exhausted at run %d of %d", index+1, config.Runs)
		}
		outcome, err := execute(config, index)
		if err != nil {
			return report, err
		}
		report.Outcomes = append(report.Outcomes, outcome)
		for _, failure := range outcome.Failures {
			failures[failure]++
			if signatures[failure] == nil {
				signatures[failure] = map[string]bool{}
			}
			signatures[failure][outcome.Signature] = true
		}
	}
	for test, count := range failures {
		switch {
		case count < config.Runs:
			report.Flakes = append(report.Flakes, test)
		default:
			report.Violations = append(report.Violations, fmt.Sprintf("%s fails every run (%d/%d): consistent failure, not a flake", test, count, config.Runs))
		}
	}
	_ = signatures
	if err := judgeQuarantine(report, config); err != nil {
		return report, err
	}
	return report, nil
}

// testEvent is the subset of `go test -json` this runner reads.
type testEvent struct {
	Time    time.Time `json:"Time"`
	Action  string    `json:"Action"`
	Package string    `json:"Package"`
	Test    string    `json:"Test"`
	Output  string    `json:"Output"`
}

// execute runs `go test -json` once with this run's variation and parses
// the failing test names plus a signature over the failure output.
func execute(config *Config, index int) (Outcome, error) {
	args := []string{"test", "-json", "-count=1", fmt.Sprintf("-parallel=%d", config.Parallel)}
	if config.Tests != "" {
		args = append(args, "-run", config.Tests)
	}
	if config.Shuffle {
		args = append(args, "-shuffle=on")
	}
	args = append(args, config.Packages)
	command := exec.Command("go", args...)
	command.Dir = config.Root
	command.Env = append(os.Environ(), "GOFLAGS=-mod=readonly")
	seed := ""
	if len(config.Seeds) > 0 {
		seed = config.Seeds[index%len(config.Seeds)]
		command.Env = append(command.Env, "ARENA_TEST_SEED="+seed)
	}
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	runErr := command.Run()

	outcome := Outcome{Index: index + 1, Seed: seed, Parallel: config.Parallel}
	if config.Shuffle {
		outcome.Shuffle = "on"
	} else {
		outcome.Shuffle = "off"
	}
	failures := map[string]bool{}
	var failureOutput strings.Builder
	scanner := bufio.NewScanner(&output)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		var event testEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if event.Action == "fail" && event.Test != "" {
			failures[event.Package+"::"+event.Test] = true
			failureOutput.WriteString(event.Output)
		}
		if event.Action == "fail" && event.Test == "" {
			failureOutput.WriteString(event.Output)
		}
	}
	for failure := range failures {
		outcome.Failures = append(outcome.Failures, failure)
	}
	if len(outcome.Failures) == 0 && runErr == nil {
		outcome.Passed = true
	}
	if len(outcome.Failures) > 0 {
		sum := sha256.Sum256([]byte(failureOutput.String()))
		outcome.Signature = hex.EncodeToString(sum[:16])
	}
	return outcome, nil
}
