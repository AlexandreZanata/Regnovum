package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// PackageCover runs `go test -coverprofile` for one package and returns the
// combined stdout. Tests inject a fake; the delivered runner below is the
// only one that executes the toolchain.
type packageRunner func(pkg, profile string) (string, error)

// gitRunner runs one git command and returns its stdout. Tests inject a
// fake; the delivered runner shells out to git.
type gitRunner func(args ...string) (string, error)

// runPackageCover executes the standard toolchain for one package. No
// third-party coverage tool is pinned because the signal is the Go
// coverprofile itself, read from the file the toolchain writes.
func runPackageCover(pkg, profile string) (string, error) {
	command := exec.Command("go", "test", "-count=1", "-coverprofile="+profile, "./"+pkg)
	output, err := command.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("coverage measurement of %s: %w", pkg, err)
	}
	return string(output), nil
}

// runGit shells out to git and returns its stdout.
func runGit(args ...string) (string, error) {
	command := exec.Command("git", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(output), nil
}

// coverBlock is one statement block of a coverprofile.
type coverBlock struct {
	file    string
	start   int
	end     int
	stmts   int
	covered bool
}

// packageCover is the measured outcome of one package: statement totals
// after allowlist subtraction, and the blocks for diff attribution.
type packageCover struct {
	total   int
	covered int
	blocks  []coverBlock
	percent float64
}

// modulePrefix is stripped from coverprofile file names to reach repo paths.
const modulePrefix = "github.com/AlexandreZanata/Regnovum/"

// parseProfile decodes one coverprofile file into blocks. Unknown lines are
// a refusal: a profile the gate cannot read is a measurement it cannot
// judge.
func parseProfile(path string) ([]coverBlock, []string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, []string{fmt.Sprintf("profile-unreadable: %s: %v", path, err)}
	}
	lines := strings.Split(string(raw), "\n")
	var blocks []coverBlock
	for i, line := range lines {
		if i == 0 {
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, []string{fmt.Sprintf("profile-malformed: %s line %d has %d fields, want 3", path, i+1, len(fields))}
		}
		name := fields[0]
		stmts, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, []string{fmt.Sprintf("profile-malformed: %s line %d stmts %q", path, i+1, fields[1])}
		}
		count, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, []string{fmt.Sprintf("profile-malformed: %s line %d count %q", path, i+1, fields[2])}
		}
		file, start, end, ok := splitBlockName(name)
		if !ok {
			return nil, []string{fmt.Sprintf("profile-malformed: %s line %d name %q", path, i+1, name)}
		}
		blocks = append(blocks, coverBlock{file: file, start: start, end: end, stmts: stmts, covered: count > 0})
	}
	return blocks, nil
}

// splitBlockName renders "path/file.go:start.col,end.col" into its parts.
func splitBlockName(name string) (string, int, int, bool) {
	colon := strings.LastIndex(name, ":")
	if colon < 0 {
		return "", 0, 0, false
	}
	file := strings.TrimPrefix(name[:colon], modulePrefix)
	rest := name[colon+1:]
	comma := strings.Index(rest, ",")
	if comma < 0 {
		return "", 0, 0, false
	}
	startPart := rest[:comma]
	endPart := rest[comma+1:]
	startLine := lineOf(startPart)
	endLine := lineOf(endPart)
	if startLine <= 0 || endLine <= 0 {
		return "", 0, 0, false
	}
	return file, startLine, endLine, true
}

// lineOf reads the line number before the dot of "line.col".
func lineOf(part string) int {
	dot := strings.Index(part, ".")
	if dot < 0 {
		return 0
	}
	line, err := strconv.Atoi(part[:dot])
	if err != nil {
		return 0
	}
	return line
}

// isAllowlisted reports whether a repo file falls under the register's
// generated allowlist (directory prefix or exact file). Unreachable entries
// name defensive lines that stay in the totals when covered; they only
// subtract when truly uncoverable, which this tree has none of.
func isAllowlisted(file string, register Register) bool {
	for _, entry := range register.Allowlist {
		if entry.Kind != "generated" {
			continue
		}
		if file == entry.Path || strings.HasPrefix(file, strings.TrimSuffix(entry.Path, "/")+"/") {
			return true
		}
	}
	return false
}

// summarize folds blocks into totals, skipping allowlisted files.
func summarize(blocks []coverBlock, register Register) packageCover {
	cover := packageCover{blocks: blocks}
	for _, block := range blocks {
		if isAllowlisted(block.file, register) {
			continue
		}
		cover.total += block.stmts
		if block.covered {
			cover.covered += block.stmts
		}
	}
	if cover.total > 0 {
		cover.percent = float64(cover.covered) * 100 / float64(cover.total)
	}
	return cover
}

// measure executes the toolchain once per target and decodes every
// profile. A target that yields no statements at all is a refusal: an
// empty measurement is the shape of a gate that stopped working.
func measure(register Register, runner packageRunner) (map[string]packageCover, []string) {
	covers := map[string]packageCover{}
	for _, target := range register.Targets {
		output, err := os.CreateTemp("", "coverage-*.out")
		if err != nil {
			return covers, []string{fmt.Sprintf("profile-unwritable: %v", err)}
		}
		profilePath := output.Name()
		output.Close()
		// The profile is removed after decoding: the register, not a
		// leftover file, is the versioned evidence.
		_, runErr := runner(target.Package, profilePath)
		blocks, parseViolations := parseProfile(profilePath)
		os.Remove(profilePath)
		if runErr != nil {
			return covers, []string{fmt.Sprintf("measurement-failed: %s: %v", target.Package, runErr)}
		}
		if len(parseViolations) > 0 {
			return covers, parseViolations
		}
		cover := summarize(blocks, register)
		if cover.total == 0 {
			return covers, []string{fmt.Sprintf("empty-measurement: %s produced no statements", target.Package)}
		}
		covers[target.Package] = cover
	}
	return covers, nil
}
