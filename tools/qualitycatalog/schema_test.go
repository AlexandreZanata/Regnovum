// Tests of the schema–loader agreement (P21-T02).
//
// The catalog has two published halves: the schema a human reads and the loader
// a pipeline runs. Neither validates the other at runtime — there is no JSON
// Schema engine in the standard library, and adding one would make a second
// contract out of the first — so the agreement is held here, in both
// directions:
//
//   - the schema this repository commits has to pass the comparison, so the two
//     documents cannot drift apart in silence;
//   - every agreement code has a mutation that makes it fire, so a comparison
//     that stopped comparing fails here instead of blessing a schema that
//     promises less than the loader enforces.
//
// A schema that promised less would be worse than no schema: it would be read
// as permission.
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestTheDeliveredSchemaAgreesWithTheLoader judges the file as it is committed,
// not a fixture: the point of the check is the document that ships.
func TestTheDeliveredSchemaAgreesWithTheLoader(t *testing.T) {
	if violations := ReadSchema(filepath.Join("..", "..")); len(violations) > 0 {
		t.Fatalf("the schema this repository publishes and the loader disagree: %v", violations)
	}
}

// TestAMissingSchemaIsRefused covers the refusal with no document to mutate: a
// checkout without the schema. A loader with no published schema has no
// contract to keep, and silence would read as agreement.
func TestAMissingSchemaIsRefused(t *testing.T) {
	if violations := ReadSchema(t.TempDir()); !hasCode(violations, codeSchemaMissing) {
		t.Fatalf("a checkout without the schema was not refused with `%s`: %v", codeSchemaMissing, violations)
	}
}

// TestEverySchemaRuleIsFalsified breaks one promise of the schema at a time.
func TestEverySchemaRuleIsFalsified(t *testing.T) {
	for _, testCase := range schemaMutations() {
		t.Run(testCase.name, func(t *testing.T) {
			violations := SchemaAgreement(testCase.build(t))
			if !hasCode(violations, testCase.code) {
				t.Fatalf("the schema defect was not refused with `%s`; the codes that fired were %v", testCase.code, codesOf(violations))
			}
		})
	}
}

// schemaMutations is one broken promise per agreement code.
func schemaMutations() []mutationCase {
	return []mutationCase{
		{
			name: "a schema that is not JSON",
			code: codeSchemaInvalid,
			build: func(t *testing.T) []byte {
				return []byte("{")
			},
		},
		{
			name: "a version the loader does not implement",
			code: codeSchemaVersionConst,
			build: func(t *testing.T) []byte {
				return changedSchema(t, func(t *testing.T, schema map[string]any) {
					set(t, objectAt(t, objectAt(t, schema, "properties"), "schema_version"), "const", SchemaVersion+1)
				})
			},
		},
		{
			name: "a schema_version the schema does not pin",
			code: codeSchemaVersionConst,
			build: func(t *testing.T) []byte {
				return changedSchema(t, func(t *testing.T, schema map[string]any) {
					remove(t, objectAt(t, schema, "properties"), "schema_version")
				})
			},
		},
		{
			name: "a catalog that may be empty",
			code: codeSchemaCatalogEmpty,
			build: func(t *testing.T) []byte {
				return changedSchema(t, func(t *testing.T, schema map[string]any) {
					set(t, objectAt(t, objectAt(t, schema, "properties"), "rules"), "minItems", 0)
				})
			},
		},
		{
			name: "a rule promise without the fields the loader demands",
			code: codeSchemaRuleFields,
			build: func(t *testing.T) []byte {
				return changedSchema(t, func(t *testing.T, schema map[string]any) {
					rule := objectAt(t, objectAt(t, schema, "$defs"), "rule")
					set(t, rule, "required", without(t, listAt(t, rule, "required"), "evidence"))
				})
			},
		},
		{
			name: "a schema that declares no rule object",
			code: codeSchemaRuleFields,
			build: func(t *testing.T) []byte {
				return changedSchema(t, func(t *testing.T, schema map[string]any) {
					remove(t, objectAt(t, schema, "$defs"), "rule")
				})
			},
		},
		{
			name: "a risk vocabulary the loader does not know",
			code: codeSchemaRiskClasses,
			build: func(t *testing.T) []byte {
				return changedSchema(t, func(t *testing.T, schema map[string]any) {
					risk := objectAt(t, objectAt(t, objectAt(t, objectAt(t, schema, "$defs"), "rule"), "properties"), "risk")
					set(t, risk, "enum", []any{"Q0", "Q1"})
				})
			},
		},
		{
			name: "a rule that declares no risk property",
			code: codeSchemaRiskClasses,
			build: func(t *testing.T) []byte {
				return changedSchema(t, func(t *testing.T, schema map[string]any) {
					remove(t, objectAt(t, objectAt(t, objectAt(t, schema, "$defs"), "rule"), "properties"), "risk")
				})
			},
		},
		{
			name: "evidence categories the loader does not demand",
			code: codeSchemaTestCategories,
			build: func(t *testing.T) []byte {
				return changedSchema(t, func(t *testing.T, schema map[string]any) {
					tests := objectAt(t, objectAt(t, schema, "$defs"), "tests")
					set(t, tests, "required", without(t, listAt(t, tests, "required"), "negative"))
				})
			},
		},
		{
			name: "a schema that declares no evidence slots",
			code: codeSchemaTestCategories,
			build: func(t *testing.T) []byte {
				return changedSchema(t, func(t *testing.T, schema map[string]any) {
					remove(t, objectAt(t, schema, "$defs"), "tests")
				})
			},
		},
		{
			name: "a catalog that admits fields it does not declare",
			code: codeSchemaStrictness,
			build: func(t *testing.T) []byte {
				return changedSchema(t, func(t *testing.T, schema map[string]any) {
					set(t, schema, "additionalProperties", true)
				})
			},
		},
		{
			name: "a rule that admits fields it does not declare",
			code: codeSchemaStrictness,
			build: func(t *testing.T) []byte {
				return changedSchema(t, func(t *testing.T, schema map[string]any) {
					set(t, objectAt(t, objectAt(t, schema, "$defs"), "rule"), "additionalProperties", true)
				})
			},
		},
		{
			name: "evidence slots that admit categories the schema does not declare",
			code: codeSchemaStrictness,
			build: func(t *testing.T) []byte {
				return changedSchema(t, func(t *testing.T, schema map[string]any) {
					set(t, objectAt(t, objectAt(t, schema, "$defs"), "tests"), "additionalProperties", true)
				})
			},
		},
	}
}

// schemaBytes reads the published schema, because the mutations below have to
// break the document that ships rather than a copy of it that may have moved on.
func schemaBytes(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(schemaPath)))
	if err != nil {
		t.Fatalf("the published schema is unreadable: %v", err)
	}
	return raw
}

// changedSchema applies a mutation to the published schema and refuses when it
// came out identical.
func changedSchema(t *testing.T, mutate func(t *testing.T, schema map[string]any)) []byte {
	t.Helper()
	var schema map[string]any
	if err := json.Unmarshal(schemaBytes(t), &schema); err != nil {
		t.Fatalf("the published schema does not decode: %v", err)
	}
	before := encode(t, schema)
	mutate(t, schema)
	after := encode(t, schema)
	if bytes.Equal(before, after) {
		t.Fatal("the mutation left the schema unchanged")
	}
	return after
}

// objectAt reaches a nested object of the schema.
func objectAt(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("the schema has no object at `%s`", key)
	}
	return value
}

// listAt reaches a list of the schema.
func listAt(t *testing.T, parent map[string]any, key string) []any {
	t.Helper()
	value, ok := parent[key].([]any)
	if !ok {
		t.Fatalf("the schema has no list at `%s`", key)
	}
	return value
}

// without removes one item from a list that has to hold it.
func without(t *testing.T, list []any, item string) []any {
	t.Helper()
	for index, value := range list {
		if value == item {
			return append(list[:index:index], list[index+1:]...)
		}
	}
	t.Fatalf("the schema list does not hold `%s`", item)
	return nil
}
