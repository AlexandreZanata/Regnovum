package main

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fixtureRoot names one gate fixture: the catalog, the pack, the schema and
// the probe the pack cites all live under it, so judging the fixture judges
// exactly one family.
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

func probeModules() []string {
	return []string{"internal/probe"}
}

func TestPackAcceptsCleanFixture(t *testing.T) {
	t.Parallel()

	root := fixtureRoot("clean")
	findings := Validate(root, "quality/catalog.json", "quality/regression-pack.json", "quality/regression-pack.schema.json", probeModules())
	requireClean(t, "clean", findings)
}

func TestPackRefusesUncoveredRule(t *testing.T) {
	t.Parallel()

	root := fixtureRoot("uncovered")
	findings := Validate(root, "quality/catalog.json", "quality/regression-pack.json", "quality/regression-pack.schema.json", probeModules())
	requireFinding(t, "uncovered", "uncovered-rule", findings)
}

func TestPackRefusesUnknownRule(t *testing.T) {
	t.Parallel()

	root := fixtureRoot("unknown")
	findings := Validate(root, "quality/catalog.json", "quality/regression-pack.json", "quality/regression-pack.schema.json", probeModules())
	requireFinding(t, "unknown", "unknown-rule", findings)
}

func TestPackRefusesMissingTest(t *testing.T) {
	t.Parallel()

	root := fixtureRoot("missing")
	findings := Validate(root, "quality/catalog.json", "quality/regression-pack.json", "quality/regression-pack.schema.json", probeModules())
	requireFinding(t, "missing", "missing-test", findings)
}

func TestPackRefusesSkippedTest(t *testing.T) {
	t.Parallel()

	root := fixtureRoot("skipped")
	findings := Validate(root, "quality/catalog.json", "quality/regression-pack.json", "quality/regression-pack.schema.json", probeModules())
	requireFinding(t, "skipped", "skipped-test", findings)
}

func TestPackRefusesRetriedTest(t *testing.T) {
	t.Parallel()

	root := fixtureRoot("retried")
	findings := Validate(root, "quality/catalog.json", "quality/regression-pack.json", "quality/regression-pack.schema.json", probeModules())
	requireFinding(t, "retried", "retried-test", findings)
}

func TestPackRefusesSlowPackage(t *testing.T) {
	t.Parallel()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not answer")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	findings := Validate(root,
		"tools/regressionpack/testdata/slowpack/quality/catalog.json",
		"tools/regressionpack/testdata/slowpack/quality/regression-pack.json",
		"tools/regressionpack/testdata/slowpack/quality/regression-pack.schema.json",
		[]string{"internal/contract"})
	requireFinding(t, "slowpack", "slow-package", findings)
}

func TestPackRefusesDefectWithoutTest(t *testing.T) {
	t.Parallel()

	root := fixtureRoot("nodefect")
	findings := Validate(root, "quality/catalog.json", "quality/regression-pack.json", "quality/regression-pack.schema.json", probeModules())
	requireFinding(t, "nodefect", "defect-without-test", findings)
}

func TestPackRefusesBadPack(t *testing.T) {
	t.Parallel()

	root := fixtureRoot("badpack")
	findings := Validate(root, "quality/catalog.json", "quality/regression-pack.json", "quality/regression-pack.schema.json", probeModules())
	requireFinding(t, "badpack", "bad-pack", findings)
}

func TestPackRefusesBadSchemaFile(t *testing.T) {
	t.Parallel()

	root := fixtureRoot("badschema")
	findings := Validate(root, "quality/catalog.json", "quality/regression-pack.json", "quality/regression-pack.schema.json", probeModules())
	requireFinding(t, "badschema", "bad-schema-file", findings)
}

func TestPlanGroupsPackByPackage(t *testing.T) {
	t.Parallel()

	planned := PlanPack(Pack{Rules: []RuleEntry{
		{Rule: "QUAL-FIX-01", Risk: "Q0", Tests: []string{"b/second_test.go::TestB", "a/first_test.go::TestA"}},
		{Rule: "QUAL-FIX-02", Risk: "Q1", Tests: []string{"a/first_test.go::TestA", "a/first_test.go::TestC"}},
	}})
	if len(planned) != 2 {
		t.Fatalf("planned %d package runs, want 2", len(planned))
	}
	if planned[0].Dir != "a" || planned[1].Dir != "b" {
		t.Fatalf("planned order = %q, %q; want deterministic a, b", planned[0].Dir, planned[1].Dir)
	}
	if strings.Join(planned[0].Tests, ",") != "TestA,TestC" {
		t.Fatalf("planned a tests = %q, want deduplicated TestA,TestC", planned[0].Tests)
	}
}

func TestRealPackIsClean(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not answer")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	findings := Validate(root, CatalogPath, PackPath, SchemaPath, RequiredModules)
	requireClean(t, "real pack", findings)
}
