package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The violation codes. Each one says what was refused rather than where the
// file went wrong: an operator reads the code and knows which promise of the
// waiver policy stopped being kept.
const (
	codeWaiversMissing      = "waivers-missing"
	codeWaiversInvalid      = "waivers-invalid"
	codeFieldUnknown        = "field-unknown"
	codeFieldMissing        = "field-missing"
	codeFieldEmpty          = "field-empty"
	codeListEmpty           = "list-empty"
	codeSchemaVersion       = "schema-version"
	codeIDFormat            = "id-format"
	codeIDDuplicate         = "id-duplicate"
	codeRiskInvalid         = "risk-invalid"
	codeCategoryUnknown     = "category-unknown"
	codeCategoryForbidden   = "category-forbidden"
	codeRiskUnderstated     = "risk-understated"
	codeRuleUnknown         = "rule-unknown"
	codeFindingUnknown      = "finding-unknown"
	codeOwnerAddress        = "owner-address"
	codeJustificationShort  = "justification-short"
	codeCompensationUnknown = "compensation-unknown"
	codeDateInvalid         = "date-invalid"
	codeWaiverExpired       = "waiver-expired"
	codeSourceUnknown       = "source-unknown"
)

// SchemaVersion is the only waivers version this loader understands.
const SchemaVersion = 1

// The risk classes, exactly the vocabulary the catalog uses. A waiver is
// judged in the same classes as the rules it suspends: two vocabularies for one
// severity would make "critical" mean two things.
const (
	RiskCritical = "Q0"
	RiskHigh     = "Q1"
	RiskNormal   = "Q2"
)

// Risks is the whole class vocabulary, and the rank of a class is its index
// reversed: a waiver may not be milder than the rule it covers, and "milder"
// has to be an order, not a comparison of two strings.
var Risks = []string{RiskCritical, RiskHigh, RiskNormal}

// Categories is the closed vocabulary a waiver declares its scope in. It is
// closed on purpose: the prohibition below is stated in these words, and a
// scope anybody could invent is a scope the prohibition cannot be checked
// against.
var Categories = []string{
	"authorization",
	"auditability",
	"data-loss",
	"documentation",
	"i18n",
	"ink",
	"money",
	"observability",
	"performance",
	"privacy",
	"security",
	"tooling",
	"webhook",
}

// ForbiddenCategories are the areas the phase prohibits a critical waiver in:
// segurança, privacidade, autorização, dinheiro, INK, webhook, auditabilidade e
// perda de dados. They are forbidden at the critical class and only there — the
// phase prohibits a *critical* waiver in them, and a policy that also forbade
// the mild ones would be a policy nobody could use.
var ForbiddenCategories = []string{
	"authorization",
	"auditability",
	"data-loss",
	"ink",
	"money",
	"privacy",
	"security",
	"webhook",
}

// WaiverFields are the keys every waiver declares. The list is also the
// schema's `required` array, and SchemaAgreement holds the two together.
//
// `categories` and `rules` are this tool's additions to the eight fields the
// phase names: the prohibition is stated in categories, and the rules are what
// ties a waiver to the catalog's own classification instead of to a sentence
// somebody typed.
var WaiverFields = []string{
	"id",
	"finding",
	"risk",
	"categories",
	"rules",
	"owner",
	"justification",
	"compensation",
	"created",
	"expires",
}

// dateLayout is the one date format the document admits. A waiver that expires
// on a date is checkable; one that expires "soon" is not.
const dateLayout = "2006-01-02"

// shortestJustification is the floor the published schema states. It is here
// and not only there because a schema that refuses what the loader admits is a
// schema nobody can satisfy: "because" is not a reason, and a register of
// reasons is the whole point of the file.
const shortestJustification = 20

var idPattern = regexp.MustCompile(`^WVR-[A-Z0-9]+(-[A-Z0-9]+)*$`)

// addressPattern is what a personal address looks like. A waiver is owned by a
// role or a team: an address is a person, and a policy that accepts one puts a
// name in a file that is read by everybody and deleted by nobody.
var addressPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// Waiver is one accepted exception, with everything a reader needs to judge it
// without asking its author.
type Waiver struct {
	ID            string   `json:"id"`
	Finding       string   `json:"finding"`
	Risk          string   `json:"risk"`
	Categories    []string `json:"categories"`
	Rules         []string `json:"rules"`
	Owner         string   `json:"owner"`
	Justification string   `json:"justification"`
	Compensation  []string `json:"compensation"`
	Created       string   `json:"created"`
	Expires       string   `json:"expires"`
}

// Waivers is the whole document. Unlike the catalog, an empty one is the state
// a healthy tree is in: nothing is waived. The two documents refuse opposite
// things, and both refusals are the right one for what they carry.
type Waivers struct {
	SchemaVersion int      `json:"schema_version"`
	Waivers       []Waiver `json:"waivers"`
}

// Violation is one refusal. It carries the waiver id and the code and nothing
// else: the justification, the owner and the compensation stay in the document,
// because a gate that prints them copies what a waiver exists to contain.
type Violation struct {
	Waiver string
	Code   string
	Detail string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s: %s: %s", v.Waiver, v.Code, v.Detail)
}

// Records is what a waiver must resolve against: the catalog's own
// classification of the rules it may cover, the findings the audits recorded,
// and the tree the compensating tests must exist in.
type Records struct {
	Rules    map[string]string // catalog rule id -> risk class
	Findings map[string]string // finding id -> the document that records it
	Root     string
}

// ReadWaivers reads the committed document and judges it against the records.
// A document that is not there is a refusal rather than a silent pass: "no
// waivers" and "no file" are different statements, and only one of them is a
// promise.
func ReadWaivers(root, path string, records Records, today time.Time) (Waivers, []Violation) {
	full, ok := safeJoin(root, path)
	if !ok {
		return Waivers{}, []Violation{{
			Waiver: "document",
			Code:   codeSourceUnknown,
			Detail: fmt.Sprintf("`%s` is not a path inside the checkout the register is judged against", path),
		}}
	}
	document, err := readFile(full)
	if err != nil {
		return Waivers{}, []Violation{{
			Waiver: "document",
			Code:   codeWaiversMissing,
			Detail: fmt.Sprintf("`%s` is not in the checkout: a policy with no register cannot be checked, and an absent register reads like an empty one", path),
		}}
	}
	return Check(document, records, today)
}

// Check is the loader proper: it decodes the document strictly, then judges
// every waiver in it.
func Check(document []byte, records Records, today time.Time) (Waivers, []Violation) {
	var violations []Violation
	header, violations := readHeader(document)
	if len(violations) > 0 {
		return Waivers{}, violations
	}
	waivers := Waivers{SchemaVersion: header.version}
	seen := map[string]int{}
	for index, raw := range header.Waivers {
		waiver, trouble, ok := readWaiver(raw, index, seen)
		violations = append(violations, trouble...)
		if !ok {
			continue
		}
		violations = append(violations, judge(waiver, records, today)...)
		waivers.Waivers = append(waivers.Waivers, waiver)
	}
	sortViolations(violations)
	return waivers, violations
}

// header keeps the entries as raw JSON on purpose: presence and emptiness are
// different refusals, and a decoded struct cannot tell an absent field from an
// empty one. The version is kept raw for the same reason and decoded into
// `version` by readHeader, so that "absent", "not a number" and "a number this
// loader does not understand" stay three different refusals.
type header struct {
	SchemaVersion json.RawMessage   `json:"schema_version"`
	Waivers       []json.RawMessage `json:"waivers"`
	version       int
}

// readHeader decodes the document strictly, refusing an unknown key, a missing
// version and a version this loader does not understand.
func readHeader(document []byte) (header, []Violation) {
	var decoded header
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return header{}, []Violation{{
			Waiver: "document",
			Code:   decodeCode(err),
			Detail: fmt.Sprintf("the document does not decode as a waivers register: %v", err),
		}}
	}
	if decoded.SchemaVersion == nil {
		return header{}, []Violation{{
			Waiver: "document",
			Code:   codeFieldMissing,
			Detail: "`schema_version` is absent: a document that does not say which contract it keeps cannot be judged",
		}}
	}
	var version int
	if err := json.Unmarshal(decoded.SchemaVersion, &version); err != nil {
		return header{}, []Violation{{
			Waiver: "document",
			Code:   codeSchemaVersion,
			Detail: fmt.Sprintf("`schema_version` is not a number: %v", err),
		}}
	}
	if version != SchemaVersion {
		return header{}, []Violation{{
			Waiver: "document",
			Code:   codeSchemaVersion,
			Detail: fmt.Sprintf("`schema_version` is %d and this loader understands %d", version, SchemaVersion),
		}}
	}
	decoded.version = version
	return decoded, nil
}

// decodeCode names a decoding failure. An unknown key is its own refusal,
// because a document written against a newer contract is a different problem
// from one that is not JSON at all — and the first one must not be reported as
// the second one.
func decodeCode(err error) string {
	if strings.Contains(err.Error(), "unknown field") {
		return codeFieldUnknown
	}
	return codeWaiversInvalid
}

// readWaiver decodes one entry strictly and refuses what is absent or blank.
// The third result is false when the entry is too broken to judge: judging half
// a waiver would produce a second, quieter refusal for the same defect.
func readWaiver(raw json.RawMessage, index int, seen map[string]int) (Waiver, []Violation, bool) {
	var waiver Waiver
	var violations []Violation
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&waiver); err != nil {
		return Waiver{}, []Violation{{
			Waiver: fmt.Sprintf("waiver[%d]", index),
			Code:   decodeCode(err),
			Detail: fmt.Sprintf("the entry does not decode as a waiver: %v", err),
		}}, false
	}

	var present map[string]json.RawMessage
	if err := json.Unmarshal(raw, &present); err != nil {
		return Waiver{}, []Violation{{
			Waiver: waiver.ID,
			Code:   codeWaiversInvalid,
			Detail: fmt.Sprintf("the entry does not decode as an object: %v", err),
		}}, false
	}
	name := waiver.ID
	if name == "" {
		name = fmt.Sprintf("waiver[%d]", index)
	}
	refuse := func(code, detail string) {
		violations = append(violations, Violation{Waiver: name, Code: code, Detail: detail})
	}
	for _, field := range WaiverFields {
		if _, ok := present[field]; !ok {
			refuse(codeFieldMissing, fmt.Sprintf("`%s` is absent: every waiver states it or the waiver is incomplete", field))
		}
	}
	for _, text := range []struct{ field, value string }{
		{"id", waiver.ID},
		{"finding", waiver.Finding},
		{"risk", waiver.Risk},
		{"owner", waiver.Owner},
		{"justification", waiver.Justification},
		{"created", waiver.Created},
		{"expires", waiver.Expires},
	} {
		if _, ok := present[text.field]; ok && strings.TrimSpace(text.value) == "" {
			refuse(codeFieldEmpty, fmt.Sprintf("`%s` is blank: an empty required field reads as a filled one to whoever skims it", text.field))
		}
	}
	for _, list := range []struct {
		field string
		items []string
	}{
		{"categories", waiver.Categories},
		{"compensation", waiver.Compensation},
	} {
		if _, ok := present[list.field]; ok && len(list.items) == 0 {
			refuse(codeListEmpty, fmt.Sprintf("`%s` is empty: a waiver that names no %s is a waiver nobody can judge", list.field, list.field))
		}
	}
	if len(violations) > 0 {
		sortViolations(violations)
		return Waiver{}, violations, false
	}
	if !idPattern.MatchString(waiver.ID) {
		refuse(codeIDFormat, fmt.Sprintf("`%s` is not a waiver identifier: the id is what a report, a review and an expiry are keyed by", waiver.ID))
	} else if first, ok := seen[waiver.ID]; ok {
		refuse(codeIDDuplicate, fmt.Sprintf("`%s` is already declared by entry %d: an id that names two waivers names none", waiver.ID, first))
	} else {
		seen[waiver.ID] = index
	}
	if len(violations) > 0 {
		sortViolations(violations)
		return Waiver{}, violations, false
	}
	return waiver, nil, true
}

// judge is the policy: the vocabulary, the owner, the window, the records and
// the prohibition.
func judge(waiver Waiver, records Records, today time.Time) []Violation {
	var violations []Violation
	refuse := func(code, detail string) {
		violations = append(violations, Violation{Waiver: waiver.ID, Code: code, Detail: detail})
	}

	if !contains(Risks, waiver.Risk) {
		refuse(codeRiskInvalid, fmt.Sprintf("`%s` is not a risk class: the vocabulary is %s", waiver.Risk, strings.Join(Risks, ", ")))
	}
	for _, category := range waiver.Categories {
		if !contains(Categories, category) {
			refuse(codeCategoryUnknown, fmt.Sprintf("`%s` is not a category: a scope outside the vocabulary cannot be held to the prohibition that is stated in it", category))
		}
	}
	if waiver.Risk == RiskCritical {
		for _, category := range waiver.Categories {
			if contains(ForbiddenCategories, category) {
				refuse(codeCategoryForbidden, fmt.Sprintf("a critical waiver in `%s` is prohibited: the area is one of the eight the phase refuses to trade, whatever the compensation", category))
			}
		}
	}
	for _, rule := range waiver.Rules {
		class, ok := records.Rules[rule]
		if !ok {
			refuse(codeRuleUnknown, fmt.Sprintf("`%s` is not a rule of the catalog: a waiver of a rule nobody declared waives a rule nobody enforces", rule))
			continue
		}
		if rank(class) > rank(waiver.Risk) {
			refuse(codeRiskUnderstated, fmt.Sprintf("`%s` is declared %s by the catalog and the waiver is declared %s: the class is read from the catalog so that the declaration cannot soften it", rule, class, waiver.Risk))
		}
	}
	if _, ok := records.Findings[waiver.Finding]; !ok {
		refuse(codeFindingUnknown, fmt.Sprintf("`%s` is recorded by no audit: a waiver accepts a finding that exists, and one that accepts a finding nobody wrote accepts nothing", waiver.Finding))
	}
	if len(strings.TrimSpace(waiver.Justification)) < shortestJustification {
		refuse(codeJustificationShort, fmt.Sprintf("`justification` is shorter than %d characters: an exception whose reason does not fit in a sentence is an exception nobody reconsidered", shortestJustification))
	}

	// A blank owner never reaches here — the entry is refused before it is
	// judged — so this is the second half of the rule: the owner exists and is a
	// role, not a person.
	if addressPattern.MatchString(waiver.Owner) {
		refuse(codeOwnerAddress, "`owner` is a personal address: a waiver is owned by a role or a team, and the register is read by everybody")
	}

	created, createdOK := parseDate(waiver.Created)
	expires, expiresOK := parseDate(waiver.Expires)
	if !createdOK {
		refuse(codeDateInvalid, fmt.Sprintf("`%s` is not a date: the format is %s", waiver.Created, dateLayout))
	}
	if !expiresOK {
		refuse(codeDateInvalid, fmt.Sprintf("`%s` is not a date: the format is %s", waiver.Expires, dateLayout))
	}
	if createdOK && expiresOK {
		if expires.Before(created) {
			refuse(codeDateInvalid, fmt.Sprintf("`%s` expires before it was created on `%s`: a window that runs backwards never expires", waiver.Expires, waiver.Created))
		} else if expires.Before(today) {
			refuse(codeWaiverExpired, fmt.Sprintf("`%s` expired on `%s` and today is `%s`: an expired waiver is a finding nobody accepted", waiver.ID, waiver.Expires, today.Format(dateLayout)))
		}
	}
	for _, reference := range waiver.Compensation {
		if !resolves(records.Root, reference) {
			refuse(codeCompensationUnknown, fmt.Sprintf("`%s` does not resolve to a test that exists: a compensation nobody can run is a sentence, not a compensation", reference))
		}
	}
	return violations
}

// parseDate reads the one date format the document admits.
func parseDate(value string) (time.Time, bool) {
	date, err := time.Parse(dateLayout, value)
	if err != nil {
		return time.Time{}, false
	}
	return date, true
}

// rank orders the classes so that "milder than" is a comparison. Q0 is the
// critical class and the highest rank.
func rank(class string) int {
	return len(Risks) - index(Risks, class)
}

// resolves reports whether a `path::Function` reference names a test file of
// the tree and a function that file declares. The reference is the compensation
// itself: a waiver whose compensation is a promise is a finding accepted twice.
func resolves(root, reference string) bool {
	path, name, ok := strings.Cut(reference, "::")
	if !ok || path == "" || name == "" {
		return false
	}
	if !strings.HasSuffix(path, ".go") {
		return false
	}
	full, ok := safeJoin(root, path)
	if !ok {
		return false
	}
	raw, err := readFile(full)
	if err != nil {
		return false
	}
	declared := regexp.MustCompile(`(?m)^func ` + regexp.QuoteMeta(name) + `\(`)
	return declared.Match(raw)
}

func contains(values []string, wanted string) bool {
	return index(values, wanted) >= 0
}

func index(values []string, wanted string) int {
	for position, value := range values {
		if value == wanted {
			return position
		}
	}
	return -1
}

func sortViolations(violations []Violation) {
	sort.Slice(violations, func(one, other int) bool {
		if violations[one].Waiver != violations[other].Waiver {
			return violations[one].Waiver < violations[other].Waiver
		}
		if violations[one].Code != violations[other].Code {
			return violations[one].Code < violations[other].Code
		}
		return violations[one].Detail < violations[other].Detail
	})
}
