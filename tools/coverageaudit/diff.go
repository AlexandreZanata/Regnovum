package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// diffBase resolves the merge base the diff is judged against: the branch
// point with origin/main, or main when the remote is absent. It mirrors
// tools/diffaudit: a base that does not resolve is a refusal, never a
// silent empty diff.
func diffBase(git gitRunner) (string, []string) {
	for _, base := range []string{"origin/main", "main"} {
		output, err := git("merge-base", base, "HEAD")
		if err == nil && strings.TrimSpace(output) != "" {
			return strings.TrimSpace(output), nil
		}
	}
	return "", []string{"diff-base-unresolvable: neither origin/main nor main resolves to a merge base"}
}

// changedFiles lists non-test Go files changed between base and HEAD.
func changedFiles(git gitRunner, base string) ([]string, []string) {
	output, err := git("diff", "--name-only", base+"...HEAD")
	if err != nil {
		return nil, []string{fmt.Sprintf("diff-unreadable: %v", err)}
	}
	var files []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasSuffix(line, ".go") || strings.HasSuffix(line, "_test.go") {
			continue
		}
		files = append(files, line)
	}
	return files, nil
}

// addedLines returns the added statement-candidate line numbers of one file
// between base and HEAD, parsed from a zero-context unified diff.
func addedLines(git gitRunner, base, file string) (map[int]bool, []string) {
	output, err := git("diff", "-U0", base+"...HEAD", "--", file)
	if err != nil {
		return nil, []string{fmt.Sprintf("diff-unreadable: %s: %v", file, err)}
	}
	added := map[int]bool{}
	current := 0
	for _, line := range strings.Split(output, "\n") {
		if len(line) >= 2 && line[:2] == "@@" {
			current = parseHunkNewStart(line)
			continue
		}
		if len(line) == 0 {
			continue
		}
		switch line[0] {
		case '+':
			if len(line) >= 3 && line[:3] == "+++" {
				continue
			}
			if current > 0 {
				added[current] = true
			}
			current++
		case '-':
			if len(line) >= 3 && line[:3] == "---" {
				continue
			}
			// Removed lines do not advance the new-file counter.
		case ' ':
			// Zero-context diffs never emit context, but counting one
			// keeps the hunk arithmetic honest if they ever do.
			current++
		default:
			// "diff --git", "index" and any other header: not a line.
			continue
		}
	}
	return added, nil
}

// parseHunkNewStart reads the new-file start from a "@@ -a,b +c,d @@" header.
func parseHunkNewStart(header string) int {
	plus := strings.Index(header, "+")
	if plus < 0 {
		return 0
	}
	rest := header[plus+1:]
	end := strings.IndexAny(rest, " @")
	if end < 0 {
		return 0
	}
	segment := rest[:end]
	comma := strings.Index(segment, ",")
	if comma >= 0 {
		segment = segment[:comma]
	}
	start, err := strconv.Atoi(strings.TrimSpace(segment))
	if err != nil {
		return 0
	}
	return start
}

// lineCovered reports whether a repo file line is covered by any block of
// its package profile. Lines outside every block (blanks, comments, pure
// declarations) are not statements and do not count either way.
func lineCovered(file string, line int, blocks []coverBlock) (bool, bool) {
	for _, block := range blocks {
		if block.file != file {
			continue
		}
		if line < block.start || line > block.end {
			continue
		}
		return block.covered, true
	}
	return false, false
}

// blocksOf returns the blocks of the package owning a repo file.
func blocksOf(file string, covers map[string]packageCover) []coverBlock {
	dir := filepath.Dir(file)
	if covers[dir].blocks != nil {
		return covers[dir].blocks
	}
	return nil
}

// diffCoverage attributes every added statement line in scope to the
// coverprofiles: covered lines pass, uncovered statement lines fail, and
// non-statement lines do not count. Allowlisted files never count.
func diffCoverage(register Register, covers map[string]packageCover, git gitRunner) (int, int, []string) {
	base, violations := diffBase(git)
	if len(violations) > 0 {
		return 0, 0, violations
	}
	files, fileViolations := changedFiles(git, base)
	if len(fileViolations) > 0 {
		return 0, 0, fileViolations
	}
	covered := 0
	total := 0
	for _, file := range files {
		if isAllowlisted(file, register) {
			continue
		}
		if packageModule(filepath.Dir(file)) == "" {
			continue
		}
		added, lineViolations := addedLines(git, base, file)
		if len(lineViolations) > 0 {
			return 0, 0, lineViolations
		}
		blocks := blocksOf(file, covers)
		if blocks == nil {
			continue
		}
		for line := range added {
			isCovered, isStatement := lineCovered(file, line, blocks)
			if !isStatement {
				continue
			}
			total++
			if isCovered {
				covered++
			}
		}
	}
	return covered, total, nil
}
