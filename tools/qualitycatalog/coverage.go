package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The violation codes of the coverage half of the gate. The loader answers
// "does what this entry claims exist?"; coverage answers the other question,
// the one a complete catalog also has to answer: "is this the whole rule set?".
// An entry nobody asked for, a rule nobody mapped and a row whose class nobody
// can read are three defects, so they are three codes.
const (
	codeCoverageMissing    = "coverage-missing"
	codeCoverageOrphan     = "coverage-orphan"
	codeSeverityUnreadable = "severity-unreadable"
)

// The documents that declare the rule set, and the prefix that ties a catalog
// entry to the row it comes from.
const (
	RequirementsDocument = "docs/REQUIREMENTS.md"
	ThreatModelDocument  = "docs/THREAT_MODEL.md"

	// CatalogPrefix is how the catalog names an entry after the document row.
	CatalogPrefix = "QUAL-"
)

// The severity vocabulary of the threat model, as its rows write it. The two
// the phase demands an entry for come first; Média and Baixa are named too
// because the reading is "which severity does this row state", and a vocabulary
// that stops at the dangerous half would read a Média row as one that states
// nothing.
const (
	SeverityCritical = "Crítica"
	SeverityHigh     = "Alta"
	SeverityMedium   = "Média"
	SeverityLow      = "Baixa"
)

// idCell is the bold first cell of a rule row, in either document: `**REQ-…**`
// or `**THR-…**`. The header of the same table has a plain `ID` there, and that
// difference — bold identifier versus column name — is what tells the two apart
// without knowing either document's layout.
var idCell = regexp.MustCompile(`^([A-Z]+-[A-Z0-9-]+)$`)

// bold is what the documents use inside a cell, for the identifier, the
// severity and the emphasis of a sentence.
var bold = regexp.MustCompile(`\*\*(.+?)\*\*`)

// inPair reports whether a document is one of the two that declare the rule set
// the phase names. It is jurisdiction, stated once: the completeness this gate
// enforces is exactly "every MVP requirement and every critical or high threat
// has an entry". An entry that cites some other document — the fixture's spec,
// a section of the business rules — is judged by the loader that its document
// and its evidence exist, and this check has no table to compare it against.
//
// sourceDocument is the document a rule cites, without the anchor: an entry
// that points at a section of the matrix still comes from the matrix.
func inPair(document string) bool {
	document, _, _ = strings.Cut(document, "#")
	return document == RequirementsDocument || document == ThreatModelDocument
}

// declared is the rule set the checkout states, keyed by the identifier the
// catalog must carry for it. Each id also remembers the document that declares
// it, because an entry filed under the wrong document is an entry a reader
// cannot trace.
type declared struct {
	requirements map[string]string
	threats      map[string]string
}

// home is the document that declares this id, if either one does.
func (d declared) home(id string) (string, bool) {
	if path, ok := d.requirements[id]; ok {
		return path, true
	}
	path, ok := d.threats[id]
	return path, ok
}

// rows reads the cells of every Markdown table row whose first cell is a bold
// identifier. Everything else — headers, separators, prose — is not a row a
// rule can come from.
func rows(document string) [][]string {
	var found [][]string
	for _, line := range strings.Split(document, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}
		fields := strings.Split(strings.Trim(trimmed, "|"), "|")
		cells := make([]string, 0, len(fields))
		for _, field := range fields {
			cells = append(cells, cleanCell(field))
		}
		if len(cells) == 0 {
			continue
		}
		if _, ok := identifier(cells[0]); !ok {
			continue
		}
		found = append(found, cells)
	}
	return found
}

// identifier reads an identifier out of a cell, if the cell holds nothing else.
func identifier(cell string) (string, bool) {
	match := idCell.FindStringSubmatch(cell)
	if match == nil {
		return "", false
	}
	return match[1], true
}

// severity reads the severity a threat row states. It is read by value and not
// by column position, deliberately: the threat model holds one row whose actor
// cell is absent, so every cell after it sits one place to the left, and a
// positional read would call that row's mitigation its severity — silently,
// which is how a Crítica row would stop being one.
func severity(cells []string) (string, bool) {
	for _, candidate := range cells {
		switch candidate {
		case SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow:
			return candidate, true
		}
	}
	return "", false
}

// cleanCell strips the two decorations the documents use inside a cell — bold
// and inline code — and folds the whitespace, so that a severity written as
// `**Crítica**` and one written as `Crítica` are the same severity.
func cleanCell(cell string) string {
	cell = bold.ReplaceAllString(cell, "$1")
	cell = strings.ReplaceAll(cell, "`", "")
	return strings.Join(strings.Fields(cell), " ")
}

// declaredRules reads the rule set out of the documents themselves. It never
// takes the catalog's word for what exists: the point of the check is that the
// two lists are compared as they are.
func declaredRules(root string) (declared, []Violation) {
	rules := declared{requirements: map[string]string{}, threats: map[string]string{}}
	var violations []Violation
	for _, path := range []string{RequirementsDocument, ThreatModelDocument} {
		document, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			violations = append(violations, Violation{
				Rule:   path,
				Code:   codeSourceUnknown,
				Detail: fmt.Sprintf("the rule set the catalog must cover is unreadable: %v", err),
			})
			continue
		}
		for _, cells := range rows(string(document)) {
			identifier, _ := identifier(cells[0])
			switch {
			case strings.HasPrefix(identifier, "REQ-"):
				rules.requirements[identifier] = path
			case strings.HasPrefix(identifier, "THR-"):
				class, ok := severity(cells)
				if !ok {
					violations = append(violations, Violation{
						Rule:   identifier,
						Code:   codeSeverityUnreadable,
						Detail: fmt.Sprintf("%s states no severity from the document's own vocabulary: whether the rule demands an entry cannot be known, and a threat whose class is unknown is one nobody covers", path),
					})
					continue
				}
				if class == SeverityCritical || class == SeverityHigh {
					rules.threats[identifier] = path
				}
			}
		}
	}
	return rules, violations
}

// CheckCoverage judges the catalog's completeness: the entries must be exactly
// the rule set the documents declare. Both directions are defects.
//
// A rule the documents declare and the catalog does not map is a rule with no
// evidence demanded of it — the silence the phase exists to end. An entry the
// documents do not declare is a rule the product does not have, wearing the
// authority of a document it was never in.
func CheckCoverage(catalog Catalog, rules declared) []Violation {
	var violations []Violation
	covered := map[string]bool{}
	for _, rule := range catalog.Rules {
		document, _, _ := strings.Cut(rule.Source, "#")
		if !inPair(document) {
			continue
		}
		identifier := strings.TrimPrefix(rule.ID, CatalogPrefix)
		covered[identifier] = true
		home, ok := rules.home(identifier)
		if !ok {
			violations = append(violations, Violation{
				Rule:   rule.ID,
				Code:   codeCoverageOrphan,
				Detail: fmt.Sprintf("the documents declare no `%s`: an entry nobody asked for is a rule the product does not have", identifier),
			})
			continue
		}
		if document != home {
			violations = append(violations, Violation{
				Rule:   rule.ID,
				Code:   codeCoverageOrphan,
				Detail: fmt.Sprintf("`%s` is declared by %s and the entry cites %s: an entry filed under another document is one the reader cannot trace", identifier, home, document),
			})
		}
	}
	violations = append(violations, missingOf(rules.requirements, covered, "requirement")...)
	violations = append(violations, missingOf(rules.threats, covered, "critical or high threat")...)
	sort.Slice(violations, func(one, other int) bool {
		if violations[one].Rule != violations[other].Rule {
			return violations[one].Rule < violations[other].Rule
		}
		return violations[one].Code < violations[other].Code
	})
	return violations
}

// missingOf is the direction that costs the product money: a rule the documents
// declare, with no entry to demand evidence from it.
func missingOf(declaredByDocument map[string]string, covered map[string]bool, kind string) []Violation {
	missing := make([]string, 0, len(declaredByDocument))
	for identifier := range declaredByDocument {
		if !covered[identifier] {
			missing = append(missing, identifier)
		}
	}
	sort.Strings(missing)
	violations := make([]Violation, 0, len(missing))
	for _, identifier := range missing {
		violations = append(violations, Violation{
			Rule:   identifier,
			Code:   codeCoverageMissing,
			Detail: fmt.Sprintf("%s declares this %s and the catalog maps no rule to it: a rule with no evidence demanded of it is a rule nobody enforces", declaredByDocument[identifier], kind),
		})
	}
	return violations
}
