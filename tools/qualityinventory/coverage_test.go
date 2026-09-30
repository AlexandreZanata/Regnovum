// Tests of the semantic coverage inventory (P21-T06).
//
// The inventory joins three documents with six families of artifacts, and every
// join is a place where a report can be wrong without being obviously wrong: a
// citation that stopped resolving counted as evidence, a rule with no anchor
// counted as covered, or a committed report that no longer describes the tree
// and passes anyway. The tests below hold five properties:
//
//   - the complete fixture joins, so the four buckets can be non-empty and
//     still correct;
//   - the route's owner is the module that serves it, read from the adapter's
//     own route table, because a contract tag is a display name and a wrong
//     owner would be wrong in the one column a reader trusts;
//   - one mutation per violation code makes that code fire, each editing the
//     fixture by name and failing when the file it means to change is not
//     there, so no case can pass by mutating nothing;
//   - every code the tool declares is named by at least one mutation, and every
//     mutation names a code the tool declares;
//   - the report delivered in this checkout is the one this tree generates,
//     twice in a row, and every rule of the catalog has an anchor in it.
//
// The fixture checkout is a small tree of its own — two documents, a matrix, a
// contract, one migration, one adapter and one application file — so a mutation
// breaks exactly one thing.
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// caseDef is one way the inventory can be defeated, and the code that has to
// fire.
type caseDef struct {
	name  string
	code  string
	judge func(t *testing.T) Violations
}

// fixtureFiles is the whole fixture checkout: the two quality documents, the
// matrix that joins them to the code, and one artifact of every family. It is a
// map rather than a directory so that a mutation changes one file and the tree
// is rebuilt for the next case.
func fixtureFiles() map[string]string {
	return map[string]string{
		CatalogPath: `{
  "schema_version": 1,
  "rules": [
    {"id": "QUAL-REQ-FIX-01", "risk": "Q0", "modules": ["internal/fixture"],
     "tests": {"positive": ["internal/fixture/application/rules_test.go::TestRuleHolds"]}},
    {"id": "QUAL-REQ-FIX-02", "risk": "Q1", "modules": ["internal/fixture"],
     "tests": {"positive": ["internal/fixture/application/rules_test.go::TestOtherHolds"]}}
  ]
}`,
		EvidencePath: `{
  "schema_version": 1,
  "evidence": [
    {"id": "EVD-FIXTURE-UNIT-01", "kind": "unit", "suite": "SUITE-FIXTURE-UNIT", "risk": "Q0",
     "rules": ["QUAL-REQ-FIX-01"],
     "tests": ["internal/fixture/application/rules_test.go::TestRuleHolds"],
     "proves": "a regra crítica é provada pelo teste declarado"}
  ]
}`,
		MatrixPath: "# Matriz\n\n" +
			"| ID | Descrição do Requisito | Origem | Módulo | Fase | Endpoint | Caso de uso | Migration | Teste |\n" +
			"|---|---|---|---|---|---|---|---|---|\n" +
			"| **REQ-FIX-01** | a fixture faz o que a fixture promete | MVP §1 | `fixture` | `21` | `POST /api/v1/fixture` | `internal/fixture/application/rules.go` | `00099_fixture.sql` | `internal/fixture/application/rules_test.go::TestRuleHolds` |\n",
		ContractPath: `{
  "openapi": "3.1.0",
  "paths": {
    "/api/v1/fixture": {"post": {"tags": ["fixture"]}}
  }
}`,
		MigrationsDir + "/00099_fixture.sql": "CREATE TABLE fixture (id bigint);\n",
		"internal/fixture/adapters/http/routes.go": "package http\n\n" +
			"var routes = []route{\n\t{Method: \"POST\", Path: \"/api/v1/fixture\"},\n}\n",
		"internal/fixture/application/rules.go": "package application\n\n// Rules is the use case the matrix cites.\ntype Rules struct{}\n",
		"internal/fixture/application/rules_test.go": "package application\n\nimport \"testing\"\n\n" +
			"func TestRuleHolds(t *testing.T) {}\n\nfunc TestOtherHolds(t *testing.T) {}\n",
		JobsSourcePath: "package domain\n\ntype JobType string\n\n" +
			"const (\n\tTypeSomething JobType = \"something\"\n)\n\nvar AllJobTypes = []JobType{TypeSomething}\n",
		CLISourcePath: "package main\n\nfunc main() {\n\tswitch command {\n\tcase \"server\":\n\t\treturn\n\t}\n}\n",
	}
}

// writeRoot builds a checkout out of the fixture files.
func writeRoot(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range files {
		writeFile(t, root, path, content)
	}
	return root
}

// joined builds the checkout, reads it and joins it: the tree side of every
// case, with the refusals of the reading kept apart from the refusals of the
// join.
func joined(t *testing.T, mutate func(files map[string]string)) (string, Report, Violations) {
	t.Helper()
	files := fixtureFiles()
	if mutate != nil {
		mutate(files)
	}
	root := writeRoot(t, files)
	records, violations := ReadRecords(root)
	if len(violations) > 0 {
		return root, Report{}, violations
	}
	report, violations := Join(records)
	if len(violations) > 0 {
		return root, report, violations
	}
	return root, report, Verify(report)
}

// judgedTree is the tree-side judgement of one defect.
func judgedTree(t *testing.T, mutate func(files map[string]string)) Violations {
	t.Helper()
	_, _, violations := joined(t, mutate)
	return violations
}

// judgedCommitted is the report-side judgement: the fixture is generated, the
// committed pair is built from the report — mutated by the case — and the check
// judges it against a fresh join. It is how the drift and the integrity codes
// are reached without touching the tree.
func judgedCommitted(t *testing.T, mutate func(report *Report, doc *string)) Violations {
	t.Helper()
	_, report, violations := joined(t, nil)
	if len(violations) > 0 {
		t.Fatalf("the fixture does not join: %v", violations)
	}
	files := fixtureFiles()
	root := writeRoot(t, files)
	document := string(RenderMarkdown(report))
	if mutate != nil {
		mutate(&report, &document)
	}
	encoded, err := Render(report)
	if err != nil {
		t.Fatalf("the mutated report cannot be encoded: %v", err)
	}
	writeFile(t, root, ReportPath, string(encoded))
	writeFile(t, root, DocPath, document)

	records, readViolations := ReadRecords(root)
	if len(readViolations) > 0 {
		t.Fatalf("the fixture checkout is unreadable: %v", readViolations)
	}
	fresh, joinViolations := Join(records)
	if len(joinViolations) > 0 {
		t.Fatalf("the fixture does not join: %v", joinViolations)
	}
	return checkCommitted(root, ReportPath, DocPath, fresh)
}

// remove drops one file from the fixture, which is how every absent-source case
// is expressed.
func remove(files map[string]string, path string) {
	if _, ok := files[path]; !ok {
		panic("the fixture does not hold " + path + " to remove")
	}
	delete(files, path)
}

// cases is the whole table of defects. It is read twice: once to drive the
// assertions and once by the bidirectional check below.
func cases() []caseDef {
	return []caseDef{
		{
			name: "a catalog that is not in the checkout",
			code: codeSourceUnreadable,
			judge: func(t *testing.T) Violations {
				return judgedTree(t, func(files map[string]string) { remove(files, CatalogPath) })
			},
		},
		{
			name: "a register that is not in the checkout",
			code: codeSourceUnreadable,
			judge: func(t *testing.T) Violations {
				return judgedTree(t, func(files map[string]string) { remove(files, EvidencePath) })
			},
		},
		{
			name: "a matrix that is not in the checkout",
			code: codeSourceUnreadable,
			judge: func(t *testing.T) Violations {
				return judgedTree(t, func(files map[string]string) { remove(files, MatrixPath) })
			},
		},
		{
			name: "a contract that does not decode",
			code: codeSourceUnreadable,
			judge: func(t *testing.T) Violations {
				return judgedTree(t, func(files map[string]string) { files[ContractPath] = "not a contract" })
			},
		},
		{
			name: "a contract that declares no path",
			code: codeSourceUnreadable,
			judge: func(t *testing.T) Violations {
				return judgedTree(t, func(files map[string]string) { files[ContractPath] = `{"paths":{}}` })
			},
		},
		{
			name: "a migration directory that is empty",
			code: codeSourceUnreadable,
			judge: func(t *testing.T) Violations {
				return judgedTree(t, func(files map[string]string) { remove(files, MigrationsDir+"/00099_fixture.sql") })
			},
		},
		{
			name: "a job domain that declares no workload",
			code: codeSourceUnreadable,
			judge: func(t *testing.T) Violations {
				return judgedTree(t, func(files map[string]string) { files[JobsSourcePath] = "package domain\n" })
			},
		},
		{
			name: "a command that dispatches no subcommand",
			code: codeSourceUnreadable,
			judge: func(t *testing.T) Violations {
				return judgedTree(t, func(files map[string]string) { files[CLISourcePath] = "package main\n\nfunc main() {}\n" })
			},
		},
		{
			name: "no adapter that declares a route",
			code: codeSourceUnreadable,
			judge: func(t *testing.T) Violations {
				return judgedTree(t, func(files map[string]string) {
					remove(files, "internal/fixture/adapters/http/routes.go")
				})
			},
		},
		{
			name: "a rule whose test stopped existing",
			code: codeRuleAbsent,
			judge: func(t *testing.T) Violations {
				return judgedTree(t, func(files map[string]string) {
					files["internal/fixture/application/rules_test.go"] = "package application\n\nimport \"testing\"\n\nfunc TestRuleHolds(t *testing.T) {}\n"
				})
			},
		},
		{
			name: "a matrix that cites an endpoint the contract does not serve",
			code: codeCiteObsolete,
			judge: func(t *testing.T) Violations {
				return judgedTree(t, func(files map[string]string) {
					files[MatrixPath] = strings.Replace(files[MatrixPath], "`POST /api/v1/fixture`", "`POST /api/v1/gone`", 1)
				})
			},
		},
		{
			name: "a matrix that cites a migration that is not there",
			code: codeCiteObsolete,
			judge: func(t *testing.T) Violations {
				return judgedTree(t, func(files map[string]string) {
					files[MatrixPath] = strings.Replace(files[MatrixPath], "`00099_fixture.sql`", "`00001_gone.sql`", 1)
				})
			},
		},
		{
			name: "a matrix that cites a use case that is not there",
			code: codeCiteObsolete,
			judge: func(t *testing.T) Violations {
				return judgedTree(t, func(files map[string]string) {
					files[MatrixPath] = strings.Replace(files[MatrixPath], "`internal/fixture/application/rules.go`", "`internal/fixture/application/gone.go`", 1)
				})
			},
		},
		{
			name: "a matrix that cites a test function nobody wrote",
			code: codeCiteObsolete,
			judge: func(t *testing.T) Violations {
				return judgedTree(t, func(files map[string]string) {
					files[MatrixPath] = strings.Replace(files[MatrixPath], "::TestRuleHolds", "::TestNobodyWrote", 1)
				})
			},
		},
		{
			name: "a catalog that cites a test file that is not there",
			code: codeCiteObsolete,
			judge: func(t *testing.T) Violations {
				return judgedTree(t, func(files map[string]string) {
					files[CatalogPath] = strings.Replace(files[CatalogPath], "internal/fixture/application/rules_test.go::TestRuleHolds", "internal/fixture/application/gone_test.go::TestRuleHolds", 1)
				})
			},
		},
		{
			name: "a register that cites a test function nobody wrote",
			code: codeCiteObsolete,
			judge: func(t *testing.T) Violations {
				return judgedTree(t, func(files map[string]string) {
					files[EvidencePath] = strings.Replace(files[EvidencePath], "::TestRuleHolds", "::TestNobodyWrote", 1)
				})
			},
		},
		{
			name: "a report that is not in the checkout",
			code: codeReportMissing,
			judge: func(t *testing.T) Violations {
				_, report, violations := joined(t, nil)
				if len(violations) > 0 {
					t.Fatalf("the fixture does not join: %v", violations)
				}
				root := writeRoot(t, fixtureFiles())
				writeFile(t, root, DocPath, string(RenderMarkdown(report)))
				return checkCommitted(root, ReportPath, DocPath, report)
			},
		},
		{
			name: "a document that is not in the checkout",
			code: codeReportMissing,
			judge: func(t *testing.T) Violations {
				_, report, violations := joined(t, nil)
				if len(violations) > 0 {
					t.Fatalf("the fixture does not join: %v", violations)
				}
				root := writeRoot(t, fixtureFiles())
				encoded, err := Render(report)
				if err != nil {
					t.Fatalf("the report cannot be encoded: %v", err)
				}
				writeFile(t, root, ReportPath, string(encoded))
				return checkCommitted(root, ReportPath, DocPath, report)
			},
		},
		{
			name: "a report that stopped describing the tree",
			code: codeReportDrift,
			judge: func(t *testing.T) Violations {
				return judgedCommitted(t, func(report *Report, _ *string) {
					report.Covered = report.Covered[:1]
					report.Summary = Summarize(*report)
				})
			},
		},
		{
			name: "a document that stopped describing the report",
			code: codeReportDrift,
			judge: func(t *testing.T) Violations {
				return judgedCommitted(t, func(_ *Report, doc *string) {
					*doc = strings.Replace(*doc, "Cobertura é contada", "Cobertura por linha", 1)
				})
			},
		},
		{
			name: "a summary that disagrees with the tables",
			code: codeReportInconsistent,
			judge: func(t *testing.T) Violations {
				return judgedCommitted(t, func(report *Report, _ *string) { report.Summary.Covered = 99 })
			},
		},
		{
			name: "an obsolete count that disagrees with the citation corpus",
			code: codeReportInconsistent,
			judge: func(t *testing.T) Violations {
				return judgedCommitted(t, func(report *Report, _ *string) { report.Summary.Obsolete = 3 })
			},
		},
		{
			name: "a citation that stopped resolving while the obsolete list stayed empty",
			code: codeReportInconsistent,
			judge: func(t *testing.T) Violations {
				return judgedCommitted(t, func(report *Report, _ *string) {
					report.Citations[0].Resolves = false
				})
			},
		},
		{
			name: "an obsolete entry no citation supports",
			code: codeReportInconsistent,
			judge: func(t *testing.T) Violations {
				return judgedCommitted(t, func(report *Report, _ *string) {
					report.Obsolete = append(report.Obsolete, Obsolete{Document: CatalogPath, Row: "QUAL-REQ-FIX-01", Family: FamilyTest, Reference: "nowhere::TestGone"})
				})
			},
		},
		{
			name: "a family whose three numbers do not add up",
			code: codeReportInconsistent,
			judge: func(t *testing.T) Violations {
				return judgedCommitted(t, func(report *Report, _ *string) {
					report.Families[0].Named = report.Families[0].Artifacts + 1
				})
			},
		},
		{
			name: "a family whose orphan count disagrees with the orphan list",
			code: codeReportInconsistent,
			judge: func(t *testing.T) Violations {
				return judgedCommitted(t, func(report *Report, _ *string) {
					report.Families[len(report.Families)-1].Orphans++
					report.Families[len(report.Families)-1].Artifacts++
				})
			},
		},
		{
			name: "a family whose citation count disagrees with the corpus",
			code: codeReportInconsistent,
			judge: func(t *testing.T) Violations {
				return judgedCommitted(t, func(report *Report, _ *string) {
					report.Families[0].Citations = 0
				})
			},
		},
		{
			name: "a family whose unresolved count disagrees with the corpus",
			code: codeReportInconsistent,
			judge: func(t *testing.T) Violations {
				return judgedCommitted(t, func(report *Report, _ *string) {
					report.Families[0].Unresolved = 2
				})
			},
		},
		{
			name: "a family declared twice",
			code: codeReportInconsistent,
			judge: func(t *testing.T) Violations {
				return judgedCommitted(t, func(report *Report, _ *string) {
					report.Families = append(report.Families, report.Families[0])
				})
			},
		},
		{
			name: "a family carrying a label the vocabulary does not know",
			code: codeReportInconsistent,
			judge: func(t *testing.T) Violations {
				return judgedCommitted(t, func(report *Report, _ *string) {
					report.Families[0].Label = "rotas"
				})
			},
		},
		{
			name: "a family of the vocabulary the report does not carry",
			code: codeReportInconsistent,
			judge: func(t *testing.T) Violations {
				return judgedCommitted(t, func(report *Report, _ *string) {
					report.Families = report.Families[1:]
				})
			},
		},
		{
			name: "a report written against another version of the contract",
			code: codeReportInconsistent,
			judge: func(t *testing.T) Violations {
				return judgedCommitted(t, func(report *Report, _ *string) { report.SchemaVersion = 2 })
			},
		},
	}
}

// TestTheCompleteFixtureIsAccepted is the control. Without it a command that
// refused everything would look like a command.
func TestTheCompleteFixtureIsAccepted(t *testing.T) {
	_, report, violations := joined(t, nil)
	if len(violations) > 0 {
		t.Fatalf("the complete fixture is refused: %v", violations)
	}
	if report.Summary.Covered != 2 || report.Summary.Absent != 0 {
		t.Fatalf("the fixture has two rules and the report says %d covered and %d absent", report.Summary.Covered, report.Summary.Absent)
	}
	if report.Summary.Citations == 0 || report.Summary.Artifacts == 0 {
		t.Fatalf("the fixture joins nothing: %+v", report.Summary)
	}
	if report.Summary.Orphans == 0 {
		t.Fatal("the fixture has a command and a job type no rule reaches, so the orphan bucket cannot be empty: the case would pass on a report that never looks")
	}
	if violations := Verify(report); len(violations) > 0 {
		t.Fatalf("the generated report is not consistent with itself: %v", violations)
	}
}

// TestTheFourBucketsAreAllPresent is the phase's minimal validation, stated as
// a property of the document: the report has to carry the four lists, and it
// has to fill them from what it measured — a bucket that is only ever empty is
// a bucket nobody has seen work.
func TestTheFourBucketsAreAllPresent(t *testing.T) {
	// One rule loses the only anchor it has and one citation loses its artifact,
	// so the two lists that are empty on a healthy tree are exercised here.
	_, report, _ := joined(t, func(files map[string]string) {
		files["internal/fixture/application/rules_test.go"] = "package application\n\nimport \"testing\"\n\nfunc TestRuleHolds(t *testing.T) {}\n"
		files[MatrixPath] = strings.Replace(files[MatrixPath], "`00099_fixture.sql`", "`00001_gone.sql`", 1)
	})
	if report.Summary.Absent == 0 {
		t.Errorf("the report carries no absent rule: %+v", report.Summary)
	}
	if report.Summary.Obsolete == 0 {
		t.Errorf("the report carries no obsolete citation: %+v", report.Summary)
	}
	if report.Summary.Orphans == 0 {
		t.Errorf("the report carries no orphan: %+v", report.Summary)
	}

	encoded, err := Render(report)
	if err != nil {
		t.Fatalf("the report cannot be encoded: %v", err)
	}
	for _, field := range []string{`"covered"`, `"absent"`, `"obsolete"`, `"orphans"`} {
		if !bytes.Contains(encoded, []byte(field)) {
			t.Errorf("the report has no %s list", field)
		}
	}
}

// TestTheRouteOwnerIsTheModuleThatServesIt is the property `owners.go` exists
// for: the contract's tag is a display name and the report has to name the
// package, because that package is what the join asks the second question
// about.
func TestTheRouteOwnerIsTheModuleThatServesIt(t *testing.T) {
	root, _, violations := joined(t, nil)
	if len(violations) > 0 {
		t.Fatalf("the fixture does not join: %v", violations)
	}
	owners, err := readRouteOwners(root)
	if err != nil {
		t.Fatalf("the fixture declares no route: %v", err)
	}
	if got := owners.of("/api/v1/fixture"); got != "internal/fixture" {
		t.Fatalf("the route is attributed to %q and the only adapter that declares it is internal/fixture", got)
	}
	if got := owners.of("/api/v1/nowhere"); got != "internal/platform" {
		t.Fatalf("a route nothing declares is attributed to %q: the operator surface is the platform's", got)
	}
}

// TestEveryDefectIsRefused drives one mutation per defect and names the code it
// has to make the command emit.
func TestEveryDefectIsRefused(t *testing.T) {
	for _, testCase := range cases() {
		t.Run(testCase.name, func(t *testing.T) {
			violations := testCase.judge(t)
			if !reaches(violations, testCase.code) {
				t.Fatalf("the defect did not make `%s` fire: %v", testCase.code, violations)
			}
		})
	}
}

// TestEveryDeclaredCodeHasAMutation runs in both directions: a code the tool
// declares and no test makes fire is a rule nobody has seen work, and a code a
// test expects and the tool no longer declares is a test that would pass
// against a program that has moved on.
func TestEveryDeclaredCodeHasAMutation(t *testing.T) {
	declared := declaredCodes(t)
	proven := map[string]string{}
	for _, testCase := range cases() {
		proven[testCase.code] = "TestEveryDefectIsRefused"
	}
	for code, source := range declared {
		if _, ok := proven[code]; !ok {
			t.Errorf("`%s` is declared in %s and no test makes it fire: a code nobody has seen refuse anything guards nothing", code, source)
		}
	}
	for code, test := range proven {
		if _, ok := declared[code]; !ok {
			t.Errorf("`%s` is expected by %s and the tool does not declare it", code, test)
		}
	}
}

// declaredCodes reads the violation codes out of the tool's own sources instead
// of listing them again here: a list repeated in the test is a list that drifts,
// and the point of this check is that it cannot.
func declaredCodes(t *testing.T) map[string]string {
	t.Helper()
	pattern := regexp.MustCompile(`(?m)^\s*code[A-Za-z][A-Za-z0-9]*\s*=\s*"([^"]+)"`)
	declared := map[string]string{}
	for _, source := range []string{"records.go", "join.go"} {
		raw, err := os.ReadFile(source)
		if err != nil {
			t.Fatalf("the source %s is unreadable: %v", source, err)
		}
		for _, match := range pattern.FindAllStringSubmatch(string(raw), -1) {
			declared[match[1]] = source
		}
	}
	return declared
}

// TestTheCommandWritesAndThenAgreesWithItself is the third validation of the
// phase, end to end: writing the report leaves the checkout consistent, and a
// second run without -write finds no drift — generation does not change files
// when there is nothing to change.
func TestTheCommandWritesAndThenAgreesWithItself(t *testing.T) {
	root := writeRoot(t, fixtureFiles())

	var stdout, stderr bytes.Buffer
	if status := run([]string{"-root", root, "-write"}, &stdout, &stderr); status != exitOK {
		t.Fatalf("writing the fixture report failed with status %d: %s%s", status, stdout.String(), stderr.String())
	}
	first, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(ReportPath)))
	if err != nil {
		t.Fatalf("the report was not written: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if status := run([]string{"-root", root}, &stdout, &stderr); status != exitOK {
		t.Fatalf("the committed fixture report drifts from the tree it was generated from: %s%s", stdout.String(), stderr.String())
	}
	if status := run([]string{"-root", root, "-write"}, &stdout, &stderr); status != exitOK {
		t.Fatalf("writing twice failed: %s%s", stdout.String(), stderr.String())
	}
	second, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(ReportPath)))
	if err != nil {
		t.Fatalf("the report was not rewritten: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("two generations over the same tree produced different bytes: the report is not deterministic, and the drift check would be useless")
	}
}

// TestRemovingACriticalEvidenceFailsTheCommand is the validation the phase
// states as a sentence, as a measurement: take the identity that proves a
// critical rule out of the register and the command refuses the checkout.
func TestRemovingACriticalEvidenceFailsTheCommand(t *testing.T) {
	root := writeRoot(t, fixtureFiles())
	var stdout, stderr bytes.Buffer
	if status := run([]string{"-root", root, "-write"}, &stdout, &stderr); status != exitOK {
		t.Fatalf("the fixture report cannot be written: %s%s", stdout.String(), stderr.String())
	}

	files := fixtureFiles()
	files[EvidencePath] = `{"schema_version": 1, "evidence": []}`
	writeFile(t, root, EvidencePath, files[EvidencePath])

	stdout.Reset()
	stderr.Reset()
	status := run([]string{"-root", root}, &stdout, &stderr)
	if status == exitOK {
		t.Fatalf("the command accepted a tree with no evidence identity for its critical rule: %s", stdout.String())
	}
	if !reaches(checkCommitted(root, ReportPath, DocPath, Report{}), codeReportDrift) && !strings.Contains(stderr.String(), codeReportDrift) {
		t.Fatalf("the command failed for a reason the phase did not ask about: %s", stderr.String())
	}
}

// TestTheDeliveredReportIsTheOneThisTreeGenerates holds the tool to the files
// in this checkout: the report as delivered, the tree it was generated from,
// and the claim the phase's exit gate makes — that every backend rule has an
// anchor a machine can point at.
func TestTheDeliveredReportIsTheOneThisTreeGenerates(t *testing.T) {
	root := filepath.Join("..", "..")
	records, violations := ReadRecords(root)
	if len(violations) > 0 {
		t.Fatalf("the delivered sources are unreadable: %v", violations)
	}
	report, violations := Join(records)
	if len(violations) > 0 {
		t.Fatalf("the delivered tree does not join: %v", violations)
	}
	if violations := Verify(report); len(violations) > 0 {
		t.Fatalf("the delivered report is not consistent with itself: %v", violations)
	}
	if report.Summary.Covered != report.Catalog.Rules {
		t.Errorf("the delivered tree has %d rule(s) and only %d carry an anchor: a rule nothing proves is a rule nobody applies", report.Catalog.Rules, report.Summary.Covered)
	}
	if report.Catalog.Critical == 0 {
		t.Fatal("the delivered catalog declares no critical rule: this test would then prove nothing about coverage where it matters")
	}

	committed, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(ReportPath)))
	if err != nil {
		t.Fatalf("the delivered report is unreadable: %v", err)
	}
	parsed, err := Parse(committed)
	if err != nil {
		t.Fatalf("the delivered report does not decode: %v", err)
	}
	generated, err := Render(report)
	if err != nil {
		t.Fatalf("the report cannot be encoded: %v", err)
	}
	if !bytes.Equal(committed, generated) {
		t.Fatalf("the delivered report is not the one this tree generates (%s): run `make quality-inventory-write` and commit the inventory", firstDifference(committed, generated))
	}
	if parsed.Summary != report.Summary {
		t.Fatalf("the delivered report states %+v and the tree generates %+v", parsed.Summary, report.Summary)
	}
	if violations := Verify(parsed); len(violations) > 0 {
		t.Fatalf("the delivered report is not consistent with itself: %v", violations)
	}

	document, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(DocPath)))
	if err != nil {
		t.Fatalf("the delivered document is unreadable: %v", err)
	}
	if !bytes.Equal(document, RenderMarkdown(report)) {
		t.Fatal("the delivered document is not the rendering of the delivered report")
	}
	for _, section := range []string{"## 3. Coberto", "## 4. Ausente", "## 5. Obsoleto", "## 6. Órfão"} {
		if !bytes.Contains(document, []byte(section)) {
			t.Errorf("the delivered document has no %q section: the inventory lists four buckets and the document has to show all four", section)
		}
	}
}

// TestTheDeliveredReportIsDeterministic states the property the drift check
// rests on: the same tree produces the same bytes, or the gate would fail for a
// reason nobody could act on.
func TestTheDeliveredReportIsDeterministic(t *testing.T) {
	root := filepath.Join("..", "..")
	first := renderTwice(t, root)
	second := renderTwice(t, root)
	if !bytes.Equal(first, second) {
		t.Fatal("two joins of the same tree produced different bytes")
	}
}

func renderTwice(t *testing.T, root string) []byte {
	t.Helper()
	records, violations := ReadRecords(root)
	if len(violations) > 0 {
		t.Fatalf("the delivered sources are unreadable: %v", violations)
	}
	report, violations := Join(records)
	if len(violations) > 0 {
		t.Fatalf("the delivered tree does not join: %v", violations)
	}
	encoded, err := Render(report)
	if err != nil {
		t.Fatalf("the report cannot be encoded: %v", err)
	}
	return encoded
}

// TestTheMatrixIsReadByColumnName is the lesson the threat model already taught
// once: a document with two table layouts is read by the name of its columns,
// and a reader that trusted positions would take one column for another.
func TestTheMatrixIsReadByColumnName(t *testing.T) {
	files := fixtureFiles()
	files[MatrixPath] += "\n| ID | Invariante do Negócio | Módulo Principal | Fase | Mecanismo de Garantia | Migration | Teste |\n" +
		"|---|---|---|---|---|---|---|\n" +
		"| **REQ-INV-01** | a fixture não depende do que não deve | `fixture` | `21` | o domínio não conhece plano | `00099_fixture.sql` | `internal/fixture/application/rules_test.go::TestRuleHolds` |\n"
	root := writeRoot(t, files)
	records, violations := ReadRecords(root)
	if len(violations) > 0 {
		t.Fatalf("the two-table matrix is unreadable: %v", violations)
	}
	rows := map[string]MatrixRow{}
	for _, row := range records.Matrix {
		rows[row.ID] = row
	}
	invariant, ok := rows["REQ-INV-01"]
	if !ok {
		t.Fatalf("the invariants table was not read: %v", rows)
	}
	if len(invariant.Endpoints) != 0 || len(invariant.UseCases) != 0 {
		t.Fatalf("the invariants table has no endpoint and no use case column, and the reader invented %v and %v", invariant.Endpoints, invariant.UseCases)
	}
	if len(invariant.Migrations) != 1 || len(invariant.Tests) != 1 {
		t.Fatalf("the invariants table declares one migration and one test and the reader found %v and %v", invariant.Migrations, invariant.Tests)
	}
}

// reaches reports whether the run produced the code, or a violation whose
// detail carries it: the command prints codes, and the tests hold them there.
func reaches(violations Violations, code string) bool {
	for _, violation := range violations {
		if violation.Code == code || strings.Contains(violation.Detail, code) {
			return true
		}
	}
	return false
}

func writeFile(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("the fixture root is not writable: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("the fixture file is not writable: %v", err)
	}
}
