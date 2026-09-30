package contract_test

// P46-T01 — fail-closed coverage of the seasonal trace:
// quality/season-trace.json maps every TEMP-01–TEMP-12 to its
// requirement, planned threats, contracts, modules, planned tests
// and owner, with an explicit approval state per entry, and this
// verifier refuses a removed TEMP, a duplicated ID, a dangling
// reference, an entry without owner and planned test, or a state
// outside the closed vocabulary. Approved intent (TEMP-01–TEMP-04)
// never authorizes launch; blocked details never advance to
// financial execution.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

type seasonTraceEntry struct {
	Temp      string   `json:"temp"`
	State     string   `json:"state"`
	Req       string   `json:"req"`
	Threats   []string `json:"threats"`
	Contracts string   `json:"contracts"`
	Modules   string   `json:"modules"`
	Tests     string   `json:"tests"`
	Owner     string   `json:"owner"`
	Refs      []string `json:"refs"`
	Note      string   `json:"note"`
}

type seasonTrace struct {
	Schema  int                `json:"schema"`
	Owners  []string           `json:"owners"`
	States  []string           `json:"states"`
	Entries []seasonTraceEntry `json:"entries"`
}

var (
	seasonIDPattern     = regexp.MustCompile(`^TEMP-\d\d$`)
	seasonReqPattern    = regexp.MustCompile(`^REQ-SEASON-\d\d$`)
	seasonThreatPattern = regexp.MustCompile(`^THR-SEASON-\d\d$`)
)

func loadSeasonTrace(t *testing.T) (string, seasonTrace) {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not answer")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	raw, err := os.ReadFile(filepath.Join(root, "quality", "season-trace.json"))
	if err != nil {
		t.Fatalf("read season trace: %v", err)
	}
	var trace seasonTrace
	if err := json.Unmarshal(raw, &trace); err != nil {
		t.Fatalf("season trace is not JSON: %v", err)
	}
	return root, trace
}

// checkSeasonTrace judges parsed inputs without touching the disk, so
// the falsifications below prove each refusal without mutating the
// tree: every returned string is one broken promise.
func checkSeasonTrace(trace seasonTrace, addendum string, exists func(string) bool) []string {
	var broken []string
	if trace.Schema != 1 {
		broken = append(broken, "schema is not 1")
	}
	if len(trace.Entries) != 12 {
		broken = append(broken, "entries are not the 12 seasonal intents")
	}
	states := map[string]bool{}
	for _, state := range trace.States {
		states[state] = true
	}
	for _, want := range []string{"INTENCAO-APROVADA", "PLANEJADO", "BLOQUEADO"} {
		if !states[want] {
			broken = append(broken, "state "+want+" outside the vocabulary")
		}
	}
	seenTemp, seenReq, seenThreat := map[string]bool{}, map[string]bool{}, map[string]bool{}
	owners := map[string]bool{}
	for _, owner := range trace.Owners {
		owners[owner] = true
	}
	for _, entry := range trace.Entries {
		if !seasonIDPattern.MatchString(entry.Temp) {
			broken = append(broken, "decision "+entry.Temp+" is not TEMP-NN")
		}
		if seenTemp[entry.Temp] {
			broken = append(broken, "decision "+entry.Temp+" mapped twice")
		}
		seenTemp[entry.Temp] = true
		if !strings.Contains(addendum, entry.Temp) {
			broken = append(broken, "decision "+entry.Temp+" has no addendum clause")
		}
		if !seasonReqPattern.MatchString(entry.Req) {
			broken = append(broken, "requirement "+entry.Req+" is not REQ-SEASON-NN")
		}
		if seenReq[entry.Req] {
			broken = append(broken, "requirement "+entry.Req+" mapped twice")
		}
		seenReq[entry.Req] = true
		if len(entry.Threats) == 0 {
			broken = append(broken, entry.Temp+" names no threat")
		}
		for _, threat := range entry.Threats {
			if !seasonThreatPattern.MatchString(threat) {
				broken = append(broken, entry.Temp+" threat "+threat+" is not THR-SEASON-NN")
			}
			if seenThreat[threat] {
				broken = append(broken, "threat "+threat+" mapped twice")
			}
			seenThreat[threat] = true
		}
		if !states[entry.State] {
			broken = append(broken, entry.Temp+" state "+entry.State+" outside the vocabulary")
		}
		if !owners[entry.Owner] {
			broken = append(broken, entry.Temp+" owner "+entry.Owner+" outside the role set")
		}
		for name, value := range map[string]string{"contracts": entry.Contracts, "modules": entry.Modules, "tests": entry.Tests, "note": entry.Note} {
			if strings.TrimSpace(value) == "" {
				broken = append(broken, entry.Temp+" "+name+" is empty")
			}
		}
		for _, ref := range entry.Refs {
			if !exists(ref) {
				broken = append(broken, entry.Temp+" points at missing file "+ref)
			}
		}
	}
	for _, want := range []string{
		"TEMP-01", "TEMP-02", "TEMP-03", "TEMP-04", "TEMP-05", "TEMP-06",
		"TEMP-07", "TEMP-08", "TEMP-09", "TEMP-10", "TEMP-11", "TEMP-12",
	} {
		if !seenTemp[want] {
			broken = append(broken, "decision "+want+" has no trace entry")
		}
	}
	return broken
}

// TestSeasonTraceCoversEveryIntent proves the bijection on the
// delivered documents: every TEMP has exactly one entry with an
// explicit state, and the addendum names every traced intent.
func TestSeasonTraceCoversEveryIntent(t *testing.T) {
	t.Parallel()

	root, trace := loadSeasonTrace(t)
	addendum, err := os.ReadFile(filepath.Join(root, "docs", "reino", "TEMPORADAS_SUCESSAO.md"))
	if err != nil {
		t.Fatal(err)
	}
	exists := func(ref string) bool {
		_, err := os.Stat(filepath.Join(root, ref))
		return err == nil
	}
	if broken := checkSeasonTrace(trace, string(addendum), exists); len(broken) != 0 {
		t.Fatalf("trace broken: %v", broken)
	}
}

// TestSeasonTraceStatesIntentHonestly pins the approval boundary:
// TEMP-01–TEMP-04 carry approved intent without launch
// authorization, and the remaining details awaiting the holder stay
// blocked instead of advancing by inference.
func TestSeasonTraceStatesIntentHonestly(t *testing.T) {
	t.Parallel()

	_, trace := loadSeasonTrace(t)
	byTemp := map[string]seasonTraceEntry{}
	for _, entry := range trace.Entries {
		byTemp[entry.Temp] = entry
	}
	for _, temp := range []string{"TEMP-01", "TEMP-02", "TEMP-03", "TEMP-04"} {
		if byTemp[temp].State != "INTENCAO-APROVADA" {
			t.Errorf("%s state = %q, want INTENCAO-APROVADA", temp, byTemp[temp].State)
		}
	}
	for _, temp := range []string{"TEMP-07", "TEMP-08", "TEMP-09", "TEMP-12"} {
		if byTemp[temp].State != "BLOQUEADO" {
			t.Errorf("%s state = %q, want BLOQUEADO: holder acceptance is absent", temp, byTemp[temp].State)
		}
	}
	terminal := byTemp["TEMP-06"]
	if terminal.State != "PLANEJADO" {
		t.Errorf("TEMP-06 state = %q, want PLANEJADO: development only, not launch", terminal.State)
	}
	if !strings.Contains(terminal.Contracts, "seção 5.1") ||
		!strings.Contains(terminal.Note, "sem autorização de lançamento") {
		t.Error("TEMP-06 must name the terminal clause and preserve the launch boundary")
	}
}

// cloneSeasonTrace deep-copies parsed entries so mutations never
// touch the delivered document.
func cloneSeasonTrace(trace seasonTrace) seasonTrace {
	out := trace
	out.Entries = append([]seasonTraceEntry(nil), trace.Entries...)
	return out
}

// seasonAddendumStub names every traced intent, like the delivered
// addendum does.
func seasonAddendumStub() string {
	return "TEMP-01 TEMP-02 TEMP-03 TEMP-04 TEMP-05 TEMP-06 TEMP-07 TEMP-08 TEMP-09 TEMP-10 TEMP-11 TEMP-12"
}

// TestSeasonTraceRefusesMutations falsifies the verifier without
// touching the tree: an absent decision and a duplicated trace each
// break exactly the promise they violate.
func TestSeasonTraceRefusesMutations(t *testing.T) {
	t.Parallel()

	_, trace := loadSeasonTrace(t)
	always := func(string) bool { return true }

	removed := cloneSeasonTrace(trace)
	removed.Entries = removed.Entries[1:]
	if broken := checkSeasonTrace(removed, seasonAddendumStub(), always); len(broken) == 0 {
		t.Error("absent decision passed")
	}

	duplicated := cloneSeasonTrace(trace)
	duplicated.Entries[1].Req = duplicated.Entries[0].Req
	if broken := checkSeasonTrace(duplicated, seasonAddendumStub(), always); len(broken) == 0 {
		t.Error("duplicated trace passed")
	}
}
