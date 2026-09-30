package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseFindingsReadsDiagnosticsAndIgnoresProse holds the reader to the one
// thing it must not do: invent a finding out of a line that is not one, because
// a broken run that reads as a clean run is the failure this gate cannot have.
func TestParseFindingsReadsDiagnosticsAndIgnoresProse(t *testing.T) {
	output := strings.Join([]string{
		"warning: \"./tools/x/...\" matched no packages",
		"internal/wallet/adapters/postgres/store.go:41:2: assignment to nil map (SA5000)",
		"# github.com/AlexandreZanata/Regnovum/internal/wallet",
		"internal/wallet/adapters/http/handler.go:9:1: comment on exported function should be of the form \"Handler ...\" (ST1020)",
		"exit status 1",
	}, "\n")

	findings := parseFindings(output)
	if len(findings) != 2 {
		t.Fatalf("the reader kept %d findings, want 2: %+v", len(findings), findings)
	}
	// The reader sorts what it keeps, so the assertion looks the findings up by
	// check instead of by position.
	byCheck := map[string]Finding{}
	for _, finding := range findings {
		byCheck[finding.Check] = finding
	}
	if finding, ok := byCheck["SA5000"]; !ok || finding.Line != 41 {
		t.Errorf("the nil-map finding = %+v, want SA5000 at line 41", finding)
	}
	if _, ok := byCheck["ST1020"]; !ok {
		t.Errorf("the style finding was dropped: %+v", findings)
	}

	// The same check with the same message twice in one file is one identity
	// with a count of two, and a second finding is a change the baseline has to
	// see.
	groups := groupFindings(parseFindings(strings.Join([]string{
		"a.go:1:1: same message (S1011)",
		"a.go:9:1: same message (S1011)",
	}, "\n")))
	if len(groups) != 1 || groups[0].Count != 2 {
		t.Fatalf("grouping = %+v, want one group with count 2", groups)
	}
}

// TestTheBaselineIsARatchet drives the comparison in both directions.
func TestTheBaselineIsARatchet(t *testing.T) {
	baseline := &Baseline{
		Schema: baselineSchema,
		Entry: []Entry{{
			Path: "internal/wallet/handler.go", Check: "SA1012", Message: "do not pass a nil Context",
			Count: 1, Owner: "P23-T05", Reason: "owned by the task that fixes contexts",
		}},
	}
	accepted := []Finding{{Path: "internal/wallet/handler.go", Line: 12, Check: "SA1012", Message: "do not pass a nil Context"}}
	if violations := baselineRefusal(groupFindings(accepted), baseline); len(violations) > 0 {
		t.Errorf("the baseline refused a finding it accepts: %v", violations)
	}

	cases := []struct {
		name     string
		findings []Finding
		expect   string
	}{
		{
			name:     "a finding nobody accepted",
			findings: append(accepted, Finding{Path: "internal/wallet/send.go", Line: 3, Check: "SA5000", Message: "assignment to nil map"}),
			expect:   "not in the baseline",
		},
		{
			name: "the same finding twice where the baseline accepts one",
			findings: append(accepted,
				Finding{Path: "internal/wallet/handler.go", Line: 40, Check: "SA1012", Message: "do not pass a nil Context"}),
			expect: "the baseline is a ratchet, and it only shrinks",
		},
		{
			name:     "a baseline entry the tree no longer produces",
			findings: nil,
			expect:   "which the tree no longer produces",
		},
	}
	for _, testCase := range cases {
		violations := baselineRefusal(groupFindings(testCase.findings), baseline)
		if !contains(violations, testCase.expect) {
			t.Errorf("%s: expected a violation mentioning %q, got %v", testCase.name, testCase.expect, violations)
		}
	}
}

// TestTheBaselineDocumentItselfIsJudged proves the gate refuses a baseline that
// cannot be held to anything: an unknown schema, an entry without an owner or a
// reason, a count of zero, and the same identity twice.
func TestTheBaselineDocumentItselfIsJudged(t *testing.T) {
	cases := []struct {
		name     string
		document string
		expect   string
	}{
		{
			name:     "a schema this gate does not read",
			document: `{"schema": 99, "entries": []}`,
			expect:   "this gate reads schema",
		},
		{
			name:     "an entry without an owner",
			document: `{"schema": 1, "entries": [{"path": "a.go", "check": "U1000", "message": "unused", "count": 1, "owner": "", "reason": "because"}]}`,
			expect:   "has no owner",
		},
		{
			name:     "an entry without a reason",
			document: `{"schema": 1, "entries": [{"path": "a.go", "check": "U1000", "message": "unused", "count": 1, "owner": "P23-T04", "reason": "  "}]}`,
			expect:   "has no reason",
		},
		{
			name:     "an entry that accepts nothing",
			document: `{"schema": 1, "entries": [{"path": "a.go", "check": "U1000", "message": "unused", "count": 0, "owner": "P23-T04", "reason": "because"}]}`,
			expect:   "an accepted finding is one or more",
		},
		{
			name: "the same identity twice",
			document: `{"schema": 1, "entries": [
				{"path": "a.go", "check": "U1000", "message": "unused", "count": 1, "owner": "P23-T04", "reason": "because"},
				{"path": "a.go", "check": "U1000", "message": "unused", "count": 1, "owner": "P23-T04", "reason": "because"}]}`,
			expect: "one identity, one entry",
		},
	}
	for _, testCase := range cases {
		path := filepath.Join(t.TempDir(), "baseline.json")
		if err := os.WriteFile(path, []byte(testCase.document), 0o600); err != nil {
			t.Fatalf("write the baseline fixture: %v", err)
		}
		_, violations := loadBaseline(path)
		if !contains(violations, testCase.expect) {
			t.Errorf("%s: expected a violation mentioning %q, got %v", testCase.name, testCase.expect, violations)
		}
	}

	// The control: a well-formed document is read.
	path := filepath.Join(t.TempDir(), "baseline.json")
	if err := os.WriteFile(path, []byte(`{"schema": 1, "entries": [{"path": "a.go", "check": "U1000", "message": "unused", "count": 2, "owner": "P23-T04", "reason": "because"}]}`), 0o600); err != nil {
		t.Fatalf("write the baseline control: %v", err)
	}
	baseline, violations := loadBaseline(path)
	if len(violations) > 0 || baseline == nil {
		t.Fatalf("the gate refused a well-formed baseline: %v", violations)
	}
}

// TestSuppressionPolicy is the task's own requirement made executable: what the
// analyzers were told to skip has to be local, has to name a check, has to
// carry a reason, and has to be written in the vocabulary the pinned analyzer
// reads.
func TestSuppressionPolicy(t *testing.T) {
	sources := map[string]string{
		"accepted.go": `package sample

func call() {
	//lint:ignore SA1012 the explicit nil context is the failure under test
	use(nil)
}

func use(any) {}
`,
		"filewide.go": `package sample

//lint:file-ignore SA1012 this whole file is too hard to fix
func call() {}
`,
		"noreason.go": `package sample

func call() {
	//lint:ignore SA1012
	use(nil)
}

func use(any) {}
`,
		"foreign.go": `package sample

func call() {
	//nolint:staticcheck // the other linter does not read this tree
	use(nil)
}
`,
		"otherlinter.go": `package sample

func call() {
	//nolint:errcheck // a linter this gate does not pin
	use(nil)
}
`,
	}
	root := t.TempDir()
	files := []string{}
	for name, content := range sources {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		files = append(files, name)
	}

	accepted, violations := suppressionRefusal(root, files)
	if len(accepted) != 1 {
		t.Fatalf("the gate accepted %d suppressions, want exactly the one that names a check and a reason: %+v", len(accepted), accepted)
	}
	if accepted[0].Path != "accepted.go" || accepted[0].Check != "SA1012" || accepted[0].Reason == "" {
		t.Errorf("accepted suppression = %+v, want the SA1012 directive of accepted.go with its reason", accepted[0])
	}

	for _, expected := range []struct {
		path string
		rule string
	}{
		{"filewide.go", "a file-wide suppression silences a file"},
		{"noreason.go", "carries no reason"},
		{"foreign.go", "is not the pinned analyzer's vocabulary"},
	} {
		if !contains(violations, expected.rule) {
			t.Errorf("%s: expected a violation mentioning %q, got %v", expected.path, expected.rule, violations)
		}
	}
	if contains(violations, "otherlinter.go") {
		t.Errorf("the gate judged a directive of a linter it does not pin: %v", violations)
	}
}

// TestEveryFamilyHasAnOwnerAndAProof keeps the mapping honest: a family either
// has an analyzer with a check and a fixture directory, or it names the test
// that owns it — and the fixture of an analyzer family has to hold Go code, or
// the refusal the gate measures would be a refusal of nothing.
func TestEveryFamilyHasAnOwnerAndAProof(t *testing.T) {
	if len(families) == 0 {
		t.Fatal("no family is declared: the phase names seven, and an empty table describes nothing")
	}
	for _, entry := range families {
		if entry.Name == "" || entry.Owner == "" || entry.Reason == "" || entry.Target == "" {
			t.Errorf("family %+v is incomplete: every family names its owner, its reason and where the proof lives", entry)
		}
		switch entry.Owner {
		case "vet", "staticcheck":
			if entry.Check == "" && entry.Match == "" {
				t.Errorf("family %s is owned by %s and names neither a check nor a message", entry.Name, entry.Owner)
			}
			// The targets are written from the repository root, like every path
			// the Makefile passes; the test runs inside the package directory.
			names, err := goFilesIn(filepath.Join("..", "..", entry.Target))
			if err != nil {
				t.Errorf("family %s: its fixture %s is not readable: %v", entry.Name, entry.Target, err)
				continue
			}
			if len(names) == 0 {
				t.Errorf("family %s: its fixture %s holds no Go file, so nothing can be refused there", entry.Name, entry.Target)
			}
		default:
			if entry.Proof == "" {
				t.Errorf("family %s is owned by %q and names no test as its proof", entry.Name, entry.Owner)
			}
		}
	}

	// The gap has to be declared: a family the pinned toolchain does not
	// enforce is printed by the gate, and the ADR records it.
	if len(uncoveredFamilies) == 0 {
		t.Error("no uncovered family is declared: the pinned analyzers do not cover every family the phase names, and saying so is part of the gate")
	}
}

// goFilesIn lists the Go files of one directory of the repository.
func goFilesIn(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

// contains reports whether any violation mentions the expected text.
func contains(violations []string, expected string) bool {
	for _, violation := range violations {
		if strings.Contains(violation, expected) {
			return true
		}
	}
	return false
}
