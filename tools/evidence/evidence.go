package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/testsupport"
)

// The rules of the format: one per reason a document is refused. They are the
// vocabulary of a failure and of the gate that reads it, and every one of them
// has a fixture in evidence_test.go.
const (
	// RuleTruncatedStream is a stream that ends before it said how a suite or a
	// case ended, a line that is not an event at all, and a filed document that
	// carries a suite or a case without a result.
	RuleTruncatedStream = "truncated-stream"
	// RuleUnknownAction is an action, a status or a kind this format does not
	// know. Ignoring it would publish a document that silently lacks whatever
	// it meant.
	RuleUnknownAction = "unknown-action"
	// RuleMissingMetadata is a document written without the commit, the seed,
	// the instant, the duration or the version of a tool, and a document whose
	// suites cite no rule at all: evidence that does not name what it measured
	// is a log with a title.
	RuleMissingMetadata = "missing-metadata"
	// RuleSensitiveContent is a value the platform's own detector knows that
	// survived into the document.
	RuleSensitiveContent = "sensitive-content"
	// RuleNonCanonicalOrder is a document whose suites or cases are not in the
	// order of the format, which is what makes a line of one artifact line up
	// with the line of another.
	RuleNonCanonicalOrder = "non-canonical-order"
)

// FormatRules answers every rule of the format, in a fixed order.
func FormatRules() []string {
	return []string{
		RuleTruncatedStream,
		RuleUnknownAction,
		RuleMissingMetadata,
		RuleSensitiveContent,
		RuleNonCanonicalOrder,
	}
}

// DetectorRules answers the rules of the detector the redaction uses: the
// vocabulary of the seeded dataset (P22-T04). One vocabulary decides what
// "somebody's data" is, for the fixtures and for the evidence.
func DetectorRules() []string {
	return testsupport.SensitiveRules()
}

// The kinds of a suite: the closed vocabulary of what produced it. It is
// small on purpose. The kind says how to read the artifact — a Go package has
// cases with Go names, a load run has thresholds, a browser journey has
// journeys, a gate has rule identifiers — and a kind outside it is refused
// instead of guessed. The *purpose* of a suite (unit, property, performance,
// recovery) belongs to the quality registry of P21; this format does not
// invent a second one.
const (
	KindGoTest = "go-test"
	KindAudit  = "audit"
	KindLoad   = "load"
	KindE2E    = "e2e"
)

// Kinds answers the closed vocabulary of suite kinds, in a fixed order.
func Kinds() []string {
	return []string{KindGoTest, KindAudit, KindLoad, KindE2E}
}

// The statuses of a suite and of a case. Empty is not one of them: it is the
// absence this format refuses.
const (
	StatusPass = "pass"
	StatusFail = "fail"
	StatusSkip = "skip"
)

// Statuses answers the closed vocabulary of results, in a fixed order.
func Statuses() []string {
	return []string{StatusPass, StatusFail, StatusSkip}
}

// knownAction is the closed vocabulary of `go test -json`. The two build
// actions are in it because a suite that fails to build is a failing suite, and
// a format that refused them would answer a broken build with a refusal to
// report — the one case where the evidence matters most.
var knownActions = map[string]bool{
	"start":        true,
	"run":          true,
	"pause":        true,
	"cont":         true,
	"pass":         true,
	"fail":         true,
	"skip":         true,
	"output":       true,
	"build-output": true,
	"build-fail":   true,
}

// Refusal is why evidence was refused, named by rule so that a gate can count
// it and a reader can act on it.
type Refusal struct {
	Rule   string `json:"rule"`
	Detail string `json:"detail"`
}

func (r Refusal) Error() string {
	return r.Rule + ": " + r.Detail
}

// refuse builds a refusal.
func refuse(rule, format string, args ...any) Refusal {
	return Refusal{Rule: rule, Detail: fmt.Sprintf(format, args...)}
}

// event is one line of `go test -json`, as the toolchain writes it.
type event struct {
	Time    time.Time `json:"Time"`
	Action  string    `json:"Action"`
	Package string    `json:"Package"`
	Test    string    `json:"Test"`
	Elapsed float64   `json:"Elapsed"`
	Output  string    `json:"Output"`
}

// Metadata is what a document must carry about the run itself. The caller gives
// it because only the caller knows the commit and the versions, and it is
// required: a document without it is a log with a title.
type Metadata struct {
	Commit string            `json:"commit"`
	Seed   string            `json:"seed"`
	Tools  map[string]string `json:"tools"`
}

// Missing answers the metadata that is absent, sorted, so that a refusal names
// everything the run has to add instead of one thing per attempt.
func (m Metadata) Missing() []string {
	missing := []string{}
	if strings.TrimSpace(m.Commit) == "" {
		missing = append(missing, "commit")
	}
	if strings.TrimSpace(m.Seed) == "" {
		missing = append(missing, "seed")
	}
	if len(m.Tools) == 0 {
		missing = append(missing, "tools")
	}
	for name, version := range m.Tools {
		if strings.TrimSpace(name) == "" {
			missing = append(missing, "tool with no name")
			continue
		}
		if strings.TrimSpace(version) == "" {
			missing = append(missing, "version of "+name)
		}
	}
	sort.Strings(missing)
	return missing
}

// Case is one case of one suite.
type Case struct {
	Name            string   `json:"name"`
	Status          string   `json:"status"`
	DurationSeconds float64  `json:"duration_seconds"`
	Rules           []string `json:"rules,omitempty"`
	Output          []string `json:"output,omitempty"`
}

// Suite is one suite of the run: a Go package, a gate, a load run or a set of
// journeys. Everything a reader needs in order to know what happened is here,
// and its shape does not depend on which of the four produced it.
type Suite struct {
	Name            string   `json:"name"`
	Kind            string   `json:"kind"`
	Status          string   `json:"status"`
	DurationSeconds float64  `json:"duration_seconds"`
	Rules           []string `json:"rules"`
	Output          []string `json:"output,omitempty"`
	Cases           []Case   `json:"cases"`
}

// Totals is what the document counted, so a reader does not walk it.
type Totals struct {
	Suites  int `json:"suites"`
	Cases   int `json:"cases"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

// Redaction is one detector rule that fired while the document was written, and
// how many values it replaced. The value is not in the artifact.
type Redaction struct {
	Rule  string `json:"rule"`
	Count int    `json:"count"`
}

// Document is the evidence of one run, whatever produced it.
type Document struct {
	Schema          int               `json:"schema"`
	Commit          string            `json:"commit"`
	Seed            string            `json:"seed"`
	Tools           map[string]string `json:"tools"`
	At              time.Time         `json:"at"`
	DurationSeconds float64           `json:"duration_seconds"`
	Totals          Totals            `json:"totals"`
	Suites          []Suite           `json:"suites"`
	Redactions      []Redaction       `json:"redactions"`
	Rules           []string          `json:"rules"`
}

// schema is the version of this document. It is written into the artifact so
// that a reader of a later format knows which one it is holding.
const schema = 1

// Citations names the rules each suite proves, keyed by the name the producer
// uses: the package path of a Go suite, the suite name of a declaration.
type Citations map[string][]string

// Convert reads a `go test -json` stream and answers the document it becomes,
// with one suite per package and one case per test.
//
// It refuses a stream that ends before a package or a test said how it ended, a
// line that is not an event, an action the format does not know, and a call
// that cannot name the run. It never refuses on the *content* of a line:
// content is redacted, because a suite that printed somebody's address is a
// suite whose evidence has to be filed and made safe.
func Convert(stream io.Reader, metadata Metadata, citations Citations) (Document, error) {
	if missing := metadata.Missing(); len(missing) != 0 {
		return Document{}, refuse(RuleMissingMetadata, "the evidence does not name %s", strings.Join(missing, ", "))
	}

	suites := map[string]*Suite{}
	// open holds where a case that has been announced lives. It is a pointer to
	// the suite and an index, and never a pointer into the suite's slice: the
	// slice grows as the tests of a package are announced, and a pointer taken
	// before a growth writes into the array that was left behind — which is how
	// this parser first reported a passing suite of 239 tests as a test that
	// ended without a result.
	open := map[string]openCase{}
	startedAt := time.Time{}
	line := 0

	reader := bufio.NewReaderSize(stream, 1<<20)
	for {
		raw, err := reader.ReadString('\n')
		if raw == "" {
			if err == io.EOF {
				break
			}
			return Document{}, refuse(RuleTruncatedStream, "the stream cannot be read: %v", err)
		}
		line++
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			if err == io.EOF {
				break
			}
			continue
		}

		var decoded event
		if decodeErr := json.Unmarshal([]byte(trimmed), &decoded); decodeErr != nil {
			return Document{}, refuse(RuleTruncatedStream, "line %d is not an event: %v", line, decodeErr)
		}
		if !knownActions[decoded.Action] {
			return Document{}, refuse(RuleUnknownAction, "line %d carries the action %q, which this format does not know", line, decoded.Action)
		}
		if startedAt.IsZero() || (!decoded.Time.IsZero() && decoded.Time.Before(startedAt)) {
			startedAt = decoded.Time
		}

		if decoded.Package == "" {
			// The module-level events of `-json` are the toolchain speaking: no
			// package is running yet, so they are not a suite and they are
			// counted nowhere.
			if err == io.EOF {
				break
			}
			continue
		}
		entry, exists := suites[decoded.Package]
		if !exists {
			entry = &Suite{Name: decoded.Package, Kind: KindGoTest, Rules: citationFor(citations, decoded.Package)}
			suites[decoded.Package] = entry
		}

		if decoded.Test == "" {
			switch decoded.Action {
			case "output", "build-output":
				entry.Output = append(entry.Output, decoded.Output)
			case "build-fail":
				entry.Status = StatusFail
			case "pass", "fail", "skip":
				entry.Status = decoded.Action
				entry.DurationSeconds = decoded.Elapsed
			case "start", "run", "pause", "cont":
				// A package announces itself and nothing else: the result is
				// what matters, and it comes last.
			}
			if err == io.EOF {
				break
			}
			continue
		}

		key := decoded.Package + "\x00" + decoded.Test
		switch decoded.Action {
		case "output":
			current, exists := open[key]
			if !exists {
				return Document{}, refuse(RuleTruncatedStream, "line %d carries output for %s/%s before it was announced", line, decoded.Package, decoded.Test)
			}
			entry.Cases[current.index].Output = append(entry.Cases[current.index].Output, decoded.Output)
		case "run":
			entry.Cases = append(entry.Cases, Case{Name: decoded.Test})
			open[key] = openCase{suite: entry, index: len(entry.Cases) - 1}
		case "pause", "cont":
			if _, exists := open[key]; !exists {
				return Document{}, refuse(RuleTruncatedStream, "line %d carries %q for %s/%s before it was announced", line, decoded.Action, decoded.Package, decoded.Test)
			}
		case "start", "build-output":
			// A test announcing itself carries no result, and a build line
			// inside a test is the toolchain's, already kept by the suite.
		default: // pass, fail, skip, build-fail
			current, exists := open[key]
			if !exists {
				return Document{}, refuse(RuleTruncatedStream, "line %d carries the result of %s/%s, which was never announced", line, decoded.Package, decoded.Test)
			}
			entry.Cases[current.index].Status = statusOf(decoded.Action)
			entry.Cases[current.index].DurationSeconds = decoded.Elapsed
			delete(open, key)
		}
		if err == io.EOF {
			break
		}
	}

	if len(suites) == 0 {
		return Document{}, refuse(RuleTruncatedStream, "the stream announces no package: it is a build log, not a test result")
	}
	for name, entry := range suites {
		if entry.Status == "" {
			return Document{}, refuse(RuleTruncatedStream, "the suite %s ended without a result", name)
		}
		for index := range entry.Cases {
			if entry.Cases[index].Status == "" {
				return Document{}, refuse(RuleTruncatedStream, "the case %s/%s ended without a result", name, entry.Cases[index].Name)
			}
		}
	}

	document := Document{
		Schema:     schema,
		Commit:     metadata.Commit,
		Seed:       metadata.Seed,
		Tools:      metadata.Tools,
		At:         startedAt.UTC(),
		Suites:     []Suite{},
		Redactions: []Redaction{},
		Rules:      FormatRules(),
	}
	if document.At.IsZero() {
		// A stream with no instants is still a measurement; the artifact says
		// so rather than pretending the run happened at the epoch.
		document.At = time.Unix(0, 0).UTC()
	}
	for _, entry := range suites {
		document.Suites = append(document.Suites, *entry)
	}
	document.canonicalize()
	document.redact()
	document.count()
	return document, nil
}

// openCase is where a case that has been announced lives: the suite it belongs
// to and its position in that suite.
type openCase struct {
	suite *Suite
	index int
}

// statusOf maps a `go test -json` action onto a status.
func statusOf(action string) string {
	switch action {
	case "pass", "fail", "skip":
		return action
	case "build-fail":
		return StatusFail
	default:
		return ""
	}
}

// citationFor answers the rules a suite cites, without aliasing the map the
// caller gave: a document that shares a slice with its caller changes when the
// caller does.
func citationFor(citations Citations, name string) []string {
	ids := citations[name]
	clean := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		clean = append(clean, trimmed)
	}
	sort.Strings(clean)
	return clean
}

// Declaration is what a producer that is not `go test` says about its run: the
// audits, the load run and the browser journeys write this document, and
// `declare` turns it into the same evidence as a Go suite. It exists because
// those three produce text, and a format that only knew how to read `go test
// -json` would leave them with no artifact at all.
type Declaration struct {
	Kind            string   `json:"kind"`
	Name            string   `json:"name"`
	Rules           []string `json:"rules"`
	Status          string   `json:"status"`
	DurationSeconds float64  `json:"duration_seconds"`
	At              string   `json:"at"`
	Output          []string `json:"output,omitempty"`
	Cases           []Case   `json:"cases,omitempty"`
}

// ParseDeclaration reads a declaration, refusing a field this format does not
// know: a producer that declares ten thresholds and is read as eight has no way
// of telling.
func ParseDeclaration(data []byte) (Declaration, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()

	var decoded Declaration
	if err := decoder.Decode(&decoded); err != nil {
		return Declaration{}, refuse(RuleTruncatedStream, "the declaration is not readable: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Declaration{}, refuse(RuleTruncatedStream, "the declaration carries more than one document")
	}
	return decoded, nil
}

// Declare turns a declaration into the evidence of the run it describes.
func Declare(declaration Declaration, metadata Metadata) (Document, error) {
	if missing := metadata.Missing(); len(missing) != 0 {
		return Document{}, refuse(RuleMissingMetadata, "the evidence does not name %s", strings.Join(missing, ", "))
	}
	if strings.TrimSpace(declaration.Name) == "" {
		return Document{}, refuse(RuleMissingMetadata, "the declaration does not name the suite it declares")
	}
	if !knownKind(declaration.Kind) {
		return Document{}, refuse(RuleUnknownAction, "%s declares the kind %q, which this format does not know", declaration.Name, declaration.Kind)
	}
	if !knownStatus(declaration.Status) {
		return Document{}, refuse(RuleUnknownAction, "%s declares the result %q, which this format does not know", declaration.Name, declaration.Status)
	}
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(declaration.At))
	if err != nil {
		return Document{}, refuse(RuleMissingMetadata, "%s does not say when it ran with an RFC 3339 instant: %v", declaration.Name, err)
	}
	if declaration.DurationSeconds < 0 {
		return Document{}, refuse(RuleMissingMetadata, "%s declares a negative duration", declaration.Name)
	}
	for _, entry := range declaration.Cases {
		if !knownStatus(entry.Status) {
			return Document{}, refuse(RuleUnknownAction, "%s declares the case %q with the result %q, which this format does not know", declaration.Name, entry.Name, entry.Status)
		}
	}

	suite := Suite{
		Name:            declaration.Name,
		Kind:            declaration.Kind,
		Status:          declaration.Status,
		DurationSeconds: declaration.DurationSeconds,
		Rules:           citationFor(Citations{declaration.Name: declaration.Rules}, declaration.Name),
		Output:          declaration.Output,
		Cases:           declaration.Cases,
	}
	for index := range suite.Cases {
		suite.Cases[index].Rules = citationFor(Citations{suite.Cases[index].Name: suite.Cases[index].Rules}, suite.Cases[index].Name)
	}

	document := Document{
		Schema:     schema,
		Commit:     metadata.Commit,
		Seed:       metadata.Seed,
		Tools:      metadata.Tools,
		At:         at.UTC(),
		Suites:     []Suite{suite},
		Redactions: []Redaction{},
		Rules:      FormatRules(),
	}
	document.canonicalize()
	document.redact()
	document.count()
	return document, nil
}

// knownKind reports whether a kind is one this format reads.
func knownKind(kind string) bool {
	for _, candidate := range Kinds() {
		if candidate == kind {
			return true
		}
	}
	return false
}

// knownStatus reports whether a status is one this format reads.
func knownStatus(status string) bool {
	for _, candidate := range Statuses() {
		if candidate == status {
			return true
		}
	}
	return false
}

// canonicalize puts the document in the order of the format: suites by name,
// cases by name, citations sorted. The order of a case's output is *not*
// touched: that is the log, and a log sorted is no longer a log.
//
// A suite with no cases of its own carries an empty list and not a missing one:
// JSON renders a nil slice as `null`, and an artifact whose shape depends on
// whether a suite happened to have a case is an artifact a reader has to
// special-case — which is what the first run of a real package showed.
func (d *Document) canonicalize() {
	for index := range d.Suites {
		suite := &d.Suites[index]
		if suite.Cases == nil {
			suite.Cases = []Case{}
		}
		sort.SliceStable(suite.Cases, func(left, right int) bool { return suite.Cases[left].Name < suite.Cases[right].Name })
		for caseIndex := range suite.Cases {
			entry := &suite.Cases[caseIndex]
			sort.Strings(entry.Rules)
		}
		sort.Strings(suite.Rules)
	}
	sort.SliceStable(d.Suites, func(left, right int) bool { return d.Suites[left].Name < d.Suites[right].Name })
}

// redact replaces every value the platform's detector knows, everywhere it can
// appear in the artifact, and counts what it replaced by rule. It reads the
// document as it would be written, so the artifact is safe by construction and
// not by promise.
func (d *Document) redact() {
	counted := map[string]int{}
	replace := func(value string) string {
		redacted, redactions := testsupport.RedactSensitive([]byte(value))
		for _, redaction := range redactions {
			counted[redaction.Rule] += redaction.Count
		}
		return string(redacted)
	}
	replaceAll := func(values []string) []string {
		for index := range values {
			values[index] = replace(values[index])
		}
		return values
	}

	d.Commit = replace(d.Commit)
	d.Seed = replace(d.Seed)
	for name, version := range d.Tools {
		d.Tools[name] = replace(version)
	}
	for index := range d.Suites {
		suite := &d.Suites[index]
		suite.Name = replace(suite.Name)
		suite.Output = replaceAll(suite.Output)
		for caseIndex := range suite.Cases {
			entry := &suite.Cases[caseIndex]
			entry.Name = replace(entry.Name)
			entry.Output = replaceAll(entry.Output)
		}
	}

	d.Redactions = make([]Redaction, 0, len(counted))
	for rule, count := range counted {
		d.Redactions = append(d.Redactions, Redaction{Rule: rule, Count: count})
	}
	sort.Slice(d.Redactions, func(left, right int) bool { return d.Redactions[left].Rule < d.Redactions[right].Rule })
}

// count fills the totals of the document. They count *cases*, the way JUnit
// counts them: a suite that has cases is counted by its cases, and a suite that
// has none — an audit with a single verdict, the load run — is counted as the
// one case the artifacts render it as. Counting the suite as well would report
// a run of three cases as five results, which is the kind of arithmetic nobody
// checks and everybody quotes.
func (d *Document) count() {
	d.Totals = Totals{Suites: len(d.Suites)}
	d.DurationSeconds = 0
	for _, suite := range d.Suites {
		// The duration of the document is the sum of its suites: the work the
		// run did. It is not the wall clock, because the toolchain runs the
		// packages of one call in parallel and a sum that pretended otherwise
		// would be a number nobody measured.
		d.DurationSeconds += suite.DurationSeconds
		if len(suite.Cases) == 0 {
			d.countResult(suite.Status)
		}
		for _, entry := range suite.Cases {
			d.countResult(entry.Status)
		}
	}
}

// countResult adds one result to the totals.
func (d *Document) countResult(status string) {
	d.Totals.Cases++
	switch status {
	case StatusPass:
		d.Totals.Passed++
	case StatusFail:
		d.Totals.Failed++
	case StatusSkip:
		d.Totals.Skipped++
	}
}

// JSON renders the document as it is filed: indented, with the fields in the
// order the struct declares, so that two artifacts of the same run differ only
// where the run differed.
func (d Document) JSON() ([]byte, error) {
	encoded, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// ParseDocument reads a filed document, refusing a field this reader does not
// know — a reader that silently drops a field is a reader that reports on a
// document nobody wrote.
func ParseDocument(data []byte) (Document, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()

	var decoded Document
	if err := decoder.Decode(&decoded); err != nil {
		return Document{}, refuse(RuleTruncatedStream, "the document is not readable: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Document{}, refuse(RuleTruncatedStream, "the artifact carries more than one document")
	}
	return decoded, nil
}

// junit is the shape of the XML artifact. CI systems read JUnit and not this
// document, and two artifacts that disagree are worse than one: both come from
// the same document.
type junit struct {
	XMLName  xml.Name     `xml:"testsuites"`
	Name     string       `xml:"name,attr"`
	Tests    int          `xml:"tests,attr"`
	Failures int          `xml:"failures,attr"`
	Skipped  int          `xml:"skipped,attr"`
	Time     string       `xml:"time,attr"`
	Commit   string       `xml:"commit,attr"`
	Seed     string       `xml:"seed,attr"`
	Suites   []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name     string      `xml:"name,attr"`
	Kind     string      `xml:"kind,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Skipped  int         `xml:"skipped,attr"`
	Time     string      `xml:"time,attr"`
	Cases    []junitCase `xml:"testcase"`
	System   string      `xml:"system-out,omitempty"`
}

type junitCase struct {
	Name string `xml:"name,attr"`
	// Classname is the suite, which is what a JUnit reader groups by, and the
	// kind is its own attribute: a reader that filed every case under the kind
	// would report one class called "go-test" for the whole repository.
	Classname string        `xml:"classname,attr"`
	Kind      string        `xml:"kind,attr,omitempty"`
	Time      string        `xml:"time,attr"`
	Rules     string        `xml:"rules,attr,omitempty"`
	Failure   *junitFailure `xml:"failure,omitempty"`
	Skipped   *junitSkipped `xml:"skipped,omitempty"`
	Output    string        `xml:"system-out,omitempty"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

type junitSkipped struct {
	Message string `xml:"message,attr,omitempty"`
}

// JUnit renders the document as JUnit XML, from the same document and in the
// same order: a CI reader and a human reader of the JSON disagree about
// nothing. A suite that has cases is rendered as the cases; a suite that has
// none — an audit with a single verdict, a load run with one measurement — is
// rendered as one case named after itself, because a JUnit reader that saw an
// empty testsuite would report a green run that measured nothing.
func (d Document) JUnit() ([]byte, error) {
	report := junit{
		Name:     "goyim-arena",
		Tests:    d.Totals.Cases,
		Failures: d.Totals.Failed,
		Skipped:  d.Totals.Skipped,
		Time:     seconds(d.DurationSeconds),
		Commit:   d.Commit,
		Seed:     d.Seed,
		Suites:   []junitSuite{},
	}
	for _, suite := range d.Suites {
		element := junitSuite{Name: suite.Name, Kind: suite.Kind, Time: seconds(suite.DurationSeconds), Cases: []junitCase{}}
		element.System = strings.Join(suite.Output, "")
		cases := suite.Cases
		if len(cases) == 0 {
			cases = []Case{{
				Name:            suite.Name,
				Status:          suite.Status,
				DurationSeconds: suite.DurationSeconds,
				Output:          suite.Output,
			}}
		}
		for _, entry := range cases {
			element.Tests++
			test := junitCase{
				Name:      entry.Name,
				Classname: suite.Name,
				Kind:      suite.Kind,
				Time:      seconds(entry.DurationSeconds),
				Rules:     strings.Join(entry.Rules, ","),
				Output:    strings.Join(entry.Output, ""),
			}
			switch entry.Status {
			case StatusFail:
				element.Failures++
				test.Failure = &junitFailure{Message: firstLine(entry.Output), Body: strings.Join(entry.Output, "")}
			case StatusSkip:
				element.Skipped++
				test.Skipped = &junitSkipped{Message: firstLine(entry.Output)}
			}
			element.Cases = append(element.Cases, test)
		}
		report.Suites = append(report.Suites, element)
	}

	body, err := xml.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), append(body, '\n')...), nil
}

// firstLine answers the first non-empty line of some output, which is what a
// JUnit reader shows as the message of a failure.
func firstLine(output []string) string {
	for _, line := range output {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// seconds renders a duration the way JUnit does.
func seconds(value float64) string {
	return fmt.Sprintf("%.3f", value)
}

// Violations answers what is wrong with a filed document: what only a reader of
// the artifact can ask, including the redaction gate, which scans the document
// as it would be published and not the memory of a converter.
func Violations(document Document) []Refusal {
	violations := []Refusal{}

	if missing := (Metadata{Commit: document.Commit, Seed: document.Seed, Tools: document.Tools}).Missing(); len(missing) != 0 {
		violations = append(violations, refuse(RuleMissingMetadata, "the document does not name %s", strings.Join(missing, ", ")))
	}
	if document.Schema != schema {
		violations = append(violations, refuse(RuleMissingMetadata, "the document declares schema %d, and this reader knows %d", document.Schema, schema))
	}
	if document.At.IsZero() {
		violations = append(violations, refuse(RuleMissingMetadata, "the document carries no instant"))
	}
	if document.DurationSeconds < 0 {
		violations = append(violations, refuse(RuleMissingMetadata, "the document carries the duration %.3f", document.DurationSeconds))
	}
	if len(document.Suites) == 0 {
		violations = append(violations, refuse(RuleTruncatedStream, "the document carries no suite"))
	}
	if len(document.Rules) == 0 {
		violations = append(violations, refuse(RuleMissingMetadata, "the document does not say which rules of the format it was read against"))
	}

	cited := 0
	for index, suite := range document.Suites {
		if index > 0 && document.Suites[index-1].Name > suite.Name {
			violations = append(violations, refuse(RuleNonCanonicalOrder, "the suite %s appears after %s", suite.Name, document.Suites[index-1].Name))
		}
		if !knownKind(suite.Kind) {
			violations = append(violations, refuse(RuleUnknownAction, "the suite %s declares the kind %q, which this format does not know", suite.Name, suite.Kind))
		}
		if !knownStatus(suite.Status) {
			violations = append(violations, refuse(RuleTruncatedStream, "the suite %s carries the result %q, which is not one of the format's", suite.Name, suite.Status))
		}
		cited += len(suite.Rules)
		for position, entry := range suite.Cases {
			if position > 0 && suite.Cases[position-1].Name > entry.Name {
				violations = append(violations, refuse(RuleNonCanonicalOrder, "the case %s/%s appears after %s", suite.Name, entry.Name, suite.Cases[position-1].Name))
			}
			if !knownStatus(entry.Status) {
				violations = append(violations, refuse(RuleTruncatedStream, "the case %s/%s carries the result %q, which is not one of the format's", suite.Name, entry.Name, entry.Status))
			}
			cited += len(entry.Rules)
		}
	}
	if cited == 0 {
		violations = append(violations, refuse(RuleMissingMetadata, "no suite cites a rule: the artifact proves nothing"))
	}

	// The scan is over the artifact as it would be filed.
	encoded, err := document.JSON()
	if err != nil {
		violations = append(violations, refuse(RuleSensitiveContent, "the document cannot be rendered: %v", err))
		return violations
	}
	seen := map[string]bool{}
	for _, finding := range testsupport.SensitiveFindings(encoded) {
		if seen[finding.Rule] {
			continue
		}
		seen[finding.Rule] = true
		violations = append(violations, refuse(RuleSensitiveContent, "the document carries %s %s", finding.Rule, finding.Evidence))
	}

	sort.SliceStable(violations, func(left, right int) bool {
		if violations[left].Rule != violations[right].Rule {
			return violations[left].Rule < violations[right].Rule
		}
		return violations[left].Detail < violations[right].Detail
	})
	return violations
}
