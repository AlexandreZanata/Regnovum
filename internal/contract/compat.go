// API compatibility gate (P25-T07).
//
// DiffRawBaseline compares the supported baseline
// (internal/contract/testdata/openapi.baseline.json) with the current
// api/openapi.json and classifies every difference as breaking or
// compatible: removed paths, methods and response statuses break; required
// fields added to requests break; removed schema properties, changed types
// and narrowed enums break; any change to an operation's security breaks;
// added paths, methods, statuses and optional properties, widened enums and
// removed required flags pass. Inert metadata (descriptions, summaries,
// examples, deprecation markers, titles) never breaks.
//
// A breaking change without a version bump is refused as
// unversioned-breaking-change: incompatible evolution requires an explicit
// task carrying a new contract version (and an ADR where the plan demands
// one). The baseline bytes themselves are pinned by hash in compat_test.go,
// so the baseline only moves inside an explicit task that updates both
// together.
package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// BaselinePath is the supported contract snapshot, relative to the
// repository root.
const BaselinePath = "testdata/openapi.baseline.json"

// Breaking-change categories. The names are stable because a finding reads
// as `code: detail`, and the code is what a reviewer looks up.
const (
	CodeRemovedPath         = "removed-path"
	CodeRemovedMethod       = "removed-method"
	CodeRemovedStatus       = "removed-status"
	CodeAddedRequired       = "added-required"
	CodeRemovedProperty     = "removed-property"
	CodeChangedType         = "changed-type"
	CodeNarrowedEnum        = "narrowed-enum"
	CodeChangedSecurity     = "changed-security"
	CodeRemovedScheme       = "removed-security-scheme"
	CodeChangedCombinator   = "changed-combinator"
	CodeChangedConvention   = "changed-convention"
	CodeUnversionedBreaking = "unversioned-breaking-change"
)

// BreakingChange is one incompatible difference between the baseline and
// the current contract.
type BreakingChange struct {
	Code   string
	Detail string
}

func (change BreakingChange) String() string {
	return change.Code + ": " + change.Detail
}

// BaselineSHA256 pins the supported snapshot bytes. The test compares the
// file on disk against this digest, so the baseline only moves inside an
// explicit task that updates both together.
const BaselineSHA256 = "904dd0d68215eced3935368c26a2c781a9ec45fee62e0e64538f1883a196e542"

// CheckBaselinePin verifies the baseline file still matches its pin.
func CheckBaselinePin(root, baselinePath string) error {
	raw, err := os.ReadFile(root + "/" + baselinePath)
	if err != nil {
		return fmt.Errorf("contract: read baseline: %w", err)
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != BaselineSHA256 {
		return fmt.Errorf("contract: baseline %s drifted from its pin: regenerate it only inside an explicit task", baselinePath)
	}
	return nil
}

// canonical renders a decoded JSON value deterministically (encoding/json
// sorts map keys), so structural comparison never depends on key order.
func canonical(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

// decodeDocument decodes raw contract bytes into generic values.
func decodeDocument(raw []byte) (map[string]any, error) {
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	return document, nil
}

// resolveRef follows one local component reference ("#/components/...")
// inside a document decoded as generic values.
func resolveRef(document map[string]any, reference string) (map[string]any, bool) {
	const prefix = "#/"
	if !strings.HasPrefix(reference, prefix) {
		return nil, false
	}
	var current any = document
	for _, segment := range strings.Split(strings.TrimPrefix(reference, prefix), "/") {
		typed, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = typed[segment]
		if !ok {
			return nil, false
		}
	}
	resolved, ok := current.(map[string]any)
	return resolved, ok
}

// resolveSchema follows a local $ref, returning the schema itself when it
// carries no reference.
func resolveSchema(document map[string]any, schema map[string]any) map[string]any {
	reference, _ := schema["$ref"].(string)
	if reference == "" {
		return schema
	}
	if resolved, ok := resolveRef(document, reference); ok {
		return resolved
	}
	return schema
}

// stringList normalizes a JSON string-or-list into a sorted list.
func stringList(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []any:
		var out []string
		for _, item := range typed {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		sort.Strings(out)
		return out
	default:
		return nil
	}
}

// DiffRawBaseline compares baseline bytes against current contract bytes
// and returns every breaking change, sorted for a stable report. An empty
// result means the current contract is backward compatible with the
// baseline.
func DiffRawBaseline(rawBaseline, rawCurrent []byte) []BreakingChange {
	baseline, err := decodeDocument(rawBaseline)
	if err != nil {
		return []BreakingChange{{Code: "baseline-unreadable", Detail: err.Error()}}
	}
	current, err := decodeDocument(rawCurrent)
	if err != nil {
		return []BreakingChange{{Code: "contract-unreadable", Detail: err.Error()}}
	}
	return diffDocuments(baseline, current)
}

// diffDocuments judges the whole pair: paths, component schemas, security
// schemes and the public x-conventions block. The info version travels
// alongside for RequireVersionBump, which judges it.
func diffDocuments(baseline, current map[string]any) []BreakingChange {
	var findings []BreakingChange
	findings = append(findings, diffPaths(objectAt(baseline, "paths"), objectAt(current, "paths"), baseline, current)...)
	findings = append(findings, diffSchemas(
		objectAt(objectAt(baseline, "components"), "schemas"),
		objectAt(objectAt(current, "components"), "schemas"),
		baseline, current, "components.schemas")...)
	findings = append(findings, diffSecuritySchemes(
		objectAt(objectAt(baseline, "components"), "securitySchemes"),
		objectAt(objectAt(current, "components"), "securitySchemes"))...)
	if canonical(baseline["x-conventions"]) != canonical(current["x-conventions"]) {
		findings = append(findings, BreakingChange{Code: CodeChangedConvention, Detail: "x-conventions (pagination, idempotency) changed"})
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].String() < findings[j].String() })
	return findings
}

// objectAt reads a child object, tolerating absence.
func objectAt(document map[string]any, key string) map[string]any {
	child, _ := document[key].(map[string]any)
	if child == nil {
		return map[string]any{}
	}
	return child
}

// isOperation answers whether a path-level field is an HTTP method rather
// than a shared path item (parameters, summary, servers).
func isOperation(field string) bool {
	switch strings.ToUpper(field) {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		return true
	default:
		return false
	}
}

// diffPaths judges every path of the baseline against the current
// document: removed paths and methods break, added ones pass.
func diffPaths(baseline, current map[string]any, baselineDoc, currentDoc map[string]any) []BreakingChange {
	var findings []BreakingChange
	for path, rawBase := range baseline {
		base, ok := rawBase.(map[string]any)
		if !ok {
			continue
		}
		rawHead, ok := current[path]
		if !ok {
			findings = append(findings, BreakingChange{Code: CodeRemovedPath, Detail: path})
			continue
		}
		head, ok := rawHead.(map[string]any)
		if !ok {
			continue
		}
		for field, rawOperation := range base {
			if !isOperation(field) {
				continue
			}
			operation, ok := rawOperation.(map[string]any)
			if !ok {
				continue
			}
			rawCurrent, ok := head[field]
			if !ok {
				findings = append(findings, BreakingChange{Code: CodeRemovedMethod, Detail: strings.ToUpper(field) + " " + path})
				continue
			}
			currentOperation, ok := rawCurrent.(map[string]any)
			if !ok {
				continue
			}
			findings = append(findings, diffOperation(path, field, operation, currentOperation, baselineDoc, currentDoc)...)
		}
	}
	return findings
}

// diffOperation judges one operation present on both sides: parameters,
// request body, responses and security.
func diffOperation(path, method string, baseline, current, baselineDoc, currentDoc map[string]any) []BreakingChange {
	var findings []BreakingChange
	where := strings.ToUpper(method) + " " + path
	findings = append(findings, diffParameters(where, listOf(baseline, "parameters"), listOf(current, "parameters"), baselineDoc, currentDoc)...)
	findings = append(findings, diffRequestBody(where, objectAt(baseline, "requestBody"), objectAt(current, "requestBody"), baselineDoc, currentDoc)...)
	findings = append(findings, diffResponses(where, objectAt(baseline, "responses"), objectAt(current, "responses"), baselineDoc, currentDoc)...)
	if canonical(baseline["security"]) != canonical(current["security"]) {
		findings = append(findings, BreakingChange{Code: CodeChangedSecurity, Detail: where + ": security requirements changed"})
	}
	return findings
}

// listOf reads an array field, tolerating absence.
func listOf(document map[string]any, key string) []any {
	list, _ := document[key].([]any)
	return list
}

// parameterKey identifies one parameter by name and location.
func parameterKey(parameter map[string]any) string {
	name, _ := parameter["name"].(string)
	location, _ := parameter["in"].(string)
	return location + ":" + name
}

// diffParameters judges query, path and header parameters: added required
// parameters break, removed ones pass, and changed schemas follow the
// schema rules.
func diffParameters(where string, baseline, current []any, baselineDoc, currentDoc map[string]any) []BreakingChange {
	var findings []BreakingChange
	byKey := func(list []any) map[string]map[string]any {
		indexed := map[string]map[string]any{}
		for _, raw := range list {
			parameter, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			indexed[parameterKey(parameter)] = parameter
		}
		return indexed
	}
	baseIndexed, headIndexed := byKey(baseline), byKey(current)
	for key, base := range baseIndexed {
		head, ok := headIndexed[key]
		if !ok {
			continue
		}
		baseRequired, _ := base["required"].(bool)
		headRequired, _ := head["required"].(bool)
		if !baseRequired && headRequired {
			findings = append(findings, BreakingChange{Code: CodeAddedRequired, Detail: where + ": parameter " + key + " became required"})
		}
		findings = append(findings, diffSchemaValue(where+" parameter "+key, base["schema"], head["schema"], baselineDoc, currentDoc)...)
	}
	for key, head := range headIndexed {
		if _, ok := baseIndexed[key]; !ok {
			if required, _ := head["required"].(bool); required {
				findings = append(findings, BreakingChange{Code: CodeAddedRequired, Detail: where + ": required parameter " + key + " added"})
			}
		}
	}
	return findings
}

// diffRequestBody judges the request body: a newly required body breaks,
// and its schema follows the schema rules.
func diffRequestBody(where string, baseline, current map[string]any, baselineDoc, currentDoc map[string]any) []BreakingChange {
	var findings []BreakingChange
	if len(baseline) == 0 && len(current) == 0 {
		return findings
	}
	baseRequired, _ := baseline["required"].(bool)
	headRequired, _ := current["required"].(bool)
	if !baseRequired && headRequired && len(current) > 0 {
		findings = append(findings, BreakingChange{Code: CodeAddedRequired, Detail: where + ": request body became required"})
	}
	findings = append(findings, diffMediaTypes(where+" request", objectAt(baseline, "content"), objectAt(current, "content"), baselineDoc, currentDoc)...)
	return findings
}

// diffResponses judges response statuses: removed statuses break, added
// ones pass, and shared ones follow the schema rules.
func diffResponses(where string, baseline, current map[string]any, baselineDoc, currentDoc map[string]any) []BreakingChange {
	var findings []BreakingChange
	for status := range baseline {
		if _, ok := current[status]; !ok {
			findings = append(findings, BreakingChange{Code: CodeRemovedStatus, Detail: where + ": response " + status + " removed"})
		}
	}
	for status, rawHead := range current {
		rawBase, ok := baseline[status]
		if !ok {
			continue
		}
		head, ok := rawHead.(map[string]any)
		if !ok {
			continue
		}
		base, ok := rawBase.(map[string]any)
		if !ok {
			continue
		}
		findings = append(findings, diffMediaTypes(where+" response "+status, objectAt(base, "content"), objectAt(head, "content"), baselineDoc, currentDoc)...)
	}
	return findings
}

// diffMediaTypes judges shared media types by schema; added media types
// pass alongside their status.
func diffMediaTypes(where string, baseline, current map[string]any, baselineDoc, currentDoc map[string]any) []BreakingChange {
	var findings []BreakingChange
	for mediaType, rawHead := range current {
		rawBase, ok := baseline[mediaType]
		if !ok {
			continue
		}
		head, ok := rawHead.(map[string]any)
		if !ok {
			continue
		}
		base, ok := rawBase.(map[string]any)
		if !ok {
			continue
		}
		findings = append(findings, diffSchemaValue(where, base["schema"], head["schema"], baselineDoc, currentDoc)...)
	}
	return findings
}

// diffSchemas judges component schemas by name: removed schemas break,
// added ones pass, shared ones follow the schema rules.
func diffSchemas(baseline, current map[string]any, baselineDoc, currentDoc map[string]any, where string) []BreakingChange {
	var findings []BreakingChange
	for name := range baseline {
		if _, ok := current[name]; !ok {
			findings = append(findings, BreakingChange{Code: CodeRemovedProperty, Detail: where + "." + name + " removed"})
		}
	}
	for name, rawHead := range current {
		rawBase, ok := baseline[name]
		if !ok {
			continue
		}
		base, ok := rawBase.(map[string]any)
		if !ok {
			continue
		}
		head, ok := rawHead.(map[string]any)
		if !ok {
			continue
		}
		findings = append(findings, diffSchemaObject(where+"."+name, resolveSchema(baselineDoc, base), resolveSchema(currentDoc, head), baselineDoc, currentDoc)...)
	}
	return findings
}

// diffSchemaValue judges two schemas that may be absent, references or
// inline definitions.
func diffSchemaValue(where string, baseline, head any, baselineDoc, currentDoc map[string]any) []BreakingChange {
	base, baseOk := baseline.(map[string]any)
	current, headOk := head.(map[string]any)
	if !baseOk && !headOk {
		return nil
	}
	if !baseOk || !headOk {
		return []BreakingChange{{Code: CodeChangedType, Detail: where + ": schema shape changed"}}
	}
	return diffSchemaObject(where, resolveSchema(baselineDoc, base), resolveSchema(currentDoc, current), baselineDoc, currentDoc)
}

// diffSchemaObject judges two resolved schema objects: required additions,
// removed properties, type changes, narrowed enums and combinator changes
// break; optional additions, widened enums and prose never do.
func diffSchemaObject(where string, baseline, head, baselineDoc, currentDoc map[string]any) []BreakingChange {
	var findings []BreakingChange
	findings = append(findings, diffRequiredFields(where, baseline, head)...)
	findings = append(findings, diffPropertySets(where, baseline, head, baselineDoc, currentDoc)...)
	findings = append(findings, diffTypeAndEnum(where, baseline, head)...)
	findings = append(findings, diffItemsAndCombinators(where, baseline, head, baselineDoc, currentDoc)...)
	return findings
}

// diffRequiredFields refuses newly required fields.
func diffRequiredFields(where string, baseline, head map[string]any) []BreakingChange {
	var findings []BreakingChange
	baseRequired := requiredSet(baseline)
	for field := range requiredSet(head) {
		if !baseRequired[field] {
			findings = append(findings, BreakingChange{Code: CodeAddedRequired, Detail: where + ": required field " + field + " added"})
		}
	}
	return findings
}

// diffPropertySets refuses removed properties and judges shared ones
// recursively.
func diffPropertySets(where string, baseline, head, baselineDoc, currentDoc map[string]any) []BreakingChange {
	var findings []BreakingChange
	baseProperties := objectAt(baseline, "properties")
	headProperties := objectAt(head, "properties")
	for name := range baseProperties {
		if _, ok := headProperties[name]; !ok {
			findings = append(findings, BreakingChange{Code: CodeRemovedProperty, Detail: where + "." + name + " removed"})
		}
	}
	for name, rawHead := range headProperties {
		rawBase, ok := baseProperties[name]
		if !ok {
			continue
		}
		headProp, ok := rawHead.(map[string]any)
		if !ok {
			continue
		}
		baseProp, ok := rawBase.(map[string]any)
		if !ok {
			continue
		}
		findings = append(findings, diffSchemaObject(where+"."+name, resolveSchema(baselineDoc, baseProp), resolveSchema(currentDoc, headProp), baselineDoc, currentDoc)...)
	}
	return findings
}

// diffTypeAndEnum refuses type changes and narrowed enumerations.
func diffTypeAndEnum(where string, baseline, head map[string]any) []BreakingChange {
	var findings []BreakingChange
	if normalizeType(baseline["type"]) != normalizeType(head["type"]) && (baseline["type"] != nil || head["type"] != nil) {
		findings = append(findings, BreakingChange{Code: CodeChangedType, Detail: where + ": type changed"})
	}
	findings = append(findings, diffEnum(where, baseline["enum"], head["enum"])...)
	return findings
}

// diffItemsAndCombinators judges array items and structural combinators,
// which have no compatible direction the gate can name.
func diffItemsAndCombinators(where string, baseline, head, baselineDoc, currentDoc map[string]any) []BreakingChange {
	var findings []BreakingChange
	if itemsBase, itemsHead := baseline["items"], head["items"]; itemsBase != nil || itemsHead != nil {
		baseItems, baseOk := itemsBase.(map[string]any)
		headItems, headOk := itemsHead.(map[string]any)
		if !baseOk || !headOk {
			if canonical(itemsBase) != canonical(itemsHead) {
				findings = append(findings, BreakingChange{Code: CodeChangedCombinator, Detail: where + ": array items changed"})
			}
		} else {
			findings = append(findings, diffSchemaObject(where+"[]", resolveSchema(baselineDoc, baseItems), resolveSchema(currentDoc, headItems), baselineDoc, currentDoc)...)
		}
	}
	for _, combinator := range []string{"allOf", "oneOf", "anyOf", "not"} {
		if canonical(baseline[combinator]) != canonical(head[combinator]) {
			findings = append(findings, BreakingChange{Code: CodeChangedCombinator, Detail: where + ": " + combinator + " changed"})
		}
	}
	if _, ok := baseline["additionalProperties"]; ok {
		if canonical(baseline["additionalProperties"]) != canonical(head["additionalProperties"]) {
			if open, _ := head["additionalProperties"].(bool); !open {
				findings = append(findings, BreakingChange{Code: CodeChangedCombinator, Detail: where + ": additional properties restricted"})
			}
		}
	}
	return findings
}

// requiredSet indexes a schema's required list.
func requiredSet(schema map[string]any) map[string]bool {
	set := map[string]bool{}
	required, _ := schema["required"].([]any)
	for _, raw := range required {
		if name, ok := raw.(string); ok {
			set[name] = true
		}
	}
	return set
}

// normalizeType renders a JSON Schema type (a name or, since 3.1, a list)
// in a canonical order.
func normalizeType(value any) string {
	return strings.Join(stringList(value), "|")
}

// diffEnum judges enumerations: removed values break, added ones pass.
func diffEnum(where string, baseline, head any) []BreakingChange {
	if baseline == nil && head == nil {
		return nil
	}
	toSet := func(value any) map[string]bool {
		set := map[string]bool{}
		list, _ := value.([]any)
		for _, item := range list {
			set[canonical(item)] = true
		}
		return set
	}
	baseSet, headSet := toSet(baseline), toSet(head)
	var findings []BreakingChange
	for value := range baseSet {
		if !headSet[value] {
			findings = append(findings, BreakingChange{Code: CodeNarrowedEnum, Detail: where + ": enum value " + value + " removed"})
		}
	}
	return findings
}

// diffSecuritySchemes judges the shared security schemes: removed schemes
// break, added ones pass, and redefined ones break (clients pin behavior,
// not names).
func diffSecuritySchemes(baseline, current map[string]any) []BreakingChange {
	var findings []BreakingChange
	for name, rawBase := range baseline {
		rawHead, ok := current[name]
		if !ok {
			findings = append(findings, BreakingChange{Code: CodeRemovedScheme, Detail: "security scheme " + name + " removed"})
			continue
		}
		if canonical(rawBase) != canonical(rawHead) {
			findings = append(findings, BreakingChange{Code: CodeChangedSecurity, Detail: "security scheme " + name + " redefined"})
		}
	}
	return findings
}

// infoVersion reads an info.version from decoded contract bytes.
func infoVersion(raw []byte) string {
	var document struct {
		Info struct {
			Version string `json:"version"`
		} `json:"info"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return ""
	}
	return document.Info.Version
}

// RequireVersionBump refuses breaking changes that travel without a new
// contract version: incompatible evolution requires an explicit task
// carrying a version bump (and an ADR where the plan demands one).
func RequireVersionBump(rawBaseline, rawCurrent []byte, findings []BreakingChange) []BreakingChange {
	if len(findings) == 0 {
		return nil
	}
	baselineVersion, currentVersion := infoVersion(rawBaseline), infoVersion(rawCurrent)
	if baselineVersion == currentVersion {
		return []BreakingChange{{
			Code:   CodeUnversionedBreaking,
			Detail: fmt.Sprintf("%d breaking change(s) without a version bump (still %s)", len(findings), currentVersion),
		}}
	}
	return nil
}
