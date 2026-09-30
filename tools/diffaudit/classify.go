package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// classification is what the gate decided about one file of one commit: the
// class it fell into, the role that class gives it, the area it belongs to and
// the matcher that claimed it. The report prints all four, because a
// classification nobody can read is a classification nobody can argue with.
type classification struct {
	Commit  string
	Status  string
	Path    string
	Finger  string
	Role    string
	Class   string
	Area    string
	Matched string
}

// provenance register: the families of generated artifacts. The gate reads it to
// answer two questions it does not own — what is generated (and therefore what
// the change owes the input of) and which artifacts the contract family holds —
// so that `make generate-check` and this gate judge the same declarations.
const provenancePath = "quality/provenance.json"

// catalogPath is the rule table. The gate reads it for the evidence a rule
// declares and for the risk class of that rule, and it never edits it: the
// authority on the document's shape is `tools/qualitycatalog`, and this gate
// reads the subset it judges.
const catalogPath = "quality/catalog.json"

type provenanceRegister struct {
	Schema   int                `json:"schema"`
	Families []provenanceFamily `json:"families"`
}

type provenanceFamily struct {
	Name    string           `json:"name"`
	Inputs  []provenanceFile `json:"inputs"`
	Outputs []provenanceFile `json:"outputs"`
	Sources []provenanceFile `json:"sources"`
}

// provenanceFile is one entry of a family: the path of an input, of an output or
// of a generator source. The rest of the entry — the digest, the include and the
// exclude patterns — belongs to the gate of P23-T06, and this one reads the path
// because the path is what a change either touches or does not.
type provenanceFile struct {
	Path string `json:"path"`
}

// catalog is the rule table as this gate reads it: the identity, the risk class
// and the evidence of each rule. The categories of `tests` are read as data and
// split into nominal and adversarial by the policy, because that split is a
// policy decision and not a property of the document.
type catalog struct {
	Rules []catalogRule `json:"rules"`
}

type catalogRule struct {
	ID    string              `json:"id"`
	Risk  string              `json:"risk"`
	Tests map[string][]string `json:"tests"`
}

// ruleFiles answers the paths a rule declares as its evidence.
func (r catalogRule) ruleFiles(kind string) []string {
	files := []string{}
	for _, ref := range r.Tests[kind] {
		if path, _, ok := strings.Cut(ref, "::"); ok {
			files = append(files, path)
		}
	}
	return files
}

// references answers every `path::function` a rule declares, with the category
// that declared it, in a fixed order.
type reference struct {
	Kind string
	Ref  string
	Path string
	Func string
}

func (r catalogRule) references() []reference {
	refs := []reference{}
	kinds := make([]string, 0, len(r.Tests))
	for kind := range r.Tests {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		for _, entry := range r.Tests[kind] {
			path, function, _ := strings.Cut(entry, "::")
			refs = append(refs, reference{Kind: kind, Ref: entry, Path: path, Func: function})
		}
	}
	return refs
}

// readProvenance reads the generated-artifact register.
func readProvenance(root string) (provenanceRegister, error) {
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(provenancePath)))
	if err != nil {
		return provenanceRegister{}, fmt.Errorf("%s: %w", provenancePath, err)
	}
	var register provenanceRegister
	if err := json.Unmarshal(raw, &register); err != nil {
		return provenanceRegister{}, fmt.Errorf("%s: %w", provenancePath, err)
	}
	return register, nil
}

// readCatalog reads a rule table from text. The text comes from the tree or from
// one side of the change, which is what makes the Q0 question answerable: it
// compares the table before the change with the table after it.
func readCatalog(name, text string) (catalog, error) {
	var document catalog
	decoder := json.NewDecoder(bytes.NewReader([]byte(text)))
	if err := decoder.Decode(&document); err != nil {
		return catalog{}, fmt.Errorf("%s não decodifica como catálogo de regras: %w", name, err)
	}
	if len(document.Rules) == 0 {
		return catalog{}, fmt.Errorf("%s não declara regra nenhuma: um catálogo vazio não exige evidência de nada", name)
	}
	return document, nil
}

// familyOf answers the generator family a path belongs to, when the path is an
// output, an input or a source of one.
func familyOf(register provenanceRegister, path string) (provenanceFamily, string, bool) {
	for _, family := range register.Families {
		for _, output := range family.Outputs {
			if output.Path == path {
				return family, "output", true
			}
		}
		for _, input := range family.Inputs {
			if input.Path == path {
				return family, "input", true
			}
		}
		for _, source := range family.Sources {
			if source.Path == path || strings.HasPrefix(path, strings.TrimSuffix(source.Path, "/")+"/") {
				return family, "source", true
			}
		}
	}
	return provenanceFamily{}, "", false
}

// familyNamed answers a family by its name, which is how the policy points at
// the family that owns the contract artifacts.
func familyNamed(register provenanceRegister, name string) (provenanceFamily, bool) {
	for _, family := range register.Families {
		if family.Name == name {
			return family, true
		}
	}
	return provenanceFamily{}, false
}

// classifyAll walks the commits and classifies every file they touch. The
// classification is ordered and the first match wins, which is why the policy
// lists the classes in the order a reader expects: the generated artifact before
// the directory it lives in, the test before the production tree it tests.
func classifyAll(document changeDocument, pol policy, register provenanceRegister) ([]classification, error) {
	classes := []classification{}
	for _, commit := range document.Commits {
		for _, file := range commit.Files {
			entry, err := classify(commit.SHA, file, pol, register)
			if err != nil {
				return nil, err
			}
			classes = append(classes, entry)
		}
	}
	return classes, nil
}

// classify answers the class of one file of one commit.
func classify(sha string, file fileRecord, pol policy, register provenanceRegister) (classification, error) {
	entry := classification{
		Commit: sha, Status: file.Status, Path: file.Path, Area: areaOf(file.Path, pol),
		Role: roleUnclassified, Class: roleUnclassified,
	}
	if _, kind, ok := familyOf(register, file.Path); ok && kind == "output" {
		entry.Class, entry.Role, entry.Matched = "generated", roleGenerated, "artefato declarado em "+provenancePath
		return entry, nil
	}
	// A change that does not move a token is formatting: the policy does not
	// demand evidence of it, and the check exists so that a `gofmt` sweep does
	// not read as a behavior change. A file the gate cannot compare is treated as
	// a behavior change — the conservative side — and the count of those is
	// printed.
	if file.Status == modifiedFile || file.Status == renamedFile || file.Status == copiedFile {
		if file.Before != "" && file.After != "" && formatOnly(file.Before, file.After) {
			entry.Class, entry.Role, entry.Matched = "format", roleFormat, "nenhum token mudou entre os dois lados"
			return entry, nil
		}
	}
	for _, candidate := range pol.Classes {
		switch {
		case candidate.Origin == "provenance":
			continue // proved above, by the register and not by a matcher
		case candidate.Marker:
			if usesMarker(file, pol.Route.Marker) {
				entry.Class, entry.Role, entry.Matched = candidate.Name, candidate.Role, "marcador "+pol.Route.Marker
				return entry, nil
			}
		case candidate.Paths:
			if file.Path == pol.Route.Registry {
				entry.Class, entry.Role, entry.Matched = candidate.Name, candidate.Role, "registrador "+pol.Route.Registry
				return entry, nil
			}
		default:
			if reason, ok := candidate.matched(file.Path); ok {
				entry.Class, entry.Role, entry.Matched = candidate.Name, candidate.Role, reason
				return entry, nil
			}
		}
	}
	return entry, nil
}

// usesMarker answers whether either side of the change carries the marker the
// route surface is recognized by. Reading both sides matters: a file that
// registers routes and stops doing it is a route change too.
func usesMarker(file fileRecord, marker string) bool {
	return strings.Contains(file.After, marker) || strings.Contains(file.Before, marker)
}

// areaOf derives the area of a path: the first prefix of the policy that
// matches decides the depth, and a path that matches none belongs to its own
// directory. The area is what `same-area-test` compares, and nothing else.
func areaOf(path string, pol policy) string {
	segments := strings.Split(path, "/")
	for _, rule := range pol.Areas.Prefixes {
		if !strings.HasPrefix(path, rule.Prefix) {
			continue
		}
		if len(segments) < rule.Depth {
			return strings.Join(segments, "/")
		}
		return strings.Join(segments[:rule.Depth], "/")
	}
	if len(segments) > 1 {
		return strings.Join(segments[:len(segments)-1], "/")
	}
	return path
}

// formatOnly answers whether the change between two sources moved no token. It
// reads the token streams and not the text, so a comment, an indentation or a
// line break is formatting — which is exactly the question the policy asks, and
// the reason the answer is a lexer and not a diff.
func formatOnly(before, after string) bool {
	first, okFirst := tokensOf(before)
	second, okSecond := tokensOf(after)
	if !okFirst || !okSecond {
		return false
	}
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}

// tokensOf lexes a source into its token stream, skipping comments: the question
// is whether the program still says the same thing, and a comment is not the
// program saying anything.
//
// Each token travels as its class **and its text**. Comparing the class alone
// would be the bug that matters here — a string literal is a `STRING` whatever it
// contains, and an identifier is an `IDENT` whatever it is called —, so a change
// of `%v` to `%w` or of one name to another would read as formatting and the
// exemption would hide the change it was asked about. A source that cannot be
// lexed answers false, which is the conservative side.
func tokensOf(source string) ([]string, bool) {
	if source == "" {
		return nil, false
	}
	var lexer scanner.Scanner
	positions := token.NewFileSet()
	file := positions.AddFile("probe.go", positions.Base(), len(source))
	lexer.Init(file, []byte(source), nil, 0)
	tokens := []string{}
	for {
		_, tok, literal := lexer.Scan()
		if tok == token.EOF {
			break
		}
		tokens = append(tokens, tok.String()+"\x00"+literal)
	}
	return tokens, true
}

// routesIn extracts the routes a source declares, in the canonical
// `METHOD /path` form the registry uses. The shape the registry fixes is a
// composite literal with a `Method` and a `Path` field, and the method is
// written either as `http.MethodGet` or as a string; both are read, and a route
// whose method this gate cannot name is left out and counted instead of guessed.
func routesIn(source string) ([]string, []string, error) {
	if strings.TrimSpace(source) == "" {
		return nil, nil, nil
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), "routes.go", source, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("a fonte do arquivo de rotas não parseia: %w", err)
	}
	routes := []string{}
	unnamed := []string{}
	ast.Inspect(parsed, func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		method, path := "", ""
		for _, element := range literal.Elts {
			pair, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := pair.Key.(*ast.Ident)
			if !ok {
				continue
			}
			switch key.Name {
			case "Method":
				method = methodName(pair.Value)
			case "Path":
				path = stringValue(pair.Value)
			}
		}
		if path == "" {
			return true
		}
		if method == "" {
			unnamed = append(unnamed, path)
			return true
		}
		routes = append(routes, method+" "+path)
		return true
	})
	sort.Strings(routes)
	return routes, unnamed, nil
}

// methodName answers the HTTP method of a method field: the constant of the
// standard library or a string literal, and nothing else.
func methodName(expression ast.Expr) string {
	switch typed := expression.(type) {
	case *ast.SelectorExpr:
		name := typed.Sel.Name
		if strings.HasPrefix(name, "Method") {
			return strings.ToUpper(strings.TrimPrefix(name, "Method"))
		}
	case *ast.BasicLit:
		if typed.Kind == token.STRING {
			return strings.ToUpper(stringValue(typed))
		}
	}
	return ""
}

func stringValue(expression ast.Expr) string {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return ""
	}
	return strings.Trim(literal.Value, "`\"")
}

// routeSetDiff answers the routes the change adds and removes for one file, by
// reading the sets of the two sides.
func routeSetDiff(file fileRecord) ([]string, []string, error) {
	before, _, err := routesIn(file.Before)
	if err != nil {
		return nil, nil, err
	}
	after, _, err := routesIn(file.After)
	if err != nil {
		return nil, nil, err
	}
	known := map[string]bool{}
	for _, route := range before {
		known[route] = true
	}
	added := []string{}
	for _, route := range after {
		if !known[route] {
			added = append(added, route)
		}
	}
	present := map[string]bool{}
	for _, route := range after {
		present[route] = true
	}
	removed := []string{}
	for _, route := range before {
		if !present[route] {
			removed = append(removed, route)
		}
	}
	return added, removed, nil
}
