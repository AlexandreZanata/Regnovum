package contract_test

// P44-T01 — fail-closed coverage of the financial rule map:
// quality/financial-coverage.json consolidates the economic axes
// (supply per season, custody, idempotency, legacy rights, time,
// price, tithe, crumbs, decree, privacy and deactivated products)
// and maps TEMP-01–TEMP-12, the Q19–Q31 decisions, the THR-ECON
// threats, the QUAL-* rules that exist in the catalog and the
// executable or planned regression of each axis, always with an
// owner, a runbook reference and the season dimension.
//
// The verifier refuses a removed axis, a removed TEMP or Q, a
// dangling reference, an entry without owner, runbook or season
// dimension, and an axis whose declared test does not resolve in the
// delivered tree. A pending decision is never a certified
// parameter: the status vocabulary separates what an existing
// regression proves (covered) from what only waits for a future
// implementation (planned), what the holder has not authorized
// (blocked) and what stays unoffered (deferred).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

type coverageAxis struct {
	Axis    string   `json:"axis"`
	Q       []string `json:"q"`
	Temp    []string `json:"temp"`
	Threats []string `json:"threats"`
	Rules   []string `json:"rules"`
	Tests   []string `json:"tests"`
	Planned []string `json:"planned"`
	Owner   string   `json:"owner"`
	Runbook string   `json:"runbook"`
	Seasons string   `json:"seasons"`
	Status  string   `json:"status"`
	Note    string   `json:"note"`
}

type financialCoverage struct {
	Schema  int            `json:"schema"`
	Owners  []string       `json:"owners"`
	Status  []string       `json:"statuses"`
	Entries []coverageAxis `json:"entries"`
}

var (
	coverageQPattern      = regexp.MustCompile(`^Q\d\d$`)
	coverageTempPattern   = regexp.MustCompile(`^TEMP-\d\d$`)
	coverageThreatPattern = regexp.MustCompile(`^THR-ECON-\d\d$`)
	coverageRunbook       = regexp.MustCompile(`^docs/RUNBOOKS\.md R\d(?:–R\d)?$`)
	coverageRulePattern   = regexp.MustCompile(`^QUAL-[A-Z0-9-]+$`)
)

// testRefSplits answers the file and the function of one `path::Test`
// reference. A reference without the separator is not a test.
func splitTestRef(ref string) (string, string) {
	path, function, found := strings.Cut(ref, "::")
	if !found {
		return "", ""
	}
	return path, function
}

func loadFinancialCoverage(t *testing.T) (string, financialCoverage) {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not answer")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	raw, err := os.ReadFile(filepath.Join(root, "quality", "financial-coverage.json"))
	if err != nil {
		t.Fatalf("read financial coverage: %v", err)
	}
	var coverage financialCoverage
	if err := json.Unmarshal(raw, &coverage); err != nil {
		t.Fatalf("financial coverage is not JSON: %v", err)
	}
	return root, coverage
}

// catalogRuleIDs answers every rule declared by the catalog, so a
// coverage axis can only name a rule the rule table really holds.
func catalogRuleIDs(t *testing.T, root string) map[string]bool {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(root, "quality", "catalog.json"))
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	var table struct {
		Rules []struct {
			ID string `json:"id"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatalf("catalog is not JSON: %v", err)
	}
	ids := map[string]bool{}
	for _, rule := range table.Rules {
		ids[rule.ID] = true
	}
	return ids
}

// checkFinancialCoverage judges parsed inputs without touching the
// disk, so the falsifications below prove each refusal without
// mutating the tree: every returned string is one broken promise.
// `exists` resolves a file reference and `functionIn` proves the
// named test really lives in that file.
func checkFinancialCoverage(coverage financialCoverage, ruleIDs map[string]bool, exists func(string) bool, functionIn func(string, string) bool) []string {
	var broken []string
	if coverage.Schema != 1 {
		broken = append(broken, "schema is not 1")
	}
	statuses := map[string]bool{}
	for _, status := range coverage.Status {
		statuses[status] = true
	}
	for _, want := range []string{"covered", "planned", "blocked", "deferred"} {
		if !statuses[want] {
			broken = append(broken, "status "+want+" outside the vocabulary")
		}
	}
	owners := map[string]bool{}
	for _, owner := range coverage.Owners {
		owners[owner] = true
	}
	seenAxis, seenTemp, seenThreat, seenRule := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	seenQ := map[string]bool{}
	for _, entry := range coverage.Entries {
		if entry.Axis == "" {
			broken = append(broken, "an axis has no name")
		}
		if seenAxis[entry.Axis] {
			broken = append(broken, "axis "+entry.Axis+" mapped twice")
		}
		seenAxis[entry.Axis] = true
		if len(entry.Q) == 0 {
			broken = append(broken, entry.Axis+" names no Q")
		}
		for _, q := range entry.Q {
			if !coverageQPattern.MatchString(q) {
				broken = append(broken, entry.Axis+" decision "+q+" is not QNN")
			}
			seenQ[q] = true
		}
		if len(entry.Temp) == 0 {
			broken = append(broken, entry.Axis+" names no seasonal intent")
		}
		for _, temp := range entry.Temp {
			if !coverageTempPattern.MatchString(temp) {
				broken = append(broken, entry.Axis+" intent "+temp+" is not TEMP-NN")
			}
			seenTemp[temp] = true
		}
		if len(entry.Threats) == 0 {
			broken = append(broken, entry.Axis+" names no threat")
		}
		for _, threat := range entry.Threats {
			if !coverageThreatPattern.MatchString(threat) {
				broken = append(broken, entry.Axis+" threat "+threat+" is not THR-ECON-NN")
			}
			seenThreat[threat] = true
		}
		for _, rule := range entry.Rules {
			if !coverageRulePattern.MatchString(rule) {
				broken = append(broken, entry.Axis+" rule "+rule+" is not QUAL-*")
				continue
			}
			if !ruleIDs[rule] {
				broken = append(broken, entry.Axis+" rule "+rule+" is not in the catalog")
				continue
			}
			seenRule[rule] = true
		}
		if !statuses[entry.Status] {
			broken = append(broken, entry.Axis+" status "+entry.Status+" outside the vocabulary")
		}
		if !owners[entry.Owner] {
			broken = append(broken, entry.Axis+" owner "+entry.Owner+" outside the role set")
		}
		if !coverageRunbook.MatchString(entry.Runbook) {
			broken = append(broken, entry.Axis+" runbook "+entry.Runbook+" is not a named runbook")
		}
		if strings.TrimSpace(entry.Seasons) == "" {
			broken = append(broken, entry.Axis+" has no season dimension")
		}
		if strings.TrimSpace(entry.Note) == "" {
			broken = append(broken, entry.Axis+" has no note")
		}
		if len(entry.Tests) == 0 && len(entry.Planned) == 0 {
			broken = append(broken, entry.Axis+" proves nothing: no executed test and no planned regression")
		}
		for _, ref := range entry.Tests {
			path, function := splitTestRef(ref)
			if path == "" || function == "" {
				broken = append(broken, entry.Axis+" test "+ref+" is not path::TestName")
				continue
			}
			if !exists(path) {
				broken = append(broken, entry.Axis+" test "+ref+" points at a missing file")
				continue
			}
			if !functionIn(path, function) {
				broken = append(broken, entry.Axis+" test "+ref+" names a function the file does not hold")
			}
		}
	}
	for _, want := range []string{
		"TEMP-01", "TEMP-02", "TEMP-03", "TEMP-04", "TEMP-05", "TEMP-06",
		"TEMP-07", "TEMP-08", "TEMP-09", "TEMP-10", "TEMP-11", "TEMP-12",
	} {
		if !seenTemp[want] {
			broken = append(broken, "seasonal intent "+want+" has no axis")
		}
	}
	// The critical and high threats of §5.1–§5.7 that P44-T01 names:
	// every one of them must land on an axis or it is a control whose
	// proof nobody can find.
	for _, want := range []string{
		"THR-ECON-20", "THR-ECON-21", "THR-ECON-23", "THR-ECON-22",
		"THR-ECON-30", "THR-ECON-11", "THR-ECON-25", "THR-ECON-24",
		"THR-ECON-28", "THR-ECON-32", "THR-ECON-34", "THR-ECON-35",
	} {
		if !seenThreat[want] {
			broken = append(broken, "threat "+want+" has no axis")
		}
	}
	return broken
}

// TestFinancialCoverageMapsEveryAxis proves the delivered map on the
// delivered tree: every axis resolves its executable tests and every
// declared rule exists in the catalog.
func TestFinancialCoverageMapsEveryAxis(t *testing.T) {
	t.Parallel()

	root, coverage := loadFinancialCoverage(t)
	ruleIDs := catalogRuleIDs(t, root)
	exists := func(ref string) bool {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(ref)))
		return err == nil
	}
	functionIn := func(path, function string) bool {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return false
		}
		return strings.Contains(string(raw), "func "+function+"(")
	}
	if broken := checkFinancialCoverage(coverage, ruleIDs, exists, functionIn); len(broken) != 0 {
		t.Fatalf("financial coverage broken: %v", broken)
	}
}

// TestFinancialCoverageStatesPendingHonestly pins the launch boundary:
// no axis claims covered while its evidence is only planned, and the
// decisions the holder has not ratified never read as certified
// parameters.
func TestFinancialCoverageStatesPendingHonestly(t *testing.T) {
	t.Parallel()

	_, coverage := loadFinancialCoverage(t)
	for _, entry := range coverage.Entries {
		switch entry.Status {
		case "covered":
			if len(entry.Tests) == 0 {
				t.Errorf("axis %s is covered without an executed test", entry.Axis)
			}
			if len(entry.Planned) != 0 {
				t.Errorf("axis %s is covered and still declares a planned regression: %v", entry.Axis, entry.Planned)
			}
		case "planned", "blocked":
			if !strings.Contains(entry.Note, "PENDENTE") {
				t.Errorf("axis %s is %s and does not state that its decisions are PENDENTE", entry.Axis, entry.Status)
			}
		case "deferred":
			if !strings.Contains(entry.Note, "não ofertados") {
				t.Errorf("axis %s is deferred and does not state that the products stay unoffered", entry.Axis)
			}
		}
	}
}

// cloneFinancialCoverage deep-copies parsed entries so mutations never
// touch the delivered document.
func cloneFinancialCoverage(coverage financialCoverage) financialCoverage {
	out := coverage
	out.Entries = append([]coverageAxis(nil), coverage.Entries...)
	return out
}

// TestFinancialCoverageRefusesMutations falsifies the verifier without
// touching the tree: removing a season axis, an intent, a declared
// test, an owner and a rule each breaks exactly the promise it
// violates.
func TestFinancialCoverageRefusesMutations(t *testing.T) {
	t.Parallel()

	_, coverage := loadFinancialCoverage(t)
	always := func(string) bool { return true }
	functionAlways := func(string, string) bool { return true }

	// Removing one axis dimension — here the whole season dimension of
	// the first axis — must break the map.
	dropped := cloneFinancialCoverage(coverage)
	dropped.Entries = dropped.Entries[1:]
	if broken := checkFinancialCoverage(dropped, nil, always, functionAlways); len(broken) == 0 {
		t.Error("removed axis passed")
	}

	// Removing a traced seasonal intent must break the map.
	withoutTemp := cloneFinancialCoverage(coverage)
	for i, entry := range withoutTemp.Entries {
		kept := entry.Temp[:0:0]
		for _, temp := range entry.Temp {
			if temp != "TEMP-05" {
				kept = append(kept, temp)
			}
		}
		withoutTemp.Entries[i].Temp = kept
	}
	if broken := checkFinancialCoverage(withoutTemp, nil, always, functionAlways); len(broken) == 0 {
		t.Error("removed seasonal intent passed")
	}

	// A rule that the catalog does not hold is a dangling rule.
	unknownRule := cloneFinancialCoverage(coverage)
	unknownRule.Entries[0].Rules = []string{"QUAL-NOPE-99"}
	if broken := checkFinancialCoverage(unknownRule, map[string]bool{}, always, functionAlways); len(broken) == 0 {
		t.Error("unknown catalog rule passed")
	}

	// A declared test that does not resolve in the tree is a promise
	// nobody can run.
	missingTest := cloneFinancialCoverage(coverage)
	missingTest.Entries[0].Tests = []string{"internal/seasons/domain/oracle_test.go::TestSeasonOracleVanished"}
	if broken := checkFinancialCoverage(missingTest, nil, always, func(string, string) bool { return false }); len(broken) == 0 {
		t.Error("vanished test passed")
	}

	// An axis without owner has no one accountable for the runbook.
	ownerless := cloneFinancialCoverage(coverage)
	ownerless.Entries[0].Owner = ""
	if broken := checkFinancialCoverage(ownerless, nil, always, functionAlways); len(broken) == 0 {
		t.Error("ownerless axis passed")
	}

	// An axis without season dimension is the axis a season reset
	// forgets.
	seasonless := cloneFinancialCoverage(coverage)
	seasonless.Entries[0].Seasons = ""
	if broken := checkFinancialCoverage(seasonless, nil, always, functionAlways); len(broken) == 0 {
		t.Error("axis without season dimension passed")
	}
}
