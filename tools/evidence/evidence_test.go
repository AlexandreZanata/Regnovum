package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/testsupport"
)

// The fixtures of the format. Every rule of FormatRules has one here, the
// vocabulary test at the end proves it, and the fixtures that produce sensitive
// content compose it instead of writing it: this repository may not carry a
// literal a secret scanner reads as a live credential, and the point of these
// fixtures is a value exactly like that.

const (
	commitFixture = "4f1c9d2b8a0e5c3f7b6d1a2e9c8b7a6d5e4f3a2b"
	seedFixture   = "20260923"
)

// secretFixture is a payment provider key, assembled from its parts.
func secretFixture() string {
	return "sk_" + "live_" + strings.Repeat("A1b2C3d4", 3)
}

// addressFixture is somebody's business address, outside the reserved domains a
// dataset may use.
const addressFixture = "ana.silva@corp.example-business.com"

// metadataFixture is what every producing call has to carry.
func metadataFixture() Metadata {
	return Metadata{
		Commit: commitFixture,
		Seed:   seedFixture,
		Tools:  map[string]string{"go": "go1.25.0"},
	}
}

// citationsFixture names the rule each package of the stream proves. It stands
// in for the `-cite` flags, which the command reads into the same type.
func citationsFixture() Citations {
	return Citations{
		"example.test/arenas": {"arenas-published-visibility"},
		"example.test/wallet": {"wallet-ledger-append-only"},
	}
}

// streamFixture is a `go test -json` stream of two packages: one that passes
// and one that fails a case, with output on each.
func streamFixture() string {
	events := []string{
		`{"Time":"2026-09-23T10:00:00.000000000Z","Action":"start","Package":"example.test/wallet"}`,
		`{"Time":"2026-09-23T10:00:00.100000000Z","Action":"run","Package":"example.test/wallet","Test":"TestAppendOnly"}`,
		`{"Time":"2026-09-23T10:00:00.110000000Z","Action":"output","Package":"example.test/wallet","Test":"TestAppendOnly","Output":"=== RUN   TestAppendOnly\n"}`,
		`{"Time":"2026-09-23T10:00:00.120000000Z","Action":"output","Package":"example.test/wallet","Test":"TestAppendOnly","Output":"--- PASS: TestAppendOnly (0.02s)\n"}`,
		`{"Time":"2026-09-23T10:00:00.130000000Z","Action":"pass","Package":"example.test/wallet","Test":"TestAppendOnly","Elapsed":0.02}`,
		`{"Time":"2026-09-23T10:00:00.200000000Z","Action":"run","Package":"example.test/wallet","Test":"TestEntryOrder"}`,
		`{"Time":"2026-09-23T10:00:00.210000000Z","Action":"fail","Package":"example.test/wallet","Test":"TestEntryOrder","Elapsed":0.03}`,
		`{"Time":"2026-09-23T10:00:00.300000000Z","Action":"fail","Package":"example.test/wallet","Elapsed":0.31}`,
		`{"Time":"2026-09-23T10:00:01.000000000Z","Action":"start","Package":"example.test/arenas"}`,
		`{"Time":"2026-09-23T10:00:01.100000000Z","Action":"run","Package":"example.test/arenas","Test":"TestDraftIsInvisible"}`,
		`{"Time":"2026-09-23T10:00:01.200000000Z","Action":"pass","Package":"example.test/arenas","Test":"TestDraftIsInvisible","Elapsed":0.01}`,
		`{"Time":"2026-09-23T10:00:01.300000000Z","Action":"pass","Package":"example.test/arenas","Elapsed":0.30}`,
	}
	return strings.Join(events, "\n") + "\n"
}

// scrambledStream is the same run announced in the other order: the packages in
// reverse and, inside a package, the tests in reverse — each case keeping its
// own lines in the order they were printed, because that order is the log and
// nothing may reorder it. The document it becomes has to be the same one, byte
// for byte.
func scrambledStream() string {
	events := []string{
		`{"Time":"2026-09-23T10:00:01.000000000Z","Action":"start","Package":"example.test/arenas"}`,
		`{"Time":"2026-09-23T10:00:01.100000000Z","Action":"run","Package":"example.test/arenas","Test":"TestDraftIsInvisible"}`,
		`{"Time":"2026-09-23T10:00:01.200000000Z","Action":"pass","Package":"example.test/arenas","Test":"TestDraftIsInvisible","Elapsed":0.01}`,
		`{"Time":"2026-09-23T10:00:01.300000000Z","Action":"pass","Package":"example.test/arenas","Elapsed":0.30}`,
		`{"Time":"2026-09-23T10:00:00.000000000Z","Action":"start","Package":"example.test/wallet"}`,
		`{"Time":"2026-09-23T10:00:00.200000000Z","Action":"run","Package":"example.test/wallet","Test":"TestEntryOrder"}`,
		`{"Time":"2026-09-23T10:00:00.210000000Z","Action":"fail","Package":"example.test/wallet","Test":"TestEntryOrder","Elapsed":0.03}`,
		`{"Time":"2026-09-23T10:00:00.100000000Z","Action":"run","Package":"example.test/wallet","Test":"TestAppendOnly"}`,
		`{"Time":"2026-09-23T10:00:00.110000000Z","Action":"output","Package":"example.test/wallet","Test":"TestAppendOnly","Output":"=== RUN   TestAppendOnly\n"}`,
		`{"Time":"2026-09-23T10:00:00.120000000Z","Action":"output","Package":"example.test/wallet","Test":"TestAppendOnly","Output":"--- PASS: TestAppendOnly (0.02s)\n"}`,
		`{"Time":"2026-09-23T10:00:00.130000000Z","Action":"pass","Package":"example.test/wallet","Test":"TestAppendOnly","Elapsed":0.02}`,
		`{"Time":"2026-09-23T10:00:00.300000000Z","Action":"fail","Package":"example.test/wallet","Elapsed":0.31}`,
	}
	return strings.Join(events, "\n") + "\n"
}

// convertFixture reads the stream of the fixture into a document.
func convertFixture(t *testing.T, stream string) Document {
	t.Helper()
	document, err := Convert(strings.NewReader(stream), metadataFixture(), citationsFixture())
	if err != nil {
		t.Fatalf("the fixture stream was refused: %v", err)
	}
	return document
}

// refusalFixture is one way of being refused and the rule that names it. The
// table exists so that the vocabulary of the format is declared in one place
// and a rule that no fixture exercises is red instead of decorative.
type refusalFixture struct {
	name  string
	rule  string
	build func(t *testing.T) error
}

// refusals is every reason one document was refused, as one error. A
// conversion is refused by a single rule; a filed document can break several at
// once, and a fixture is held to exactly the rule it exists for.
type refusals []Refusal

func (r refusals) Error() string {
	details := make([]string, 0, len(r))
	for _, refusal := range r {
		details = append(details, refusal.Error())
	}
	return strings.Join(details, "; ")
}

// rules answers the rules of every refusal.
func (r refusals) rules() []string {
	rules := make([]string, 0, len(r))
	for _, refusal := range r {
		rules = append(rules, refusal.Rule)
	}
	return rules
}

// refusalRules answers every rule an error names.
func refusalRules(err error) []string {
	var single Refusal
	if errors.As(err, &single) {
		return []string{single.Rule}
	}
	var many refusals
	if errors.As(err, &many) {
		return many.rules()
	}
	return nil
}

// refusalFixtures answers every way this format refuses something.
func refusalFixtures() []refusalFixture {
	mutate := func(change func(document *Document)) func(t *testing.T) error {
		return func(t *testing.T) error {
			document := convertFixture(t, streamFixture())
			change(&document)
			violations := Violations(document)
			if len(violations) == 0 {
				return errors.New("the document was accepted")
			}
			return refusals(violations)
		}
	}
	return []refusalFixture{
		{
			name: "a stream cut before the package said how it ended",
			rule: RuleTruncatedStream,
			build: func(t *testing.T) error {
				lines := strings.Split(strings.TrimSpace(streamFixture()), "\n")
				cut := strings.Join(lines[:len(lines)-1], "\n") + "\n"
				_, err := Convert(strings.NewReader(cut), metadataFixture(), citationsFixture())
				return err
			},
		},
		{
			name: "the last test of a package cut before it said how it ended",
			rule: RuleTruncatedStream,
			build: func(t *testing.T) error {
				lines := strings.Split(strings.TrimSpace(streamFixture()), "\n")
				cut := strings.Join(lines[:len(lines)-2], "\n") + "\n"
				_, err := Convert(strings.NewReader(cut), metadataFixture(), citationsFixture())
				return err
			},
		},
		{
			name: "a line that is not an event",
			rule: RuleTruncatedStream,
			build: func(t *testing.T) error {
				_, err := Convert(strings.NewReader("ok  \texample.test/arenas\n"), metadataFixture(), citationsFixture())
				return err
			},
		},
		{
			name: "an action the format does not know",
			rule: RuleUnknownAction,
			build: func(t *testing.T) error {
				stream := streamFixture() +
					`{"Time":"2026-09-23T10:00:01.400000000Z","Action":"bench","Package":"example.test/arenas","Test":"BenchmarkPublish"}` + "\n"
				_, err := Convert(strings.NewReader(stream), metadataFixture(), citationsFixture())
				return err
			},
		},
		{
			name: "output for a test that was never announced",
			rule: RuleTruncatedStream,
			build: func(t *testing.T) error {
				stream := `{"Time":"2026-09-23T10:00:00.100000000Z","Action":"output","Package":"example.test/wallet","Test":"TestAbsent","Output":"x\n"}` + "\n"
				_, err := Convert(strings.NewReader(stream), metadataFixture(), citationsFixture())
				return err
			},
		},
		{
			name: "the result of a test that was never announced",
			rule: RuleTruncatedStream,
			build: func(t *testing.T) error {
				stream := `{"Time":"2026-09-23T10:00:00.100000000Z","Action":"pass","Package":"example.test/wallet","Test":"TestAbsent"}` + "\n"
				_, err := Convert(strings.NewReader(stream), metadataFixture(), citationsFixture())
				return err
			},
		},
		{
			name: "a stream that announces no package",
			rule: RuleTruncatedStream,
			build: func(t *testing.T) error {
				stream := `{"Time":"2026-09-23T10:00:00.000000000Z","Action":"start"}` + "\n"
				_, err := Convert(strings.NewReader(stream), metadataFixture(), citationsFixture())
				return err
			},
		},
		{
			name: "a run that cannot name its commit",
			rule: RuleMissingMetadata,
			build: func(t *testing.T) error {
				values := metadataFixture()
				values.Commit = ""
				_, err := Convert(strings.NewReader(streamFixture()), values, citationsFixture())
				return err
			},
		},
		{
			name: "a run that cannot name the version of its tool",
			rule: RuleMissingMetadata,
			build: func(t *testing.T) error {
				values := metadataFixture()
				values.Tools = map[string]string{"go": "  "}
				_, err := Convert(strings.NewReader(streamFixture()), values, citationsFixture())
				return err
			},
		},
		{
			name: "a declaration that does not say when it ran",
			rule: RuleMissingMetadata,
			build: func(t *testing.T) error {
				_, err := Declare(Declaration{
					Kind: KindAudit, Name: "security-audit", Rules: []string{"provider-secret"}, Status: StatusPass,
				}, metadataFixture())
				return err
			},
		},
		{
			name:  "a filed document whose suite carries no result",
			rule:  RuleTruncatedStream,
			build: mutate(func(document *Document) { document.Suites[0].Status = "" }),
		},
		{
			name:  "a filed document whose case carries no result",
			rule:  RuleTruncatedStream,
			build: mutate(func(document *Document) { document.Suites[0].Cases[0].Status = "" }),
		},
		{
			name:  "a filed document that declares a kind the format does not know",
			rule:  RuleUnknownAction,
			build: mutate(func(document *Document) { document.Suites[0].Kind = "unit" }),
		},
		{
			name: "a filed document whose suites are out of order",
			rule: RuleNonCanonicalOrder,
			build: mutate(func(document *Document) {
				document.Suites[0], document.Suites[1] = document.Suites[1], document.Suites[0]
			}),
		},
		{
			name: "a filed document whose cases are out of order",
			rule: RuleNonCanonicalOrder,
			build: mutate(func(document *Document) {
				// The wallet suite, which the fixture gives two cases so that this
				// one can be written at all.
				suite := &document.Suites[1]
				suite.Cases[0], suite.Cases[1] = suite.Cases[1], suite.Cases[0]
			}),
		},
		{
			name: "a filed document that cites no rule at all",
			rule: RuleMissingMetadata,
			build: mutate(func(document *Document) {
				for index := range document.Suites {
					document.Suites[index].Rules = nil
					for caseIndex := range document.Suites[index].Cases {
						document.Suites[index].Cases[caseIndex].Rules = nil
					}
				}
			}),
		},
		{
			name:  "a filed document of another schema",
			rule:  RuleMissingMetadata,
			build: mutate(func(document *Document) { document.Schema = schema + 1 }),
		},
		{
			name:  "a filed document that carries no instant",
			rule:  RuleMissingMetadata,
			build: mutate(func(document *Document) { document.At = time.Time{} }),
		},
		{
			name: "a filed document that still carries a credential",
			rule: RuleSensitiveContent,
			build: mutate(func(document *Document) {
				document.Suites[0].Cases[0].Output = append(document.Suites[0].Cases[0].Output, "checkout key "+secretFixture()+"\n")
			}),
		},
		{
			name: "a filed document that still carries somebody's address",
			rule: RuleSensitiveContent,
			build: mutate(func(document *Document) {
				document.Suites[0].Cases[0].Output = append(document.Suites[0].Cases[0].Output, "reported by "+addressFixture+"\n")
			}),
		},
	}
}

// TestEveryDeclaredRuleIsExercisedByAFixture is the vocabulary test: it runs
// every fixture, requires each one to be refused by exactly the rule it exists
// for — a fixture that breaks two rules at once proves neither — and requires
// the union to be the rules the format declares. A rule nobody exercises and a
// fixture nobody can name are both red here.
func TestEveryDeclaredRuleIsExercisedByAFixture(t *testing.T) {
	t.Parallel()

	exercised := map[string]bool{}
	for _, fixture := range refusalFixtures() {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			err := fixture.build(t)
			if err == nil {
				t.Fatalf("the fixture was accepted: it exists to be refused by %s", fixture.rule)
			}
			rules := refusalRules(err)
			if len(rules) == 0 {
				t.Fatalf("the fixture was refused without naming a rule: %v", err)
			}
			if len(rules) != 1 || rules[0] != fixture.rule {
				t.Fatalf("the fixture was refused by %v, and it exists for %s: %v", rules, fixture.rule, err)
			}
		})
		exercised[fixture.rule] = true
	}

	for _, rule := range FormatRules() {
		if !exercised[rule] {
			t.Errorf("the rule %s has no fixture: a rule nobody exercises cannot be trusted", rule)
		}
	}
	for rule := range exercised {
		declared := false
		for _, candidate := range FormatRules() {
			if candidate == rule {
				declared = true
			}
		}
		if !declared {
			t.Errorf("the fixture exercises %s, which the format does not declare", rule)
		}
	}
}

// TestAStreamBecomesOneDocument is the shape of the artifact: one suite per
// package, one case per test, the result of each, the citations the caller
// declared, and the output of a case in the order it was printed.
func TestAStreamBecomesOneDocument(t *testing.T) {
	t.Parallel()

	document := convertFixture(t, streamFixture())

	if document.Schema != schema {
		t.Fatalf("the document declares schema %d", document.Schema)
	}
	if document.Commit != commitFixture || document.Seed != seedFixture {
		t.Fatalf("the document does not carry the run it measured: %+v", document)
	}
	if got := strings.Join(document.Rules, ","); got != strings.Join(FormatRules(), ",") {
		t.Fatalf("the document was read against %q", got)
	}
	if document.Totals != (Totals{Suites: 2, Cases: 3, Passed: 2, Failed: 1}) {
		t.Fatalf("the totals of the run are %+v", document.Totals)
	}
	if document.DurationSeconds != 0.61 {
		t.Fatalf("the duration of the run is %v, and its suites took 0.31 and 0.30", document.DurationSeconds)
	}

	want := []string{"example.test/arenas", "example.test/wallet"}
	for index, name := range want {
		if document.Suites[index].Name != name {
			t.Fatalf("suite %d is %s, and the order of the format is %v", index, document.Suites[index].Name, want)
		}
	}

	wallet := document.Suites[1]
	if wallet.Kind != KindGoTest || wallet.Status != StatusFail {
		t.Fatalf("the wallet suite is %s/%s", wallet.Kind, wallet.Status)
	}
	if strings.Join(wallet.Rules, ",") != "wallet-ledger-append-only" {
		t.Fatalf("the wallet suite cites %v", wallet.Rules)
	}
	if len(wallet.Cases) != 2 || wallet.Cases[0].Name != "TestAppendOnly" || wallet.Cases[1].Status != StatusFail {
		t.Fatalf("the cases of the wallet suite are %+v", wallet.Cases)
	}
	if wallet.Cases[0].DurationSeconds != 0.02 || wallet.DurationSeconds != 0.31 {
		t.Fatalf("the durations were lost: case %v suite %v", wallet.Cases[0].DurationSeconds, wallet.DurationSeconds)
	}
	printed := strings.Join(wallet.Cases[0].Output, "")
	if !strings.HasPrefix(printed, "=== RUN   TestAppendOnly") {
		t.Fatalf("the output of the case lost its order: %q", printed)
	}
	if document.Suites[0].Status != StatusPass || document.Suites[0].Rules[0] != "arenas-published-visibility" {
		t.Fatalf("the arenas suite is %+v", document.Suites[0])
	}
}

// TestTheOrderIsTheFormatsNotTheInputs is the determinism property stated as
// the phase asks it: the same run, announced in another order, produces the
// same artifact — byte for byte. The output of a case is the one thing that
// keeps its own order, because that is the log.
func TestTheOrderIsTheFormatsNotTheInputs(t *testing.T) {
	t.Parallel()

	first, err := convertFixture(t, streamFixture()).JSON()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	second, err := convertFixture(t, scrambledStream()).JSON()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("two announcements of the same run disagree:\n%s\n%s", first, second)
	}

	// And the artifact is the same document when it is read back and written
	// again: a reader that loses a field would say so here.
	document, err := ParseDocument(first)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	again, err := document.JSON()
	if err != nil {
		t.Fatalf("render again: %v", err)
	}
	if !bytes.Equal(first, again) {
		t.Fatalf("the artifact does not survive being read:\n%s\n%s", first, again)
	}
}

// TestALongPackageKeepsEveryResult is the fixture for a defect this parser had
// and only the real toolchain found: a package with enough cases for the slice
// of cases to grow past its first array. A parser that kept a pointer into that
// slice wrote the result of every case announced after the growth into the
// array that was left behind, and then reported a passing suite as tests that
// ended without a result. No fixture of two tests can see that, and the stream
// of one real package has 239.
func TestALongPackageKeepsEveryResult(t *testing.T) {
	t.Parallel()

	const cases = 64
	var stream strings.Builder
	// Every case is announced first, and the results arrive afterwards, which is
	// what the toolchain does with a test that pauses and lets the others run:
	// the announcement and the result are separated by the growth of the slice.
	for index := 0; index < cases; index++ {
		fmt.Fprintf(&stream, `{"Action":"run","Package":"example.test/long","Test":%q}`+"\n", fmt.Sprintf("TestCase%02d", index))
	}
	for index := 0; index < cases; index++ {
		name := fmt.Sprintf("TestCase%02d", index)
		fmt.Fprintf(&stream, `{"Action":"output","Package":"example.test/long","Test":%q,"Output":"=== RUN   %s\n"}`+"\n", name, name)
		fmt.Fprintf(&stream, `{"Action":"pass","Package":"example.test/long","Test":%q,"Elapsed":0.001}`+"\n", name)
	}
	fmt.Fprintf(&stream, `{"Action":"pass","Package":"example.test/long","Elapsed":0.5}`+"\n")

	document, err := Convert(strings.NewReader(stream.String()), metadataFixture(), Citations{"example.test/long": {"platform-long-suite"}})
	if err != nil {
		t.Fatalf("a long suite was refused: %v", err)
	}
	if document.Totals.Cases != cases || document.Totals.Passed != cases || document.Totals.Failed != 0 {
		t.Fatalf("the results of a long suite were lost: %+v", document.Totals)
	}
	for _, entry := range document.Suites[0].Cases {
		if entry.Status != StatusPass {
			t.Fatalf("the case %s came out as %q", entry.Name, entry.Status)
		}
		if len(entry.Output) != 1 {
			t.Fatalf("the case %s lost its output: %v", entry.Name, entry.Output)
		}
	}
}

// TestTheRedactionRemovesWhatTheDetectorKnowsAndTheGateProvesIt covers the two
// halves of one rule: the conversion leaves no value the platform's detector
// knows in the artifact, counts what it replaced by rule, and the gate over the
// filed artifact passes — and refuses the same document the moment a value is
// put back.
func TestTheRedactionRemovesWhatTheDetectorKnowsAndTheGateProvesIt(t *testing.T) {
	t.Parallel()

	stream := streamFixture()
	stream = strings.Replace(stream,
		`"Output":"--- PASS: TestAppendOnly (0.02s)\n"`,
		`"Output":"checkout key `+secretFixture()+` for `+addressFixture+`\n"`, 1)

	document, err := Convert(strings.NewReader(stream), metadataFixture(), citationsFixture())
	if err != nil {
		t.Fatalf("the fixture stream was refused: %v", err)
	}

	printed := strings.Join(document.Suites[1].Cases[0].Output, "")
	if strings.Contains(printed, secretFixture()) || strings.Contains(printed, addressFixture) {
		t.Fatalf("the artifact carries what it found: %q", printed)
	}
	if !strings.Contains(printed, "[REDACTED:provider-secret]") || !strings.Contains(printed, "[REDACTED:real-email-domain]") {
		t.Fatalf("the artifact does not name what it replaced: %q", printed)
	}

	counted := map[string]int{}
	for _, entry := range document.Redactions {
		counted[entry.Rule] = entry.Count
	}
	if counted["provider-secret"] != 1 || counted["real-email-domain"] != 1 || len(counted) != 2 {
		t.Fatalf("the redaction was counted as %+v", document.Redactions)
	}
	if redacted(document) != 2 {
		t.Fatalf("the run reports %d redactions", redacted(document))
	}
	if violations := Violations(document); len(violations) != 0 {
		t.Fatalf("the artifact of a run that printed a credential was refused: %+v", violations)
	}

	// The control: the same document, with one value back where it was.
	document.Suites[1].Cases[0].Output = []string{"checkout key " + secretFixture() + "\n"}
	violations := Violations(document)
	if len(violations) == 0 || violations[0].Rule != RuleSensitiveContent {
		t.Fatalf("the gate accepted a document that carries a credential: %+v", violations)
	}
	if strings.Contains(violations[0].Detail, secretFixture()) {
		t.Fatalf("the refusal republishes what it found: %q", violations[0].Detail)
	}
}

// TestTheOtherThreeProducersDeclareTheSameDocument is the part of the format
// that makes it one format: an audit, the load run and the browser journeys do
// not emit `go test -json`, so they declare themselves, and what comes out is
// the same artifact as a Go suite.
func TestTheOtherThreeProducersDeclareTheSameDocument(t *testing.T) {
	t.Parallel()

	declarations := map[string]string{
		KindAudit: `{
  "kind": "audit",
  "name": "security-audit",
  "rules": ["provider-secret", "webhook-secret"],
  "status": "fail",
  "duration_seconds": 12.5,
  "at": "2026-09-23T10:00:00Z",
  "output": ["2 findings\n"],
  "cases": [
    {"name": "provider-secret", "status": "pass", "duration_seconds": 1.5, "rules": ["provider-secret"]},
    {"name": "webhook-secret", "status": "fail", "duration_seconds": 2.0, "rules": ["webhook-secret"],
     "output": ["the webhook secret is shared between two environments\n"]}
  ]
}`,
		KindLoad: `{
  "kind": "load",
  "name": "capacity-smoke",
  "rules": ["capacity-p95"],
  "status": "pass",
  "duration_seconds": 300.0,
  "at": "2026-09-23T10:00:00Z"
}`,
		KindE2E: `{
  "kind": "e2e",
  "name": "account-journey",
  "rules": ["account-confirmation"],
  "status": "pass",
  "duration_seconds": 41.0,
  "at": "2026-09-23T10:00:00Z",
  "cases": [
    {"name": "specs/account.spec.js::confirms the address", "status": "pass", "duration_seconds": 4.0}
  ]
}`,
	}

	for kind, payload := range declarations {
		declaration, err := ParseDeclaration([]byte(payload))
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		document, err := Declare(declaration, metadataFixture())
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if len(document.Suites) != 1 || document.Suites[0].Kind != kind {
			t.Fatalf("%s produced %+v", kind, document.Suites)
		}
		if violations := Violations(document); len(violations) != 0 {
			t.Fatalf("%s produced a document the gate refuses: %+v", kind, violations)
		}
		rendered, err := document.JSON()
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		// The artifact of a declaration is read back by the same reader that
		// reads the artifact of a Go suite: one format, not four.
		if _, err := ParseDocument(rendered); err != nil {
			t.Fatalf("%s: the artifact is not readable: %v", kind, err)
		}
		// A suite that has no case of its own carries an empty list, never a
		// missing one: a reader that special-cases `null` is a reader that was
		// made to.
		if kind == KindLoad && !strings.Contains(string(rendered), `"cases": []`) {
			t.Fatalf("a declaration without cases renders as %s", rendered)
		}
		report, err := document.JUnit()
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if !strings.Contains(string(report), `kind="`+kind+`"`) {
			t.Fatalf("%s: the JUnit artifact does not name the kind:\n%s", kind, report)
		}
	}

	// A suite that has no case of its own — an audit with one verdict — is
	// still one case in JUnit, because an empty testsuite reads as a green run
	// that measured nothing.
	declaration, err := ParseDeclaration([]byte(declarations[KindLoad]))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	document, err := Declare(declaration, metadataFixture())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	report, err := document.JUnit()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !strings.Contains(string(report), `tests="1"`) || !strings.Contains(string(report), `name="capacity-smoke"`) {
		t.Fatalf("the load artifact renders as no test at all:\n%s", report)
	}
}

// TestADeclarationIsReadStrictly is the other half of "nothing incomplete is
// published": a declaration with a field this format does not know is refused
// instead of silently dropped, because a reader that drops a field reports on a
// document nobody wrote.
func TestADeclarationIsReadStrictly(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"an unknown field":  `{"kind":"audit","name":"a","rules":["r"],"status":"pass","duration_seconds":0,"at":"2026-09-23T10:00:00Z","thresholds":{"p95":1}}`,
		"an unknown kind":   `{"kind":"message","name":"a","rules":["r"],"status":"pass","duration_seconds":0,"at":"2026-09-23T10:00:00Z"}`,
		"an unknown result": `{"kind":"audit","name":"a","rules":["r"],"status":"ok","duration_seconds":0,"at":"2026-09-23T10:00:00Z"}`,
		"no name":           `{"kind":"audit","name":"","rules":["r"],"status":"pass","duration_seconds":0,"at":"2026-09-23T10:00:00Z"}`,
		"two documents":     `{"kind":"audit","name":"a","rules":["r"],"status":"pass","duration_seconds":0,"at":"2026-09-23T10:00:00Z"}{}`,
		"not JSON":          `audit: ok`,
	}
	for name, payload := range cases {
		payload := payload
		t.Run(name, func(t *testing.T) {
			declaration, err := ParseDeclaration([]byte(payload))
			if err != nil {
				var refusal Refusal
				if !errors.As(err, &refusal) {
					t.Fatalf("the declaration was refused without a rule: %v", err)
				}
				return
			}
			if _, err := Declare(declaration, metadataFixture()); err == nil {
				t.Fatal("the declaration was accepted")
			}
		})
	}
}

// TestTheCommandIsTheGateItPromises drives the command the way a caller does:
// a truncated stream writes nothing and is refused by rule, a good artifact
// passes, and an artifact that carries a credential is refused with the values
// named and not republished.
func TestTheCommandIsTheGateItPromises(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	streamPath := filepath.Join(directory, "run.jsonl")
	documentPath := filepath.Join(directory, "evidence.json")
	junitPath := filepath.Join(directory, "evidence.xml")

	if err := os.WriteFile(streamPath, []byte(streamFixture()), 0o644); err != nil {
		t.Fatalf("write the stream: %v", err)
	}

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"convert",
		"-in", streamPath, "-out", documentPath, "-junit", junitPath,
		"-commit", commitFixture, "-seed", seedFixture, "-tool", "go=go1.25.0",
		"-cite", "example.test/wallet=wallet-ledger-append-only",
		"-cite", "example.test/arenas=arenas-published-visibility",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("convert refused a good stream: %v (%s)", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "2 suites, 3 cases, 1 failed") {
		t.Fatalf("convert did not report the run: %s", stderr.String())
	}

	// The JUnit artifact and the document are the same run: the failing case is
	// a failure in both.
	report, err := os.ReadFile(junitPath)
	if err != nil {
		t.Fatalf("read the JUnit artifact: %v", err)
	}
	if !strings.Contains(string(report), "<failure") || !strings.Contains(string(report), `failures="1"`) {
		t.Fatalf("the JUnit artifact lost the failure:\n%s", report)
	}

	stdout.Reset()
	stderr.Reset()
	if err := run([]string{"check", "-in", documentPath}, &stdout, &stderr); err != nil {
		t.Fatalf("check refused the artifact it wrote: %v (%s)", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "is readable against schema 1") {
		t.Fatalf("check said nothing: %s", stdout.String())
	}

	// A stream that cannot be read as evidence: the artifact is not written.
	stdout.Reset()
	stderr.Reset()
	broken := filepath.Join(directory, "broken.jsonl")
	if err := os.WriteFile(broken, []byte("ok  \texample.test/arenas\n"), 0o644); err != nil {
		t.Fatalf("write the broken stream: %v", err)
	}
	brokenOut := filepath.Join(directory, "broken.json")
	err = run([]string{
		"convert", "-in", broken, "-out", brokenOut,
		"-commit", commitFixture, "-seed", seedFixture, "-tool", "go=go1.25.0",
	}, &stdout, &stderr)
	var refusal Refusal
	if !errors.As(err, &refusal) || refusal.Rule != RuleTruncatedStream {
		t.Fatalf("a stream that is not evidence was accepted: %v", err)
	}
	if _, statErr := os.Stat(brokenOut); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a refused run wrote an artifact anyway")
	}

	// And the gate over an artifact somebody edited afterwards.
	edited := filepath.Join(directory, "edited.json")
	raw, err := os.ReadFile(documentPath)
	if err != nil {
		t.Fatalf("read the artifact: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode the artifact: %v", err)
	}
	suites, ok := decoded["suites"].([]any)
	if !ok || len(suites) == 0 {
		t.Fatalf("the artifact carries no suites: %s", raw)
	}
	wallet := suites[1].(map[string]any)
	cases := wallet["cases"].([]any)
	cases[0].(map[string]any)["output"] = []any{"checkout key " + secretFixture() + "\n"}
	patched, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("render the edited artifact: %v", err)
	}
	if err := os.WriteFile(edited, patched, 0o644); err != nil {
		t.Fatalf("write the edited artifact: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	err = run([]string{"check", "-in", edited}, &stdout, &stderr)
	if err == nil {
		t.Fatal("the gate accepted an artifact that carries a credential")
	}
	if !strings.Contains(stderr.String(), RuleSensitiveContent) {
		t.Fatalf("the gate refused without naming the rule: %s", stderr.String())
	}
	if strings.Contains(stderr.String(), secretFixture()) {
		t.Fatalf("the gate republished what it found: %s", stderr.String())
	}

	// The vocabularies are readable, which is what a reader of a refusal needs.
	stdout.Reset()
	if err := run([]string{"rules"}, &stdout, &stderr); err != nil {
		t.Fatalf("rules: %v", err)
	}
	vocabularies := struct {
		Rules    []string `json:"rules"`
		Detector []string `json:"detector_rules"`
		Kinds    []string `json:"kinds"`
		Statuses []string `json:"statuses"`
	}{}
	if err := json.Unmarshal(stdout.Bytes(), &vocabularies); err != nil {
		t.Fatalf("the vocabularies are not readable: %v (%s)", err, stdout.String())
	}
	if len(vocabularies.Rules) != len(FormatRules()) || len(vocabularies.Detector) != len(DetectorRules()) {
		t.Fatalf("the vocabularies are incomplete: %+v", vocabularies)
	}
}

// TestTheCommandRefusesMisuseSeparately is the boundary of the exit codes: a
// call this command cannot make sense of is not a measurement, so it is not
// reported as a refusal with a rule.
func TestTheCommandRefusesMisuseSeparately(t *testing.T) {
	t.Parallel()

	cases := [][]string{
		{},
		{"unknown"},
		{"convert"},
		{"convert", "-in", "-", "-commit", commitFixture, "-seed", seedFixture, "-tool", "bad"},
		{"convert", "-in", "-", "-commit", commitFixture, "-seed", seedFixture, "-tool", "go=1", "-cite", "no-equals"},
		{"convert", "-in", "-", "positional"},
	}
	for _, args := range cases {
		args := args
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(args, &stdout, &stderr)
			if err == nil {
				t.Fatal("the call was accepted")
			}
			var refusal Refusal
			if errors.As(err, &refusal) {
				t.Fatalf("a misuse was reported as a measurement: %v", err)
			}
			if !errors.Is(err, errUsage) {
				t.Fatalf("the misuse is not a usage error: %v", err)
			}
		})
	}
}

// TestTheArtifactCarriesNoValueTheDetectorKnows is the scan the phase asks for
// as a gate of its own: the bytes written to disk are read back and scanned,
// which is the only place the question can be asked honestly.
func TestTheArtifactCarriesNoValueTheDetectorKnows(t *testing.T) {
	t.Parallel()

	stream := streamFixture()
	stream = strings.Replace(stream,
		`"Output":"--- PASS: TestAppendOnly (0.02s)\n"`,
		`"Output":"authorization: Bearer `+strings.Repeat("T0k3n", 6)+` for `+addressFixture+`\n"`, 1)
	stream = strings.Replace(stream,
		`"Output":"=== RUN   TestAppendOnly\n"`,
		`"Output":"key `+secretFixture()+`\n"`, 1)

	document, err := Convert(strings.NewReader(stream), metadataFixture(), citationsFixture())
	if err != nil {
		t.Fatalf("the fixture stream was refused: %v", err)
	}
	rendered, err := document.JSON()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	report, err := document.JUnit()
	if err != nil {
		t.Fatalf("render JUnit: %v", err)
	}

	for name, artifact := range map[string][]byte{"document": rendered, "junit": report} {
		if findings := testsupport.SensitiveFindings(artifact); len(findings) != 0 {
			t.Fatalf("the %s artifact carries %v", name, findings)
		}
	}

	// The control: the same scan over the stream itself does find the values,
	// so the two artifacts are clean because they were redacted and not because
	// the detector looked away.
	if findings := testsupport.SensitiveFindings([]byte(stream)); len(findings) == 0 {
		t.Fatal("the detector found nothing in the stream that carries three values")
	}
	if len(document.Redactions) != 3 {
		t.Fatalf("the three values were counted as %+v", document.Redactions)
	}
}
