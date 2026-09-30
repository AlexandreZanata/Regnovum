package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// The schema-agreement codes. The loader and the schema are two statements of
// the same contract, and this is where they are held together: a document that
// promises something the loader does not enforce is a document that will be
// trusted for a reason that stopped being true.
const (
	codeSchemaMissing    = "schema-missing"
	codeSchemaInvalid    = "schema-invalid"
	codeSchemaFields     = "schema-waiver-fields"
	codeSchemaVocabulary = "schema-waiver-vocabulary"
	codeSchemaStrictness = "schema-strictness"
)

// SchemaPath is where the waivers schema lives, relative to the repository
// root. It is a constant because two callers asking for "the schema" have to
// mean the same file.
const SchemaPath = "quality/waivers.schema.json"

// waiverSchema is the part of the published schema this check reads. The
// vocabulary of a policy is exactly what a reader will trust without reading
// the loader, so it is read as data and compared, never restated by hand.
type waiverSchema struct {
	Required             []string                  `json:"required"`
	AdditionalProperties *bool                     `json:"additionalProperties"`
	Properties           map[string]schemaProperty `json:"properties"`
}

// schemaProperty is one property of the document. The waiver lives behind a
// `$ref`, as it should: the array is the document's shape and the definition is
// the entry's contract, and a reader that stopped at the array would judge an
// empty object and call every vocabulary agreed.
type schemaProperty struct {
	Type  string   `json:"type"`
	Enum  []string `json:"enum"`
	Items struct {
		Ref  string   `json:"$ref"`
		Enum []string `json:"enum"`
	} `json:"items"`
}

type schemaDocument struct {
	Properties  map[string]schemaProperty `json:"properties"`
	Definitions map[string]waiverSchema   `json:"$defs"`
}

// ReadSchema reads the committed schema and judges it against the loader. A
// schema that is not there cannot hold the loader to anything, so its absence
// is a refusal.
func ReadSchema(root string) []Violation {
	raw, err := readFile(filepath.Join(root, filepath.FromSlash(SchemaPath)))
	if err != nil {
		return []Violation{{
			Waiver: "schema",
			Code:   codeSchemaMissing,
			Detail: fmt.Sprintf("`%s` is not in the checkout: a policy with no published schema has no contract to keep", SchemaPath),
		}}
	}
	var schema schemaDocument
	if err := json.Unmarshal(raw, &schema); err != nil {
		return []Violation{{
			Waiver: "schema",
			Code:   codeSchemaInvalid,
			Detail: fmt.Sprintf("`%s` does not decode as a schema: %v", SchemaPath, err),
		}}
	}
	waivers, ok := schema.Properties["waivers"]
	if !ok || waivers.Type != "array" {
		return []Violation{{
			Waiver: "schema",
			Code:   codeSchemaInvalid,
			Detail: fmt.Sprintf("`%s` declares no `waivers` array: the schema does not describe the document it is published for", SchemaPath),
		}}
	}
	name := waivers.Items.Ref[strings.LastIndex(waivers.Items.Ref, "/")+1:]
	waiver, ok := schema.Definitions[name]
	if name == "" || !ok {
		return []Violation{{
			Waiver: "schema",
			Code:   codeSchemaInvalid,
			Detail: fmt.Sprintf("`%s` does not point at a definition for its entries: %q resolves to nothing, so the entry contract is written nowhere", SchemaPath, waivers.Items.Ref),
		}}
	}

	var violations []Violation
	if !sameSet(sorted(waiver.Required), sorted(WaiverFields)) {
		violations = append(violations, Violation{
			Waiver: "schema",
			Code:   codeSchemaFields,
			Detail: fmt.Sprintf("the schema requires %v and the loader demands %v: a field one of them does not know is a field one of them will not enforce", sorted(waiver.Required), sorted(WaiverFields)),
		})
	}
	if waiver.AdditionalProperties == nil || *waiver.AdditionalProperties {
		violations = append(violations, Violation{
			Waiver: "schema",
			Code:   codeSchemaStrictness,
			Detail: "the schema does not set `additionalProperties: false` on a waiver: a document that admits keys the loader refuses is a document that will be written with them",
		})
	}
	if got := sorted(enumOf(waiver, "risk")); !sameSet(got, sorted(Risks)) {
		violations = append(violations, Violation{
			Waiver: "schema",
			Code:   codeSchemaVocabulary,
			Detail: fmt.Sprintf("the schema admits risk classes %v and the loader judges against %v: a class the schema admits and the loader does not know is a waiver nobody can write", got, sorted(Risks)),
		})
	}
	if got := sorted(enumOf(waiver, "categories")); !sameSet(got, sorted(Categories)) {
		violations = append(violations, Violation{
			Waiver: "schema",
			Code:   codeSchemaVocabulary,
			Detail: fmt.Sprintf("the schema admits categories %v and the loader judges against %v: the prohibition is stated in these words, so the two lists have to be the same list", got, sorted(Categories)),
		})
	}
	sortViolations(violations)
	return violations
}

// enumOf reads the enum of one array-typed property of a waiver, or nil when
// the property declares none.
func enumOf(waiver waiverSchema, name string) []string {
	property, ok := waiver.Properties[name]
	if !ok {
		return nil
	}
	if len(property.Enum) > 0 {
		return property.Enum
	}
	// A list of categories states its vocabulary on its items: the values are
	// the entries of the array, not the array itself.
	return property.Items.Enum
}

func sorted(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

// sameSet compares two sorted lists as sets: the order of a vocabulary is not a
// promise, and requiring one would make the schema refusable for a reason the
// policy never stated.
func sameSet(one, other []string) bool {
	if len(one) != len(other) {
		return false
	}
	for position := range one {
		if one[position] != other[position] {
			return false
		}
	}
	return true
}
