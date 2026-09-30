package main

import (
	"encoding/json"
	"fmt"
	"os"
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
	codeSchemaFields     = "schema-evidence-fields"
	codeSchemaVocabulary = "schema-evidence-vocabulary"
	codeSchemaStrictness = "schema-strictness"
)

// SchemaPath is where the register schema lives, relative to the repository
// root. It is a constant because two callers asking for "the schema" have to
// mean the same file.
const SchemaPath = "quality/evidence.schema.json"

// schemaProperty is one property of the document or of a definition. A list
// states its vocabulary on its items, and that is what this check compares: the
// values are the entries of the array, not the array itself.
type schemaProperty struct {
	Type                 string                    `json:"type"`
	Enum                 []string                  `json:"enum"`
	AdditionalProperties *bool                     `json:"additionalProperties"`
	Required             []string                  `json:"required"`
	Properties           map[string]schemaProperty `json:"properties"`
	Items                struct {
		Ref  string   `json:"$ref"`
		Enum []string `json:"enum"`
	} `json:"items"`
}

type schemaDocument struct {
	Properties  map[string]schemaProperty `json:"properties"`
	Definitions map[string]schemaProperty `json:"$defs"`
}

// ReadSchema reads the committed schema and judges it against the loader. A
// schema that is not there cannot hold the loader to anything, so its absence
// is a refusal.
func ReadSchema(root string) Violations {
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(SchemaPath)))
	if err != nil {
		return Violations{{
			Row:    "schema",
			Code:   codeSchemaMissing,
			Detail: fmt.Sprintf("`%s` is not in the checkout: a taxonomy with no published schema has no contract to keep", SchemaPath),
		}}
	}
	var schema schemaDocument
	if err := json.Unmarshal(raw, &schema); err != nil {
		return Violations{{
			Row:    "schema",
			Code:   codeSchemaInvalid,
			Detail: fmt.Sprintf("`%s` does not decode as a schema: %v", SchemaPath, err),
		}}
	}

	evidence, trouble := pointed(schema, "evidence")
	if trouble != nil {
		return Violations{*trouble}
	}
	suite, trouble := pointed(schema, "suites")
	if trouble != nil {
		return Violations{*trouble}
	}

	var violations Violations
	if !sameSet(sorted(evidence.Required), sorted(EvidenceRequired)) {
		violations = append(violations, Violation{
			Row:    "schema",
			Code:   codeSchemaFields,
			Detail: fmt.Sprintf("the schema requires %v of an evidence and the loader demands %v: a field one of them does not know is a field one of them will not enforce", sorted(evidence.Required), sorted(EvidenceRequired)),
		})
	}
	if !sameSet(sorted(suite.Required), sorted(SuiteRequired)) {
		violations = append(violations, Violation{
			Row:    "schema",
			Code:   codeSchemaFields,
			Detail: fmt.Sprintf("the schema requires %v of a suite and the loader demands %v", sorted(suite.Required), sorted(SuiteRequired)),
		})
	}
	if evidence.AdditionalProperties == nil || *evidence.AdditionalProperties {
		violations = append(violations, Violation{
			Row:    "schema",
			Code:   codeSchemaStrictness,
			Detail: "the schema does not set `additionalProperties: false` on an evidence row: a document that admits keys the loader refuses is a document that will be written with them",
		})
	}
	if got := sorted(enumOf(evidence, "kind")); !sameSet(got, sorted(Kinds)) {
		violations = append(violations, Violation{
			Row:    "schema",
			Code:   codeSchemaVocabulary,
			Detail: fmt.Sprintf("the schema admits kinds %v and the loader judges against %v: the taxonomy is one list, not two", got, sorted(Kinds)),
		})
	}
	if got := sorted(enumOf(suite, "environment")); !sameSet(got, sorted(Environments)) {
		violations = append(violations, Violation{
			Row:    "schema",
			Code:   codeSchemaVocabulary,
			Detail: fmt.Sprintf("the schema admits environments %v and the loader judges against %v: where a test runs is not a free word", got, sorted(Environments)),
		})
	}
	if !sameSet(sorted(keysOf(suite.Properties)), sorted(SuiteFields)) {
		violations = append(violations, Violation{
			Row:    "schema",
			Code:   codeSchemaFields,
			Detail: fmt.Sprintf("the schema admits %v of a suite and the loader understands %v: the owner is optional for both, and optional has to mean the same thing on both sides", sorted(keysOf(suite.Properties)), sorted(SuiteFields)),
		})
	}
	sortViolations(violations)
	return violations
}

// pointed follows the `$ref` of the array a document property declares, so that
// the definition judged is the one the document actually points at. Reading the
// array alone would judge an empty property and call every vocabulary agreed.
func pointed(schema schemaDocument, property string) (schemaProperty, *Violation) {
	array, ok := schema.Properties[property]
	if !ok || array.Type != "array" {
		return schemaProperty{}, &Violation{
			Row:    "schema",
			Code:   codeSchemaInvalid,
			Detail: fmt.Sprintf("`%s` declares no `%s` array: the schema does not describe the document it is published for", SchemaPath, property),
		}
	}
	name := array.Items.Ref[strings.LastIndex(array.Items.Ref, "/")+1:]
	definition, ok := schema.Definitions[name]
	if name == "" || !ok {
		return schemaProperty{}, &Violation{
			Row:    "schema",
			Code:   codeSchemaInvalid,
			Detail: fmt.Sprintf("`%s`: the `%s` array points at %q, which resolves to nothing", SchemaPath, property, array.Items.Ref),
		}
	}
	return definition, nil
}

// enumOf reads the vocabulary a property declares: on the property itself, or
// on its items when the property is a list.
func enumOf(row schemaProperty, name string) []string {
	property, ok := row.Properties[name]
	if !ok {
		return nil
	}
	if len(property.Enum) > 0 {
		return property.Enum
	}
	return property.Items.Enum
}

// keysOf lists the properties a definition admits, so that "which keys does a
// suite declare" is answered from the schema and not from a list repeated here.
func keysOf(properties map[string]schemaProperty) []string {
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	return keys
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
