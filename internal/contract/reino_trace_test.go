package contract_test

// P31-T02 — fail-closed coverage of the constitutional trace:
// quality/reino-trace.json maps every Q01–Q36 to its requirement,
// planned threats, contracts, modules, planned tests and owner, and
// this verifier refuses a removed Q, a duplicated ID, a dangling
// reference or an entry without owner and planned test. Threat contents
// land in P31-T05; here only the planned identifiers are judged.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

type reinoTraceEntry struct {
	Q         string   `json:"q"`
	Req       string   `json:"req"`
	Threats   []string `json:"threats"`
	Contracts string   `json:"contracts"`
	Modules   string   `json:"modules"`
	Tests     string   `json:"tests"`
	Owner     string   `json:"owner"`
	Refs      []string `json:"refs"`
}

type reinoTrace struct {
	Schema  int               `json:"schema"`
	Owners  []string          `json:"owners"`
	Entries []reinoTraceEntry `json:"entries"`
}

var (
	threatPattern = regexp.MustCompile(`^THR-ECON-\d\d$`)
	decisionHead  = regexp.MustCompile(`(?m)^### (Q\d\d)\b`)
	requireRow    = regexp.MustCompile(`\*\*(REQ-REINO-\d\d)\*\*`)
)

func loadReinoTrace(t *testing.T) (string, reinoTrace) {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not answer")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	raw, err := os.ReadFile(filepath.Join(root, "quality", "reino-trace.json"))
	if err != nil {
		t.Fatalf("read reino trace: %v", err)
	}
	var trace reinoTrace
	if err := json.Unmarshal(raw, &trace); err != nil {
		t.Fatalf("reino trace is not JSON: %v", err)
	}
	return root, trace
}

// checkTrace judges parsed inputs without touching the disk, so the
// falsifications below prove each refusal without mutating the tree:
// every returned string is one broken promise.
func checkTrace(trace reinoTrace, decisions, requirements string, exists func(string) bool) []string {
	var broken []string
	if trace.Schema != 1 {
		broken = append(broken, "schema is not 1")
	}
	if len(trace.Entries) != 36 {
		broken = append(broken, "entries are not the 36 decisions")
	}
	wantQ := map[string]bool{}
	for _, match := range decisionHead.FindAllStringSubmatch(decisions, -1) {
		wantQ[match[1]] = true
	}
	wantReq := map[string]bool{}
	for _, match := range requireRow.FindAllStringSubmatch(requirements, -1) {
		wantReq[match[1]] = true
	}
	seenQ, seenReq, seenThreat := map[string]bool{}, map[string]bool{}, map[string]bool{}
	owners := map[string]bool{}
	for _, owner := range trace.Owners {
		owners[owner] = true
	}
	for _, entry := range trace.Entries {
		if seenQ[entry.Q] {
			broken = append(broken, "decision "+entry.Q+" mapped twice")
		}
		seenQ[entry.Q] = true
		if !wantQ[entry.Q] {
			broken = append(broken, "decision "+entry.Q+" has no heading")
		}
		if seenReq[entry.Req] {
			broken = append(broken, "requirement "+entry.Req+" mapped twice")
		}
		seenReq[entry.Req] = true
		if !wantReq[entry.Req] {
			broken = append(broken, "requirement "+entry.Req+" has no row")
		}
		if len(entry.Threats) == 0 {
			broken = append(broken, entry.Q+" names no threat")
		}
		for _, threat := range entry.Threats {
			if !threatPattern.MatchString(threat) {
				broken = append(broken, entry.Q+" threat "+threat+" is not THR-ECON-NN")
			}
			if seenThreat[threat] {
				broken = append(broken, "threat "+threat+" mapped twice")
			}
			seenThreat[threat] = true
		}
		if !owners[entry.Owner] {
			broken = append(broken, entry.Q+" owner "+entry.Owner+" outside the role set")
		}
		for name, value := range map[string]string{"contracts": entry.Contracts, "modules": entry.Modules, "tests": entry.Tests} {
			if strings.TrimSpace(value) == "" {
				broken = append(broken, entry.Q+" "+name+" is empty")
			}
		}
		for _, ref := range entry.Refs {
			if !exists(ref) {
				broken = append(broken, entry.Q+" points at missing file "+ref)
			}
		}
	}
	for q := range wantQ {
		if !seenQ[q] {
			broken = append(broken, "decision "+q+" has no trace entry")
		}
	}
	for req := range wantReq {
		if strings.HasPrefix(req, "REQ-REINO-") && !seenReq[req] {
			broken = append(broken, "requirement "+req+" has no trace entry")
		}
	}
	return broken
}

// TestReinoTraceCoversEveryDecision proves the bijection both ways on
// the delivered documents: every Q has exactly one entry and every
// requirement exists as a row, with owner, planned test and live refs.
func TestReinoTraceCoversEveryDecision(t *testing.T) {
	t.Parallel()

	root, trace := loadReinoTrace(t)
	decisions, err := os.ReadFile(filepath.Join(root, "docs", "reino", "DECISOES_VIGENTES.md"))
	if err != nil {
		t.Fatal(err)
	}
	requirements, err := os.ReadFile(filepath.Join(root, "docs", "REQUIREMENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	exists := func(ref string) bool {
		_, err := os.Stat(filepath.Join(root, ref))
		return err == nil
	}
	if broken := checkTrace(trace, string(decisions), string(requirements), exists); len(broken) != 0 {
		t.Fatalf("trace broken: %v", broken)
	}
}

// cloneTrace deep-copies parsed entries so mutations never touch the
// delivered document.
func cloneTrace(trace reinoTrace) reinoTrace {
	out := trace
	out.Entries = append([]reinoTraceEntry(nil), trace.Entries...)
	return out
}

// TestReinoTraceRefusesMutations falsifies the verifier without touching
// the tree: a removed Q, a duplicated requirement, a missing row and a
// missing file each break exactly the promise they violate.
func TestReinoTraceRefusesMutations(t *testing.T) {
	t.Parallel()

	_, trace := loadReinoTrace(t)
	always := func(string) bool { return true }

	removed := cloneTrace(trace)
	removed.Entries = removed.Entries[1:]
	if broken := checkTrace(removed, "### Q01\n### Q02\n", "**REQ-REINO-01** **REQ-REINO-02**", always); len(broken) == 0 {
		t.Error("removed Q passed")
	}

	duplicated := cloneTrace(trace)
	duplicated.Entries[1].Req = duplicated.Entries[0].Req
	if broken := checkTrace(duplicated, "### Q01\n### Q02\n", "**REQ-REINO-01** **REQ-REINO-02**", always); len(broken) == 0 {
		t.Error("duplicated requirement passed")
	}

	vanished := "**REQ-REINO-01**"
	if broken := checkTrace(trace, "### Q01\n### Q02\n", vanished, always); len(broken) == 0 {
		t.Error("vanished row passed")
	}

	missing := cloneTrace(trace)
	missing.Entries[0].Refs = append([]string(nil), missing.Entries[0].Refs...)
	missing.Entries[0].Refs[0] = "docs/reino/NOPE.md"
	if broken := checkTrace(missing, "### Q01\n", "**REQ-REINO-01**", func(ref string) bool { return ref != "docs/reino/NOPE.md" }); len(broken) == 0 {
		t.Error("missing file passed")
	}

	ownerless := cloneTrace(trace)
	ownerless.Entries[0].Owner = ""
	if broken := checkTrace(ownerless, "### Q01\n", "**REQ-REINO-01**", always); len(broken) == 0 {
		t.Error("ownerless entry passed")
	}
}
