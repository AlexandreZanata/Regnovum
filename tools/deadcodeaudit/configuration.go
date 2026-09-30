package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/AlexandreZanata/Regnovum/tools/auditkit"
)

// The configuration surface of the delivered process is the one place in this
// repository where "dead" and "impossible" are both decidable, and the phase
// asked for both: a feature flag nobody reads and a variable nobody documents.
//
// The review of the gate rests on three sets, and each one is read, never
// guessed:
//
//   - accepted — the keys `config.Load` lets through. It rejects every ARENA_*
//     variable it does not know, and its own message tells the operator to look
//     at `.env.example`, so a key there and not here makes the documented
//     environment unable to start;
//   - documented — the ARENA_* names `.env.example` carries;
//   - read — the keys `Load` consults while building the configuration.
//
// A key accepted and never read is the orphan flag: the operator sets it, the
// process takes the value and drops it. A key read inside `Load` and never
// accepted is the other impossible path: the loop above drops it before any read
// can see it. And a literal ARENA_* key read with `os.Getenv` outside the
// configuration package cannot be satisfied either, because `Load` refuses the
// variable before the process gets that far.

// registry is what the configuration package declares, read from its source.
type registry struct {
	directory string         // where the accepted set lives, for the report
	accepted  map[string]int // key -> line of the accepted-set entry
	read      map[string]int // key -> line of the read
	order     []string       // every key, for a deterministic report
}

// configuration is the whole measurement, including the counts the report
// prints: a number that is not printed is a number nobody reviews.
type configuration struct {
	accepted   int
	documented int
	read       int
	comparison []finding
}

var keyPattern = regexp.MustCompile(`\bARENA_[A-Z0-9_]+\b`)

// documentedKeys lists the ARENA_* names a template file carries, in order. The
// template is the operator's contract, so a name mentioned in its prose counts:
// the file documents variables and safe examples, and its prose is what the
// operator reads.
func documentedKeys(text string) []string {
	seen := map[string]bool{}
	keys := []string{}
	for _, match := range keyPattern.FindAllString(text, -1) {
		if seen[match] {
			continue
		}
		seen[match] = true
		keys = append(keys, match)
	}
	sort.Strings(keys)
	return keys
}

// configurationFindings judges the surface: the registry, the template that
// documents it and the files of the process that read the environment directly.
// The roots are parameters because the fixtures of this gate are the proof that
// each direction still bites, and a proof that can only run against the delivered
// tree is not a proof.
func configurationFindings(registryDirectory, template string, readRoots []string) (configuration, error) {
	registry, err := readRegistry(registryDirectory)
	if err != nil {
		return configuration{}, err
	}
	raw, err := os.ReadFile(filepath.FromSlash(template))
	if err != nil {
		return configuration{}, fmt.Errorf("read %s: %w", template, err)
	}
	documented := documentedKeys(string(raw))
	compared := compareConfiguration(registry, documented, template)
	for _, root := range readRoots {
		files, err := auditkit.GoFiles(root, auditkit.SkippedDirectories)
		if err != nil {
			return configuration{}, err
		}
		direct, err := directEnvironmentReads(files, registry)
		if err != nil {
			return configuration{}, err
		}
		compared = append(compared, direct...)
	}
	auditkit.SortFindings(compared)
	return configuration{
		accepted:   len(registry.accepted),
		documented: len(documented),
		read:       len(registry.read),
		comparison: compared,
	}, nil
}

// configurationPackage is the parsed configuration package: the directory, the
// string constants it declares and the loader whose body holds both sets.
type configurationPackage struct {
	directory string
	positions *token.FileSet
	constants map[string]string
	loader    *ast.FuncDecl
}

// readRegistry reads the accepted set and the reads of `Load` out of the
// configuration package. A registry the gate cannot read is a hole the gate
// names instead of skipping.
func readRegistry(directory string) (registry, error) {
	source, err := parseConfigurationPackage(directory)
	if err != nil {
		return registry{}, err
	}
	accepted, err := source.acceptedSet()
	if err != nil {
		return registry{}, err
	}
	read := source.declaredReads()
	order := []string{}
	for key := range accepted {
		order = append(order, key)
	}
	for key := range read {
		if _, present := accepted[key]; !present {
			order = append(order, key)
		}
	}
	sort.Strings(order)
	return registry{directory: directory, accepted: accepted, read: read, order: order}, nil
}

// parseConfigurationPackage parses every non-test file of the directory and
// holds on to the loader: the accepted set of a configuration is the one its
// loader holds, and a package without one is a refusal and not an empty answer.
func parseConfigurationPackage(directory string) (configurationPackage, error) {
	source := configurationPackage{directory: directory, positions: token.NewFileSet(), constants: map[string]string{}}
	files, err := auditkit.GoFiles(directory, nil)
	if err != nil {
		return source, err
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.FromSlash(path))
		if err != nil {
			return source, fmt.Errorf("read %s: %w", path, err)
		}
		file, err := parser.ParseFile(source.positions, path, raw, 0)
		if err != nil {
			return source, fmt.Errorf("parse %s: %w", path, err)
		}
		collectConstants(source.constants, file)
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Name.Name == "Load" && function.Recv == nil {
				source.loader = function
			}
		}
	}
	if source.loader == nil {
		return source, fmt.Errorf("%s declares no func Load: the accepted set of a configuration is the one its loader holds, and this gate cannot find it", directory)
	}
	return source, nil
}

// acceptedSet reads the table of accepted keys under Load. Exactly one table is
// judged: a second one would be a configuration nobody reads.
func (source configurationPackage) acceptedSet() (map[string]int, error) {
	accepted := map[string]int{}
	entries := 0
	ast.Inspect(source.loader.Body, func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if !ok || !isBoolMap(literal.Type) {
			return true
		}
		entries++
		for _, element := range literal.Elts {
			entry, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, known := resolveKey(entry.Key, source.constants)
			if !known {
				continue
			}
			accepted[key] = source.positions.Position(entry.Key.Pos()).Line
		}
		return true
	})
	if entries != 1 {
		return nil, fmt.Errorf("%s holds %d accepted-set tables under Load and this gate judges exactly one", source.directory, entries)
	}
	return accepted, nil
}

// declaredReads reads every key Load consults, which is the other half of the
// question: a key accepted and never read here is a value the operator sets and
// the process drops.
func (source configurationPackage) declaredReads() map[string]int {
	read := map[string]int{}
	ast.Inspect(source.loader.Body, func(node ast.Node) bool {
		index, ok := node.(*ast.IndexExpr)
		if !ok {
			return true
		}
		values, ok := index.X.(*ast.Ident)
		if !ok || values.Name != "values" {
			return true
		}
		key, known := resolveKey(index.Index, source.constants)
		if known {
			read[key] = source.positions.Position(index.Index.Pos()).Line
		}
		return true
	})
	return read
}

// collectConstants records the string constants of a package: the accepted set
// names some of its keys through them, and a gate that only read literals would
// call every one of those keys undocumented.
func collectConstants(constants map[string]string, file *ast.File) {
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}
		for _, spec := range general.Specs {
			values, ok := spec.(*ast.ValueSpec)
			if !ok || len(values.Names) != len(values.Values) {
				continue
			}
			for index, name := range values.Names {
				if value, ok := stringValue(values.Values[index]); ok {
					constants[name.Name] = value
				}
			}
		}
	}
}

func stringValue(expression ast.Expr) (string, bool) {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	if err != nil {
		return "", false
	}
	return value, true
}

// resolveKey answers the key of an expression that is either a string literal or
// a constant of the package. A key the gate cannot resolve is not a key it
// guessed: it is left out, and the counts the report prints show how many were
// left out.
func resolveKey(expression ast.Expr, constants map[string]string) (string, bool) {
	if value, ok := stringValue(expression); ok {
		return value, true
	}
	if identifier, ok := expression.(*ast.Ident); ok {
		if value, present := constants[identifier.Name]; present {
			return value, true
		}
	}
	return "", false
}

func isBoolMap(expression ast.Expr) bool {
	mapping, ok := expression.(*ast.MapType)
	if !ok {
		return false
	}
	key, keyOK := mapping.Key.(*ast.Ident)
	value, valueOK := mapping.Value.(*ast.Ident)
	return keyOK && valueOK && key.Name == "string" && value.Name == "bool"
}

// compareConfiguration is the judgement over the three sets, kept apart from the
// reading so that the test drives every direction with a table instead of a
// checkout of the repository.
func compareConfiguration(registry registry, documented []string, template string) []finding {
	documentedSet := map[string]bool{}
	for _, key := range documented {
		documentedSet[key] = true
	}
	table := registry.directory + " (conjunto aceito por Load)"
	findings := []finding{}
	for _, key := range registry.order {
		line, accepted := registry.accepted[key]
		if !accepted {
			findings = append(findings, finding{
				Rule: RuleConfigurationKey, Path: table, Line: registry.read[key],
				Detail: fmt.Sprintf("%s é lida em Load e não está no conjunto aceito: o laço que recusa variável desconhecida derruba a variável antes de qualquer leitura, então essa leitura nunca vê valor", key),
			})
			continue
		}
		if _, read := registry.read[key]; !read {
			findings = append(findings, finding{
				Rule: RuleConfigurationKey, Path: table, Line: line,
				Detail: fmt.Sprintf("%s é aceita por Load e nunca é lida: o operador define o valor e o processo o descarta", key),
			})
		}
		if !documentedSet[key] {
			findings = append(findings, finding{
				Rule: RuleConfigurationKey, Path: table, Line: line,
				Detail: fmt.Sprintf("%s é aceita por Load e não é documentada em %s: a mensagem que recusa a variável desconhecida manda o operador ler justamente esse arquivo", key, template),
			})
		}
	}
	for _, key := range documented {
		if _, accepted := registry.accepted[key]; accepted {
			continue
		}
		findings = append(findings, finding{
			Rule: RuleConfigurationKey, Path: template, Line: 1,
			Detail: fmt.Sprintf("%s é documentada em %s e Load a recusa: seguir o modelo publicado impede o processo de subir", key, template),
		})
	}
	return findings
}

// directEnvironmentReads refuses the literal ARENA_* key read with os.Getenv or
// os.LookupEnv outside the configuration package. The value can never arrive:
// Load rejects the variable, and the process refuses to start with it set. Only
// literals are judged — a call whose key comes from a variable is left to the
// gap this gate prints, instead of being guessed.
func directEnvironmentReads(files []string, registry registry) ([]finding, error) {
	findings := []finding{}
	positions := token.NewFileSet()
	for _, path := range files {
		if pathUnder(path, registry.directory) || strings.HasSuffix(path, "_test.go") {
			continue
		}
		source, err := os.ReadFile(filepath.FromSlash(path))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		file, err := parser.ParseFile(positions, path, source, 0)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || !isEnvironmentRead(call) {
				return true
			}
			for _, argument := range call.Args {
				key, ok := stringValue(argument)
				if !ok || !keyPattern.MatchString(key) {
					continue
				}
				if _, accepted := registry.accepted[key]; accepted {
					continue
				}
				findings = append(findings, finding{
					Rule: RuleConfigurationKey, Path: path,
					Line:   positions.Position(call.Pos()).Line,
					Detail: fmt.Sprintf("%s é lida direto do ambiente e o conjunto aceito não a conhece: com a variável definida o processo não sobe, e sem ela a leitura é vazia — o caminho é impossível dos dois lados", key),
				})
			}
			return true
		})
	}
	return findings, nil
}

func isEnvironmentRead(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	identifier, ok := selector.X.(*ast.Ident)
	if !ok || identifier.Name != "os" {
		return false
	}
	return selector.Sel.Name == "Getenv" || selector.Sel.Name == "LookupEnv"
}

// pathUnder reports whether a path lives in the directory or below it.
func pathUnder(path, directory string) bool {
	return path == directory || strings.HasPrefix(path, strings.TrimSuffix(directory, "/")+"/")
}
