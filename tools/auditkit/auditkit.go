// Package auditkit is the scaffolding the quality gates of phase 23 share: the
// vocabulary a finding is named with, and the walk that decides which files a gate
// judges.
//
// It exists because the gate of P23-T03 measured the same walk and the same
// finding type copied into `tools/deadcodeaudit` and `tools/erroraudit`, and a
// phase whose exit criterion is refusing duplicated code cannot keep its own
// duplication. What is shared is only what is genuinely the same question — where
// a gate looks and how it names what it found; the rules, the measurement and the
// report stay in each gate, because those are the parts that differ and the parts
// a reader opens the gate to read.
package auditkit

import (
	"fmt"
	"go/ast"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Rule is one rule of a gate: the name its findings carry and the sentence that
// says why breaking it costs something.
type Rule struct {
	Name   string
	Reason string
}

// RuleKnown reports whether a name belongs to the declared vocabulary. A gate
// that produces a finding outside its own table has a bug, and the answer to a
// bug is a refusal rather than a line in the report.
func RuleKnown(rules []Rule, name string) bool {
	for _, entry := range rules {
		if entry.Name == name {
			return true
		}
	}
	return false
}

// Finding is one refusal: where it is, which rule it breaks and what the gate
// read to decide. The detail is part of the finding on purpose — a refusal that
// does not show what it refused is a refusal nobody can argue with.
type Finding struct {
	Rule   string
	Path   string
	Line   int
	Detail string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: %s: %s", f.Path, f.Line, f.Rule, f.Detail)
}

// SkippedDirectories are the directories no gate enters. Generated code is
// excluded by provenance — the marker inside the file — and never by where it
// lives: the sqlc output of this repository owns rows, transactions and contexts,
// and a gate that judged it would bury the functions it exists to find.
var SkippedDirectories = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "testdata": true, ".local": true, "dist": true,
}

var generatedLine = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// IsGenerated reads the generated marker the way the Go toolchain does: in the
// comments before the package clause. A file that merely mentions the text — the
// generators of this repository do, inside a string — is code and is judged.
func IsGenerated(file *ast.File) bool {
	for _, group := range file.Comments {
		if group.Pos() >= file.Package {
			break
		}
		for _, comment := range group.List {
			if generatedLine.MatchString(strings.TrimRight(comment.Text, " \t")) {
				return true
			}
		}
	}
	return false
}

// Files lists every file under a root that a caller accepts, refusing to enter
// the directories it was given. The list is ordered, because two runs of a gate
// that answers differently are two gates. A nil skip list is the walk of a
// fixture, where nothing is held back.
func Files(root string, skipped map[string]bool, accept func(name string) bool) ([]string, error) {
	files := []string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && skipped[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if accept(entry.Name()) {
			files = append(files, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	sort.Strings(files)
	return files, nil
}

// GoFiles lists every Go file under a root, refusing to enter the directories a
// caller names.
func GoFiles(root string, skipped map[string]bool) ([]string, error) {
	return Files(root, skipped, func(name string) bool { return strings.HasSuffix(name, ".go") })
}

// Family is one rule with the fixture that proves it still bites. Every family
// has a fixture that must be refused and, next to it, the legal shape that must
// not be: a rule proved only in the direction that refuses is a rule that could
// be refusing everything while looking strict.
type Family struct {
	Name   string
	Target string
	Reason string
}

// ScanResult is what the provers need out of a gate's own measurement.
type ScanResult struct {
	Generated []string
	Findings  []Finding
}

// Scan reads one fixture the way the gate reads the tree: the provers ask for the
// fixtures and the gate answers with its own scanner, so a rule is proved against
// the same code path that judges the repository. The family travels with the
// directory because a gate may read one of its fixtures through a different
// entry point — the configuration family of the dead-code gate is asked for where
// the three sets of that surface are read, and not by the tree walk.
type Scan func(family Family, directory string) (ScanResult, error)

// ProveRefused runs every fixture and requires the rule that owns it to refuse
// it: a rule that stopped biting has to fail here, by name, instead of
// disappearing from a green run.
func ProveRefused(root string, families []Family, scan Scan) error {
	for _, entry := range families {
		directory := root + "/" + entry.Target
		result, err := scan(entry, directory)
		if err != nil {
			return err
		}
		found := false
		for _, refusal := range result.Findings {
			if refusal.Rule == entry.Name {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf(
				"a família %q deixou de recusar: a fixture %s produziu %d achado(s) e nenhum deles é dela",
				entry.Name, directory, len(result.Findings))
		}
	}
	return nil
}

// ProveAccepted is the other direction: the clean fixture of a rule must produce
// no finding of that rule.
func ProveAccepted(root string, families []Family, scan Scan) error {
	for _, entry := range families {
		directory := root + "/" + entry.Target
		result, err := scan(entry, directory)
		if err != nil {
			return err
		}
		if len(result.Findings) != 0 {
			return fmt.Errorf("a fixture limpa %s (família %q) produziu %d achado(s) e nenhum é esperado: %s",
				directory, entry.Name, len(result.Findings), result.Findings[0])
		}
	}
	return nil
}

// ProvenanceProbe is one proof about the exclusion itself, in both directions:
// the marked file has to be excluded, and the file that only mentions the marker
// has to be judged.
type ProvenanceProbe struct {
	Name     string
	Target   string
	Excluded bool
}

// ProveProvenance holds the two directions of the exclusion, and refuses the
// probe that would pass by proving nothing: a fixture expected to be judged has
// to have been judged. The probe reaches the scanner as a family whose name is
// the sentence of the probe, because the scanner answers for the directory and
// not for the reason it was asked.
func ProveProvenance(root string, probes []ProvenanceProbe, scan Scan) error {
	for _, probe := range probes {
		directory := root + "/" + probe.Target
		result, err := scan(Family{Name: probe.Name, Target: probe.Target}, directory)
		if err != nil {
			return err
		}
		excluded := len(result.Generated) > 0
		if excluded != probe.Excluded {
			return fmt.Errorf("%s: a fixture %s excluiu %d arquivo(s) por proveniência e este portão exige %v",
				probe.Name, directory, len(result.Generated), probe.Excluded)
		}
		if !probe.Excluded && len(result.Findings) == 0 {
			return fmt.Errorf("%s: a fixture %s não produziu achado, então não prova nada sobre arquivo julgado",
				probe.Name, directory)
		}
	}
	return nil
}

// SortFindings orders findings by path, line and rule. Two runs of a gate that
// answers in a different order are two gates.
func SortFindings(findings []Finding) {
	sort.Slice(findings, func(one, other int) bool {
		if findings[one].Path != findings[other].Path {
			return findings[one].Path < findings[other].Path
		}
		if findings[one].Line != findings[other].Line {
			return findings[one].Line < findings[other].Line
		}
		return findings[one].Rule < findings[other].Rule
	})
}
