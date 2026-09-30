package main

import (
	"path/filepath"
	"runtime"
	"testing"
)

// fixtureRoot names one gate fixture: the baseline, the schema, the report
// and the probe sources the baseline cites all live under it, so judging
// the fixture judges exactly one family.
func fixtureRoot(family string) string {
	return filepath.Join("testdata", family)
}

func requireFinding(t *testing.T, family, want string, findings []Finding) {
	t.Helper()
	for _, finding := range findings {
		if finding.Rule == want {
			return
		}
	}
	t.Fatalf("%s: no %s finding in %v", family, want, findings)
}

func requireClean(t *testing.T, family string, findings []Finding) {
	t.Helper()
	if len(findings) != 0 {
		t.Fatalf("%s: want no findings, got %v", family, findings)
	}
}

func validateFixture(t *testing.T, family string) []Finding {
	t.Helper()
	root := fixtureRoot(family)
	return Validate(root, "quality/capacity-baseline.json", "quality/capacity-baseline.schema.json", "docs/CAPACITY.md", "linux", "amd64")
}

func TestRatchetAcceptsCleanFixture(t *testing.T) {
	t.Parallel()

	requireClean(t, "clean", validateFixture(t, "clean"))
}

func TestRatchetRefusesLoosenedK6(t *testing.T) {
	t.Parallel()

	requireFinding(t, "loosened-k6", "k6-drift", validateFixture(t, "loosened-k6"))
}

func TestRatchetRefusesTightenedGo(t *testing.T) {
	t.Parallel()

	requireFinding(t, "tightened-go", "budget-drift", validateFixture(t, "tightened-go"))
}

func TestRatchetRefusesUnknownMetric(t *testing.T) {
	t.Parallel()

	requireFinding(t, "unknown-metric", "k6-drift", validateFixture(t, "unknown-metric"))
}

func TestRatchetRefusesUnlistedLive(t *testing.T) {
	t.Parallel()

	requireFinding(t, "unlisted-live", "k6-drift", validateFixture(t, "unlisted-live"))
}

func TestRatchetRefusesMissingSource(t *testing.T) {
	t.Parallel()

	requireFinding(t, "missing-source", "bad-baseline", validateFixture(t, "missing-source"))
}

func TestRatchetRefusesBadHardware(t *testing.T) {
	t.Parallel()

	requireFinding(t, "bad-hardware", "hardware-mismatch", validateFixture(t, "bad-hardware"))
}

func TestRatchetRefusesStaleReport(t *testing.T) {
	t.Parallel()

	requireFinding(t, "stale-report", "stale-report", validateFixture(t, "stale-report"))
}

func TestRatchetRefusesBadSchemaFile(t *testing.T) {
	t.Parallel()

	requireFinding(t, "badschema", "bad-schema-file", validateFixture(t, "badschema"))
}

func TestRatchetRefusesBadBaseline(t *testing.T) {
	t.Parallel()

	requireFinding(t, "bad-baseline", "bad-baseline", validateFixture(t, "bad-baseline"))
}

func TestPlanEvaluatesDurationConstants(t *testing.T) {
	t.Parallel()

	cases := []struct {
		file  string
		name  string
		value int64
	}{
		{"probe/consts.go", "maxProbeBytes", 1024},
		{"probe/consts.go", "MaxProbeLimit", 100},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			found, err := readGoConsts(fixtureRoot("clean"), testCase.file, []string{testCase.name})
			if err != nil {
				t.Fatalf("readGoConsts: %v", err)
			}
			if found[testCase.name] != testCase.value {
				t.Fatalf("%s = %d, want %d", testCase.name, found[testCase.name], testCase.value)
			}
		})
	}
}

func TestRealBaselineIsClean(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not answer")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	findings := Validate(root, BaselinePath, SchemaPath, ReportPath, runtime.GOOS, runtime.GOARCH)
	requireClean(t, "real baseline", findings)
}

func TestSchemaMatchesLoader(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not answer")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	var document struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if err := readJSON(root, SchemaPath, &document); err != nil {
		t.Fatalf("read schema: %v", err)
	}
	required := make(map[string]bool, len(document.Required))
	for _, name := range document.Required {
		required[name] = true
	}
	for _, name := range SchemaKeys {
		if document.Properties[name] == nil {
			t.Errorf("schema promises nothing about %q", name)
		}
		if !required[name] {
			t.Errorf("schema does not require %q", name)
		}
	}
}
