package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/AlexandreZanata/Regnovum/tools/auditkit"
)

// censusExtensions are the text files the census reads. Markdown is out on
// purpose: the documents of this repository quote the marker to explain the
// convention, and a census that read prose would refuse the documents that
// describe it — the same decision the dead-code gate records for the same text.
var censusExtensions = []string{".go", ".ts", ".js", ".css", ".json", ".yaml", ".yml", ".html", ".sql"}

// disclosedLine is the disclosure of a generated artifact: the line the
// generators of this repository write, with the comment punctuation the language
// of the file uses. The `Code generated` shape is the Go convention and the two
// TypeScript artifacts follow it, so one vocabulary reads both.
var disclosedLine = regexp.MustCompile(`^\s*(?://|/\*|\*|#|<!--)?\s*Code generated .* DO NOT EDIT\.?\s*(?:\*/|-->)?\s*$`)

// censusHeadlines is how many lines of a text file the census reads: the
// disclosure opens the file, and a marker further down is not one.
const censusHeadlines = 3

// disclosure answers whether a file announces that it is generated, and where.
func disclosure(root, relative string) (int, bool, error) {
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		return 0, false, fmt.Errorf("read %s: %w", relative, err)
	}
	if strings.HasSuffix(relative, ".go") {
		return goDisclosure(relative, body)
	}
	for number, line := range strings.Split(string(body), "\n") {
		if number >= censusHeadlines {
			break
		}
		if disclosedLine.MatchString(line) {
			return number + 1, true, nil
		}
	}
	return 0, false, nil
}

// goDisclosure reads the marker the way the Go toolchain does — in the comments
// before the package clause — and the question itself is the shared one, so that
// this gate and the other gates of the phase exclude exactly the same files. The
// line is read here because a finding has to name where it read it.
func goDisclosure(relative string, body []byte) (int, bool, error) {
	positions := token.NewFileSet()
	parsed, err := parser.ParseFile(positions, relative, body, parser.ParseComments)
	if err != nil {
		return 0, false, fmt.Errorf("parse %s: %w", relative, err)
	}
	if !auditkit.IsGenerated(parsed) {
		return 0, false, nil
	}
	for _, group := range parsed.Comments {
		if group.Pos() >= parsed.Package {
			break
		}
		for _, comment := range group.List {
			if disclosedLine.MatchString(comment.Text) {
				return positions.Position(comment.Slash).Line, true, nil
			}
		}
	}
	return 0, true, nil
}

// census lists the artifacts of the tree that disclose they are generated. The
// declared build outputs are left out: they are products of a build, they never
// belong to the repository, and the register requires `.gitignore` to hold them.
func census(root string, buildOutputs []string) ([]string, error) {
	files, err := auditkit.Files(root, auditkit.SkippedDirectories, func(name string) bool {
		return matchesAny(censusExtensions, strings.ToLower(filepath.Ext(name)))
	})
	if err != nil {
		return nil, err
	}
	disclosed := []string{}
	for _, path := range files {
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return nil, fmt.Errorf("relate %s: %w", path, err)
		}
		relative = filepath.ToSlash(relative)
		if underAny(relative, buildOutputs) {
			continue
		}
		_, found, err := disclosure(root, relative)
		if err != nil {
			return nil, err
		}
		if found {
			disclosed = append(disclosed, relative)
		}
	}
	return disclosed, nil
}

// underAny reports whether a path is one of the declared build outputs or lives
// inside one of them.
func underAny(relative string, roots []string) bool {
	for _, root := range roots {
		root = strings.Trim(filepath.ToSlash(root), "/")
		if relative == root || strings.HasPrefix(relative, root+"/") {
			return true
		}
	}
	return false
}

// ignoredByGit reads the root `.gitignore` and answers whether it holds a path.
// Only the root file is read, and the answer is deliberately conservative: a
// pattern that does not match the path is not an answer, and the register is the
// place that names the products, not the ignore file.
func ignoredByGit(root, relative string) (bool, error) {
	body, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return false, fmt.Errorf("read .gitignore: %w", err)
	}
	target := strings.Trim(filepath.ToSlash(relative), "/")
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		pattern := strings.Trim(strings.TrimPrefix(line, "/"), "/")
		if target == pattern || strings.HasPrefix(target, pattern+"/") {
			return true, nil
		}
	}
	return false, nil
}
