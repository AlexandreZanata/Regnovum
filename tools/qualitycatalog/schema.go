package main

import (
	"encoding/json"
	"fmt"
)

// The schema-agreement codes. The loader and the schema are two statements of
// the same contract, and this is where they are held together: a document that
// promises something the loader does not enforce is a document that will be
// trusted for a reason that stopped being true.
const (
	codeSchemaInvalid        = "schema-invalid"
	codeSchemaMissing        = "schema-missing"
	codeSchemaRuleFields     = "schema-rule-fields"
	codeSchemaTestCategories = "schema-test-categories"
	codeSchemaRiskClasses    = "schema-risk-classes"
	codeSchemaVersionConst   = "schema-version-const"
	codeSchemaStrictness     = "schema-strictness"
	codeSchemaCatalogEmpty   = "schema-catalog-empty"
)

// schemaPath is where the catalog schema lives, relative to the repository
// root. It is a constant because two callers asking for "the schema" have to
// mean the same file.
const schemaPath = "quality/catalog.schema.json"

// ReadSchema reads the committed schema and judges it against the loader. A
// schema that is not there cannot hold the loader to anything, so its absence
// is a refusal: the agreement below is the only reason to trust that the
// document and the tool say the same thing.
func ReadSchema(root string) []Violation {
	tree := Tree{Root: root}
	document, ok := tree.read(schemaPath)
	if !ok {
		return []Violation{{
			Rule:   "schema",
			Code:   codeSchemaMissing,
			Detail: fmt.Sprintf("`%s` is not in the checkout: a loader with no published schema has no contract to keep", schemaPath),
		}}
	}
	return SchemaAgreement(document)
}

// schemaDocument is as much of the JSON Schema as this comparison needs: the
// required lists, the vocabularies, the empty-catalog floor and the strictness
// switches. Everything else in the file is for the human reader.
type schemaDocument struct {
	Required             *[]string                  `json:"required"`
	AdditionalProperties *bool                      `json:"additionalProperties"`
	Properties           map[string]json.RawMessage `json:"properties"`
	Defs                 map[string]json.RawMessage `json:"$defs"`
}

// schemaObject is one nested object of the schema.
type schemaObject struct {
	Required             *[]string                  `json:"required"`
	AdditionalProperties *bool                      `json:"additionalProperties"`
	MinItems             *int                       `json:"minItems"`
	Properties           map[string]json.RawMessage `json:"properties"`
}

// schemaVocabulary reads the enum of a property.
type schemaVocabulary struct {
	Enum []string `json:"enum"`
}

// schemaConstant reads the const of a property.
type schemaConstant struct {
	Const *int `json:"const"`
}

// SchemaAgreement judges the schema against the loader that claims to
// implement it. An empty result means the two say the same thing.
func SchemaAgreement(document []byte) []Violation {
	refuse := func(code, detail string) []Violation {
		return []Violation{{Rule: "schema", Code: code, Detail: detail}}
	}

	var root schemaDocument
	if err := json.Unmarshal(document, &root); err != nil {
		return refuse(codeSchemaInvalid, fmt.Sprintf("the schema does not decode as JSON: %v", err))
	}

	var violations []Violation
	decline := func(code, detail string) {
		violations = append(violations, Violation{Rule: "schema", Code: code, Detail: detail})
	}

	// The top of the document: the version it pins and the floor that keeps an
	// empty catalog from being vacuously valid.
	if raw, ok := root.Properties["schema_version"]; ok {
		var constant schemaConstant
		if json.Unmarshal(raw, &constant) == nil {
			if constant.Const == nil {
				decline(codeSchemaVersionConst, "`schema_version` pins no value: a version a document may omit is a version no loader can refuse")
			} else if *constant.Const != SchemaVersion {
				decline(codeSchemaVersionConst, fmt.Sprintf("the schema pins schema_version %d and the loader implements %d", *constant.Const, SchemaVersion))
			}
		}
	} else {
		decline(codeSchemaVersionConst, "`schema_version` is not declared: a catalog without a version cannot be refused when the loader moves on")
	}

	if raw, ok := root.Properties["rules"]; ok {
		var rules schemaObject
		if json.Unmarshal(raw, &rules) == nil {
			if rules.MinItems == nil || *rules.MinItems < 1 {
				decline(codeSchemaCatalogEmpty, "`rules` admits an empty list: an empty catalog verifies nothing, and the schema must refuse it too")
			}
		}
	} else {
		decline(codeSchemaCatalogEmpty, "`rules` is not declared")
	}

	// Strictness: unknown fields are the mistake the schema exists to refuse,
	// so all three levels refuse them.
	strictness := []struct {
		where string
		value *bool
	}{
		{"the catalog", root.AdditionalProperties},
	}
	if raw, ok := root.Defs["rule"]; ok {
		var rule schemaObject
		if json.Unmarshal(raw, &rule) == nil {
			required := rule.Required
			if required == nil {
				decline(codeSchemaRuleFields, "the rule object declares no `required` list: the loader demands fields the schema does not")
			} else if !sameSet(*required, RuleFields) {
				decline(codeSchemaRuleFields, fmt.Sprintf("the rule `required` list is %v and the loader demands %v", *required, RuleFields))
			}
			strictness = append(strictness, struct {
				where string
				value *bool
			}{"the rule", rule.AdditionalProperties})

			if raw, ok := rule.Properties["risk"]; ok {
				var vocabulary schemaVocabulary
				if json.Unmarshal(raw, &vocabulary) == nil && !sameSet(vocabulary.Enum, RiskClasses) {
					decline(codeSchemaRiskClasses, fmt.Sprintf("the schema admits risks %v and the loader knows %v", vocabulary.Enum, RiskClasses))
				}
			} else {
				decline(codeSchemaRiskClasses, "the rule declares no `risk` property")
			}
		}
	} else {
		decline(codeSchemaRuleFields, "`$defs.rule` is not declared")
	}
	if raw, ok := root.Defs["tests"]; ok {
		var tests schemaObject
		if json.Unmarshal(raw, &tests) == nil {
			required := tests.Required
			if required == nil {
				decline(codeSchemaTestCategories, "the tests object declares no `required` list")
			} else if !sameSet(*required, RequiredTestCategories) {
				decline(codeSchemaTestCategories, fmt.Sprintf("the tests `required` list is %v and the loader demands %v", *required, RequiredTestCategories))
			}
			strictness = append(strictness, struct {
				where string
				value *bool
			}{"the tests object", tests.AdditionalProperties})
		}
	} else {
		decline(codeSchemaTestCategories, "`$defs.tests` is not declared")
	}

	for _, level := range strictness {
		if level.value == nil || *level.value {
			decline(codeSchemaStrictness, fmt.Sprintf("%s admits fields it does not declare: the loader refuses them, and a schema that does not is a second contract", level.where))
		}
	}
	return violations
}

// sameSet reports whether two lists hold the same values, order aside. The
// comparison is deliberately by value and not by position: a schema that lists
// the same fields in a different order says the same thing.
func sameSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counting := map[string]int{}
	for _, value := range left {
		counting[value]++
	}
	for _, value := range right {
		counting[value]--
		if counting[value] < 0 {
			return false
		}
	}
	for _, remaining := range counting {
		if remaining != 0 {
			return false
		}
	}
	return true
}
