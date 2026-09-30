package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// SchemaVersion is the only report version this tool writes and reads.
const SchemaVersion = 1

// Covered is one rule with the anchors the join found for it. It is the unit of
// the report for the reason the phase states in one line: coverage is counted
// per rule, never per line — a file a test happens to execute is not a rule
// anyone proved.
type Covered struct {
	Rule     string   `json:"rule"`
	Risk     string   `json:"risk"`
	Anchors  []string `json:"anchors"`
	Tests    int      `json:"tests"`
	Evidence []string `json:"evidence"`
}

// CitationRow is one claim a document makes about the tree, with the answer.
// The corpus is part of the report on purpose: it is what makes the tally
// derivable from the document rather than a sentence the document states about
// itself.
type CitationRow struct {
	Document  string `json:"document"`
	Row       string `json:"row"`
	Family    string `json:"family"`
	Reference string `json:"reference"`
	Resolves  bool   `json:"resolves"`
}

// Obsolete is one citation that outlived its artifact.
type Obsolete struct {
	Document  string `json:"document"`
	Row       string `json:"row"`
	Family    string `json:"family"`
	Reference string `json:"reference"`
}

// Orphan is one artifact of a family that no document names by name and whose
// package no rule covers.
type Orphan struct {
	Family string `json:"family"`
	Name   string `json:"name"`
	Owner  string `json:"owner"`
}

// FamilyRow is the shape of one family in the join: how many artifacts it has,
// how many are traced, how many are not, and how many claims of the documents
// aim at it. The test family has no artifact universe — a test reference is a
// citation, not something the inventory enumerates — so its row is read through
// the citation columns and says so instead of pretending to be empty.
type FamilyRow struct {
	Family     string `json:"family"`
	Label      string `json:"label"`
	Artifacts  int    `json:"artifacts"`
	Named      int    `json:"named"`
	Orphans    int    `json:"orphans"`
	Citations  int    `json:"citations"`
	Unresolved int    `json:"unresolved"`
}

// Summary is the report's tally. The command recomputes it from the lists
// before it trusts it — and so does whoever reads a committed report — so that
// it cannot become a number that disagrees with the tables below it.
type Summary struct {
	Rules     int `json:"rules"`
	Covered   int `json:"covered"`
	Absent    int `json:"absent"`
	Evidence  int `json:"evidence"`
	Citations int `json:"citations"`
	Obsolete  int `json:"obsolete"`
	Artifacts int `json:"artifacts"`
	Orphans   int `json:"orphans"`
}

// Catalog is the catalog's own size, printed next to the coverage so that a
// shrinking rule set cannot look like a growing one.
type Catalog struct {
	Rules    int `json:"rules"`
	Critical int `json:"critical"`
}

// Report is the whole document, in a fixed field order so that two runs over
// the same tree produce the same bytes.
type Report struct {
	SchemaVersion int           `json:"schema_version"`
	Generator     string        `json:"generator"`
	Catalog       Catalog       `json:"catalog"`
	Summary       Summary       `json:"summary"`
	Families      []FamilyRow   `json:"families"`
	Covered       []Covered     `json:"covered"`
	Absent        []Covered     `json:"absent"`
	Obsolete      []Obsolete    `json:"obsolete"`
	Orphans       []Orphan      `json:"orphans"`
	Citations     []CitationRow `json:"citations"`
}

// Join crosses the documents with the tree and produces the report, or the
// refusals that say what it could not cross.
func Join(records Records) (Report, Violations) {
	var violations Violations
	report := Report{SchemaVersion: SchemaVersion, Generator: "tools/qualityinventory"}
	for _, rule := range records.Rules {
		report.Catalog.Rules++
		if rule.Risk == "Q0" {
			report.Catalog.Critical++
		}
	}

	operations := map[string]bool{}
	for _, artifact := range records.Artifacts {
		if artifact.Family == FamilyRoute {
			operations[artifact.Name] = true
		}
	}

	// Every citation is resolved first. A citation that points at nothing is a
	// document making a claim the tree does not keep, and the report has to say
	// which claim it is rather than count it as evidence.
	for _, citation := range records.Citations {
		resolves := citationResolves(records.Root, citation, operations)
		report.Citations = append(report.Citations, CitationRow{
			Document:  citation.Document,
			Row:       citation.Row,
			Family:    citation.Family,
			Reference: citation.Reference,
			Resolves:  resolves,
		})
		if resolves {
			continue
		}
		report.Obsolete = append(report.Obsolete, Obsolete{
			Document:  citation.Document,
			Row:       citation.Row,
			Family:    citation.Family,
			Reference: citation.Reference,
		})
		violations = append(violations, Violation{
			Row:    citation.Row,
			Code:   codeCiteObsolete,
			Detail: fmt.Sprintf("`%s` names `%s` as a %s and nothing of that name is in the checkout", citation.Document, citation.Reference, citation.Family),
		})
	}

	matrix := map[string]MatrixRow{}
	for _, row := range records.Matrix {
		matrix[row.ID] = row
	}
	byRule := map[string][]string{}
	for _, evidence := range records.Evidence {
		for _, rule := range evidence.Rules {
			byRule[rule] = append(byRule[rule], evidence.ID)
		}
	}

	for _, rule := range records.Rules {
		covered := Covered{Rule: rule.ID, Risk: rule.Risk, Anchors: []string{}, Evidence: []string{}}
		anchors := map[string]bool{}
		for _, reference := range rule.Tests {
			if declaresTest(records.Root, reference) {
				covered.Tests++
				anchors[FamilyTest] = true
			}
		}
		if row, ok := matrix[matrixIDOf(rule.ID)]; ok {
			for _, endpoint := range row.Endpoints {
				if operations[endpoint] {
					anchors[FamilyRoute] = true
				}
			}
			for _, useCase := range row.UseCases {
				if exists(records.Root, useCase) {
					anchors[FamilyUseCase] = true
				}
			}
			for _, migration := range row.Migrations {
				if exists(records.Root, filepath.ToSlash(filepath.Join(MigrationsDir, migration))) {
					anchors[FamilyMigration] = true
				}
			}
		}
		if identities := byRule[rule.ID]; len(identities) > 0 {
			covered.Evidence = append([]string(nil), identities...)
			sort.Strings(covered.Evidence)
		}
		for _, family := range Families {
			if anchors[family] {
				covered.Anchors = append(covered.Anchors, family)
			}
		}
		if len(covered.Anchors) == 0 && len(covered.Evidence) > 0 {
			covered.Anchors = append(covered.Anchors, "identity")
		}
		if len(covered.Anchors) == 0 {
			report.Absent = append(report.Absent, covered)
			violations = append(violations, Violation{
				Row:    rule.ID,
				Code:   codeRuleAbsent,
				Detail: "no anchor of this rule resolves: no test proves it, no route, use case or migration carries it and no evidence identity declares it — a rule nothing in the tree proves is a rule nobody applies",
			})
			continue
		}
		report.Covered = append(report.Covered, covered)
	}

	// The orphan half of the join: an artifact that no document cites by name
	// and whose package no rule covers is a surface the quality documents do
	// not reach at all.
	citedNames := map[string]bool{}
	for _, citation := range records.Citations {
		citedNames[citation.Reference] = true
	}
	coveredOwners := map[string]bool{}
	for _, rule := range records.Rules {
		for _, module := range rule.Modules {
			coveredOwners[module] = true
		}
	}
	counts := map[string]*FamilyRow{}
	for _, family := range Families {
		counts[family] = &FamilyRow{Family: family, Label: Labels[family]}
	}
	for _, artifact := range records.Artifacts {
		row, ok := counts[artifact.Family]
		if !ok {
			continue
		}
		row.Artifacts++
		if citedNames[artifact.Name] || ownerCovered(coveredOwners, artifact.Owner) {
			row.Named++
			continue
		}
		row.Orphans++
		report.Orphans = append(report.Orphans, Orphan{Family: artifact.Family, Name: artifact.Name, Owner: artifact.Owner})
	}
	for _, family := range Families {
		report.Families = append(report.Families, *counts[family])
	}
	for _, citation := range report.Citations {
		for index := range report.Families {
			if report.Families[index].Family != citation.Family {
				continue
			}
			report.Families[index].Citations++
			if !citation.Resolves {
				report.Families[index].Unresolved++
			}
		}
	}
	sort.Slice(report.Orphans, func(one, other int) bool {
		if report.Orphans[one].Family != report.Orphans[other].Family {
			return report.Orphans[one].Family < report.Orphans[other].Family
		}
		return report.Orphans[one].Name < report.Orphans[other].Name
	})

	report.Summary = Summarize(report)
	sortViolations(violations)
	return report, violations
}

// Summarize recomputes the tally from the lists. It is used when the report is
// generated and when a committed report is read, so that the two can be held
// against each other: a summary is a claim about the document, and this is how
// the document keeps it.
func Summarize(report Report) Summary {
	summary := Summary{
		Rules:     report.Catalog.Rules,
		Covered:   len(report.Covered),
		Absent:    len(report.Absent),
		Evidence:  distinctEvidence(report),
		Citations: len(report.Citations),
		Obsolete:  len(report.Obsolete),
	}
	for _, family := range report.Families {
		summary.Artifacts += family.Artifacts
		summary.Orphans += family.Orphans
	}
	return summary
}

// distinctEvidence counts the identities the covered and absent rows name.
func distinctEvidence(report Report) int {
	seen := map[string]bool{}
	for _, rows := range [][]Covered{report.Covered, report.Absent} {
		for _, row := range rows {
			for _, identity := range row.Evidence {
				seen[identity] = true
			}
		}
	}
	return len(seen)
}

// Verify checks the report against itself: every relationship the document
// states twice has to agree, and the tally has to be the one the tables add up
// to. A generated report that fails this is a defect of the tool; a committed
// report that fails it has been edited by hand.
func Verify(report Report) Violations {
	var violations Violations
	refuse := func(row, detail string) {
		violations = append(violations, Violation{Row: row, Code: codeReportInconsistent, Detail: detail})
	}
	if report.SchemaVersion != SchemaVersion {
		refuse("report", fmt.Sprintf("`schema_version` is %d and this tool reads %d", report.SchemaVersion, SchemaVersion))
		return violations
	}
	if recomputed := Summarize(report); recomputed != report.Summary {
		refuse("report", fmt.Sprintf("the summary states %+v and the tables add up to %+v", report.Summary, recomputed))
	}
	unresolved := 0
	for _, citation := range report.Citations {
		if !citation.Resolves {
			unresolved++
		}
	}
	if unresolved != len(report.Obsolete) {
		refuse("report", fmt.Sprintf("%d citation(s) do not resolve and the obsolete list carries %d", unresolved, len(report.Obsolete)))
	}
	perFamily := map[string]int{}
	for _, orphan := range report.Orphans {
		perFamily[orphan.Family]++
	}
	perFamilyCitations := map[string]int{}
	perFamilyUnresolved := map[string]int{}
	for _, citation := range report.Citations {
		perFamilyCitations[citation.Family]++
		if !citation.Resolves {
			perFamilyUnresolved[citation.Family]++
		}
	}
	seen := map[string]bool{}
	for _, family := range report.Families {
		if seen[family.Family] {
			refuse(family.Family, "the family is declared twice: a table that names the same family twice adds it twice")
		}
		seen[family.Family] = true
		if family.Label != Labels[family.Family] {
			refuse(family.Family, fmt.Sprintf("the family is labelled %q and the vocabulary calls it %q", family.Label, Labels[family.Family]))
		}
		if family.Named+family.Orphans != family.Artifacts {
			refuse(family.Family, fmt.Sprintf("the family states %d artifact(s), %d named and %d orphan(s), and the three do not add up", family.Artifacts, family.Named, family.Orphans))
		}
		if perFamily[family.Family] != family.Orphans {
			refuse(family.Family, fmt.Sprintf("the family states %d orphan(s) and the orphan list carries %d", family.Orphans, perFamily[family.Family]))
		}
		if perFamilyCitations[family.Family] != family.Citations {
			refuse(family.Family, fmt.Sprintf("the family states %d citation(s) and the corpus carries %d", family.Citations, perFamilyCitations[family.Family]))
		}
		if perFamilyUnresolved[family.Family] != family.Unresolved {
			refuse(family.Family, fmt.Sprintf("the family states %d unresolved citation(s) and the corpus carries %d", family.Unresolved, perFamilyUnresolved[family.Family]))
		}
	}
	for _, family := range Families {
		if !seen[family] {
			refuse(family, "the vocabulary declares a family the report does not carry")
		}
	}
	for family := range perFamily {
		if _, ok := Labels[family]; !ok {
			refuse(family, "the orphan list names a family the vocabulary does not declare")
		}
	}
	sortViolations(violations)
	return violations
}

// citationResolves answers "is this claim kept by the tree?" for every family.
func citationResolves(root string, citation Citation, operations map[string]bool) bool {
	switch citation.Family {
	case FamilyRoute:
		return operations[citation.Reference]
	case FamilyMigration:
		return exists(root, filepath.ToSlash(filepath.Join(MigrationsDir, citation.Reference)))
	case FamilyUseCase:
		return exists(root, citation.Reference)
	case FamilyTest:
		return declaresTest(root, citation.Reference)
	}
	return false
}

// ownerCovered reports whether a rule of the catalog names the package an
// artifact lives in, or one of its ancestors. It is the second half of "is this
// traced?": a document that never says the word "wallet" still traces the
// wallet's routes if it traces the wallet module.
func ownerCovered(coveredOwners map[string]bool, owner string) bool {
	if owner == "" {
		return false
	}
	if coveredOwners[owner] {
		return true
	}
	for module := range coveredOwners {
		if strings.HasPrefix(owner, module+"/") {
			return true
		}
	}
	return false
}

// matrixIDOf maps a catalog rule to the matrix row it traces: the catalog
// prefixes the identifier with the document it belongs to and the matrix does
// not, so the join has to strip it in exactly one place.
func matrixIDOf(rule string) string {
	return strings.TrimPrefix(rule, "QUAL-")
}
