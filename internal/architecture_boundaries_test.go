// Package architecture_test enforces the shape of the backend tree and the
// direction of its dependencies.
//
// The sibling file (architecture_test.go) holds the gates that grew with the
// platform: the dependency direction of domain/application, the environment and
// effect gates, the test-only packages, the provider SDK and the module
// registry. This file is the complete boundary gate of P23-T01 ("gate completo
// de arquitetura"): it covers modules, layers, adapters, the composition root,
// HTTP and SQL, and it names the three failures the task asks for by name —
// a forbidden import, a cross-module access and an adapter wired outside the
// composition root — each proven by a fixture, because a rule nobody has
// watched fail is a comment, not a gate.
//
// What it adds to the sibling gate:
//
//   - the filesystem shape of internal/ (declared modules, declared standalone
//     packages and the layer directories each module is allowed to have);
//   - the inward direction *inside* a module (domain ◄ application ◄ adapters),
//     which the first version of the gate did not check at all: it forbade
//     domain/application from importing adapters, but nothing stopped the domain
//     from importing its own application;
//   - the composition root rule (only bootstrap and cmd/ wire adapters) with the
//     documented exception of the approved projection;
//   - the ownership of the SQL driver, the migration tool, the provider SDK, the
//     cryptography package and the Unicode library;
//   - the transport confinement (net/http belongs to the transport layers);
//   - the conceptual circular dependency between modules, detected as strongly
//     connected components and compared with the components the tree is allowed
//     to have.
//
// The dependency direction of the tree is judged on what ships: a `_test.go`
// file may stand in for a provider or reach a fixture, and P22-T05's simulators
// exist precisely so that it does not have to. Test files are therefore not
// judged here, and the packages the sibling gate already confines to tests
// (testsource, testsupport, providersim, testguard) are exempt from the rule
// that keeps internal/platform technical, since that gate proves they are
// unreachable from delivered code.
package architecture_test

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Layers names the layer directories a module may own, in the order the
// dependency rule points: adapters depend on application, application depends on
// domain, domain depends on the standard library. "contract" is the module's
// public surface for the modules that publish one.
const (
	layerDomain      = "domain"
	layerApplication = "application"
	layerAdapters    = "adapters"
	layerContract    = "contract"
)

// declaredModules maps every module that owns layers to the layers it must
// have. This is the allowlist the task asks for: minimal (a module declares the
// layers it actually has, nothing more) and documented (the comment of each
// row says why the module has that shape).
//
// There is no "domain" for search or statprojections because a query and a
// projection have no invariant of their own: search indexes what other modules
// publish and statprojections reads the aggregates transparency already owns,
// so their rules live where the data is. notifications owns a contract because
// it is the one module other modules address by name.
var declaredModules = map[string][]string{
	"arenas":          {layerDomain, layerApplication, layerAdapters},
	"arguments":       {layerDomain, layerApplication, layerAdapters},
	"audit":           {layerDomain, layerApplication, layerAdapters},
	"billing":         {layerDomain, layerApplication, layerAdapters},
	"economy":         {layerDomain, layerApplication, layerAdapters},
	"identity":        {layerDomain, layerApplication, layerAdapters},
	"jobs":            {layerDomain, layerApplication, layerAdapters},
	"moderation":      {layerDomain, layerApplication, layerAdapters},
	"notifications":   {layerDomain, layerApplication, layerContract, layerAdapters},
	"persuasion":      {layerDomain, layerApplication, layerAdapters},
	"positions":       {layerDomain, layerApplication, layerAdapters},
	"pricing":         {layerDomain, layerApplication, layerAdapters},
	"profiles":        {layerDomain, layerApplication, layerAdapters},
	"search":          {layerApplication, layerAdapters},
	"statprojections": {layerApplication, layerAdapters},
	"transparency":    {layerDomain, layerApplication, layerAdapters},
	"wallet":          {layerDomain, layerApplication, layerAdapters},
}

// declaredStandalonePackages are the top-level internal/ packages that are not
// modules with layers. Declaring them is what turns "a new directory appeared
// under internal/" from an observation into a decision: the author either
// declares it here or finds out that the tree has a shape.
//
//   - platform: technical adapters shared by modules (docs/ARCHITECTURE.md §7);
//   - bootstrap: the composition root;
//   - ports: the ports shared by every module (Clock, Random, IDGenerator);
//   - i18n: the bilingual catalogs;
//   - contract, security, buildinfo, i18ngen: build, test and CLI tooling;
//   - performance, regression: test-only suites without layers (budgets,
//     payloads, baselines, soak), declared so the shape stays a decision.
var declaredStandalonePackages = []string{
	"buildinfo", "bootstrap", "contract", "i18n", "i18ngen", "performance", "platform", "ports", "regression", "security",
}

// publicSurfaceLayers are the layers of one module that another module may use:
// its domain types, its application commands/queries and, where it publishes
// one, its contract. An inbound adapter of module A composes the public surface
// of module B (docs/ARCHITECTURE.md §4, "cada módulo expõe apenas comandos,
// queries e eventos públicos próprios"); it never reaches for B's adapters.
var publicSurfaceLayers = map[string]bool{
	layerDomain:      true,
	layerApplication: true,
	layerContract:    true,
}

// adapterImportExceptions are the only places where a package imports another
// package's adapter instead of going through the port. Each one is a reviewed
// decision, and each one has to exist in the tree: an exception that outlives
// its reason is a hole, not a decision.
//
//   - statprojections is a projection (docs/ARCHITECTURE.md §4: "acesso direto
//     às tabelas de outro módulo é proibido fora de projeções explicitamente
//     aprovadas"). It reads the transparency store it projects, and it imports
//     the store rather than the module because the projection is the one reader
//     allowed to bypass the query path.
var adapterImportExceptions = []struct {
	from   string
	to     string
	reason string
}{
	{
		from:   "internal/statprojections/adapters/postgres",
		to:     "internal/transparency/adapters/postgres",
		reason: "approved projection over the transparency store (docs/ARCHITECTURE.md §4)",
	},
}

// ownedDependency is a dependency with a named owner: the packages allowed to
// import it. It generalizes the P12-T03 rule (the provider SDK belongs to its
// adapter) to the SQL driver, the migration tool, the cryptography package and
// the Unicode library, which is what "SQL e tipo de fornecedor" means in the
// task: the detail is homologated for one owner and everywhere else the port is
// the only way in.
type ownedDependency struct {
	name     string
	prefixes []string
	owners   []string // path.Match patterns over the repo-relative package directory
	// ownersByTransport marks the rows whose owners are a shape — the transport
	// layers and the strictly technical modules — instead of a directory list.
	ownersByTransport bool
	// backendOnly judges the row only inside internal/ and cmd/: the repository's
	// development tooling talks to PostgreSQL and to the migration tool on
	// purpose (the drill audit, the e2e database harness and the migration
	// audit exist to exercise the database from outside the product), and the
	// boundary this gate governs is the one that ships.
	backendOnly bool
	reason      string
}

var ownedDependencies = []ownedDependency{
	{
		name:        "the PostgreSQL driver",
		prefixes:    []string{"github.com/jackc/pgx"},
		backendOnly: true,
		owners: []string{
			"internal/*/adapters/postgres",
			"internal/platform/postgres",
			"internal/platform/dbpool",
			"internal/platform/dbmigrate",
			"internal/platform/dbtest",
			"internal/bootstrap",
		},
		reason: "pgx belongs to the PostgreSQL adapter (AGENTS.md §3); domain and application reach it through ports, and the composition root opens the pool",
	},
	{
		name:        "the migration tool",
		prefixes:    []string{"github.com/pressly/goose"},
		backendOnly: true,
		owners:      []string{"internal/platform/dbmigrate"},
		reason:      "goose is a build/migration tool, isolated in the migration adapter",
	},
	{
		name:     "the payment provider SDK",
		prefixes: []string{"github.com/stripe/stripe-go"},
		owners: []string{
			"internal/*/adapters/stripe",
			"internal/bootstrap",
			"cmd/*",
		},
		reason: "the provider SDK is homologated for its adapter, which the composition root wires (P12-T03); provider types never cross into domain/application",
	},
	{
		name:     "the cryptography package",
		prefixes: []string{"golang.org/x/crypto"},
		owners:   []string{"internal/identity/adapters/argon2id"},
		reason:   "golang.org/x/crypto is homologated for password hashing; a second use is an ADR, not a new directory",
	},
	{
		name:     "the Unicode segmentation library",
		prefixes: []string{"github.com/rivo/uniseg"},
		owners:   []string{"internal/platform/text"},
		reason:   "grapheme clusters are a platform concern (ADR-013); domain and application consume it through ports",
	},
	{
		name:              "the transport package",
		prefixes:          []string{"net/http"},
		ownersByTransport: true,
		backendOnly:       true,
		reason:            "net/http is a transport detail: it belongs to the inbound adapters, to the strictly technical platform and to the composition root, never to a layer that must be replayable without a socket",
	},
}

// transportLayers are the layers allowed to import the transport package, and
// transportModules the standalone packages that may: platform serves the HTTP
// middleware, bootstrap mounts the mux, cmd/ runs the server, and
// internal/contract is the build/test tooling that compares the served routes
// with the document.
var transportLayers = map[string]bool{layerAdapters: true}

var transportModules = map[string]bool{
	"platform":  true,
	"bootstrap": true,
	"cmd":       true,
	"contract":  true,
}

// sanctionedModuleCycles lists the strongly connected components the module
// graph is allowed to have, with the edge that closes each one. Every closing
// edge starts in an adapter: the core (domain/application) of a module never
// depends on another module, so the cycles are consequences of adapters using
// another module's public surface in both directions, which the direction rule
// permits on purpose.
//
// They are listed rather than forbidden because forbidding them today would
// mean either weakening the direction rule or rewriting seven modules in a task
// that owns no production code. What the gate changes is that a *new* cycle
// cannot appear in silence: it fails and the author has to decide between a
// port, an event or a documented entry in this list — the same choice the
// architecture doc describes when it says cycles are forbidden and verified by
// the package graph.
//
// The closing edges of the one component below, for the record:
// arenas→arguments and arenas→positions (arenas/adapters/html renders them),
// arguments→arenas and arguments→identity and positions→arenas and
// positions→identity (their eligibility adapters read the public surface),
// identity→audit, identity→jobs, identity→moderation and jobs→audit,
// jobs→moderation and moderation→audit and audit→arenas (the audit bridges).
var sanctionedModuleCycles = []string{
	"arenas,arguments,audit,identity,jobs,moderation,positions",
}

// internalPackage is one package directory of the backend tree, with the
// classification the rules use and the imports it declares.
type internalPackage struct {
	dir     string   // "internal/arenas/adapters/html" or "cmd/arena"
	module  string   // "arenas", "platform", "ports", "cmd", ...
	layer   string   // "domain", "application", "adapters", "contract" or "" (not a layer)
	adapter string   // adapter name when layer is "adapters"
	imports []string // every import, with the module path of this repository stripped
}

// internalTarget describes an import that points inside this repository.
type internalTarget struct {
	module  string
	layer   string
	adapter string
}

// classifyInternalPath classifies a repo-relative import path. The second
// result is false for imports that are not part of this repository's internal
// tree (a standard library or third-party package).
func classifyInternalPath(importPath string) (internalTarget, bool) {
	parts := strings.Split(importPath, "/")
	if len(parts) == 0 {
		return internalTarget{}, false
	}
	switch parts[0] {
	case "internal":
		if len(parts) < 2 {
			return internalTarget{}, false
		}
		target := internalTarget{module: parts[1]}
		if _, isModule := declaredModules[parts[1]]; isModule && len(parts) >= 3 {
			target.layer = parts[2]
			if target.layer == layerAdapters && len(parts) >= 4 {
				target.adapter = parts[3]
			}
		}
		return target, true
	case "cmd":
		return internalTarget{module: "cmd"}, true
	default:
		return internalTarget{}, false
	}
}

// scanInternalPackages walks internal/ and cmd/ and returns one entry per
// delivered package, with the imports its non-test files declare. Synthetic
// tables (the fixtures) are built by hand in the same shape, so the rules are
// exercised by the same code that judges the tree.
func scanInternalPackages(t *testing.T) []internalPackage {
	t.Helper()
	root := repositoryRoot(t)
	fset := token.NewFileSet()
	packages := []internalPackage{}

	// Packages are collected per directory so that a directory with several
	// files yields one entry, like a package and not like a file.
	directories := map[string]bool{}
	for _, top := range []string{"internal", "cmd"} {
		collectErr := filepath.WalkDir(filepath.Join(root, top), func(dir string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "testdata" {
					return filepath.SkipDir
				}
				rel, relErr := filepath.Rel(root, dir)
				if relErr != nil {
					return relErr
				}
				directories[filepath.ToSlash(rel)] = true
				return nil
			}
			return nil
		})
		if collectErr != nil {
			t.Fatalf("walk %s/: %v", top, collectErr)
		}
	}

	dirNames := make([]string, 0, len(directories))
	for dir := range directories {
		dirNames = append(dirNames, dir)
	}
	sort.Strings(dirNames)

	for _, dir := range dirNames {
		entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		unit := internalPackage{dir: dir}
		if target, ok := classifyInternalPath(dir); ok {
			unit.module, unit.layer, unit.adapter = target.module, target.layer, target.adapter
		} else {
			unit.module = dir
		}

		delivered := false
		imports := map[string]bool{}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			delivered = true
			file, parseErr := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(dir), entry.Name()), nil, parser.ImportsOnly)
			if parseErr != nil {
				t.Errorf("parse %s/%s: %v", dir, entry.Name(), parseErr)
				continue
			}
			for _, importSpec := range file.Imports {
				importPath, unquoteErr := strconv.Unquote(importSpec.Path.Value)
				if unquoteErr != nil {
					t.Errorf("%s/%s: unparsable import %s", dir, entry.Name(), importSpec.Path.Value)
					continue
				}
				imports[strings.TrimPrefix(importPath, modulePrefix)] = true
			}
		}
		if !delivered {
			continue
		}
		for importPath := range imports {
			unit.imports = append(unit.imports, importPath)
		}
		sort.Strings(unit.imports)
		packages = append(packages, unit)
	}
	if len(packages) == 0 {
		t.Fatal("no delivered package found under internal/ or cmd/: the gate would judge nothing")
	}
	return packages
}

// treeShape is the filesystem view of internal/: the entries directly under it
// and the Go files of every directory below.
type treeShape struct {
	topLevel []string
	goFiles  map[string][]string // directory relative to internal/, e.g. "arenas/domain"
}

// scanTreeShape reads the shape of internal/ without judging it.
func scanTreeShape(t *testing.T) treeShape {
	t.Helper()
	root := filepath.Join(repositoryRoot(t), "internal")
	shape := treeShape{goFiles: map[string][]string{}}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read internal/: %v", err)
	}
	for _, entry := range entries {
		shape.topLevel = append(shape.topLevel, entry.Name())
	}
	sort.Strings(shape.topLevel)

	walkErr := filepath.WalkDir(root, func(dir string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "testdata" {
				return filepath.SkipDir
			}
			rel, relErr := filepath.Rel(root, dir)
			if relErr != nil {
				return relErr
			}
			shape.goFiles[filepath.ToSlash(rel)] = nil
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, filepath.Dir(dir))
		if relErr != nil {
			return relErr
		}
		key := filepath.ToSlash(rel)
		shape.goFiles[key] = append(shape.goFiles[key], entry.Name())
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk internal/: %v", walkErr)
	}
	return shape
}

// shapeViolations judges the filesystem shape: every top-level entry is
// declared, every declared module has exactly the layers it declared and each
// one holds code, and every declared standalone package exists.
func shapeViolations(shape treeShape) []string {
	violations := []string{}
	declared := map[string]bool{}
	for module := range declaredModules {
		declared[module] = true
	}
	for _, name := range declaredStandalonePackages {
		declared[name] = true
	}

	for _, name := range shape.topLevel {
		if strings.HasSuffix(name, ".go") {
			continue
		}
		if !declared[name] {
			violations = append(violations, fmt.Sprintf(
				"internal/%s is not declared — declare it as a module with its layers or as a standalone package in this gate (P23-T01)", name))
		}
	}

	for module, layers := range declaredModules {
		allowed := map[string]bool{}
		for _, layer := range layers {
			allowed[layer] = true
		}
		for _, layer := range layers {
			if !hasGoCode(shape.goFiles, module+"/"+layer) {
				violations = append(violations, fmt.Sprintf(
					"internal/%s/%s is declared but holds no Go file: a declared layer is a layer with code", module, layer))
			}
		}
		// Any other child directory of the module is either a layer that was
		// never declared (and therefore never reviewed) or a stray directory
		// somebody put next to the layers.
		for dir := range shape.goFiles {
			if !strings.HasPrefix(dir, module+"/") {
				continue
			}
			rest := strings.TrimPrefix(dir, module+"/")
			child := strings.SplitN(rest, "/", 2)[0]
			if !allowed[child] {
				violations = append(violations, fmt.Sprintf(
					"internal/%s/%s is not a declared layer of %s (declared: %s)", module, child, module, strings.Join(layers, ", ")))
			}
		}
	}

	for _, name := range declaredStandalonePackages {
		found := false
		for dir := range shape.goFiles {
			if dir == name || strings.HasPrefix(dir, name+"/") {
				found = true
			}
		}
		if !found {
			violations = append(violations, fmt.Sprintf(
				"internal/%s is declared but does not exist: remove it from the gate instead of keeping a rule about nothing", name))
		}
	}

	// The module registry of the older gate and this one have to agree, or one
	// of them is judging a tree that no longer exists.
	for _, module := range businessModules {
		if _, ok := declaredModules[module]; !ok {
			violations = append(violations, fmt.Sprintf(
				"business module %s is missing from the declared modules of this gate", module))
		}
	}
	return violations
}

// hasGoCode reports whether a directory or any directory below it holds a Go
// file. A layer is allowed to be a directory of adapter packages (adapters/http,
// adapters/postgres, ...) instead of a package itself, which is the shape the
// architecture doc gives it.
func hasGoCode(goFiles map[string][]string, dir string) bool {
	if len(goFiles[dir]) > 0 {
		return true
	}
	for candidate, files := range goFiles {
		if strings.HasPrefix(candidate, dir+"/") && len(files) > 0 {
			return true
		}
	}
	return false
}

// layerViolations judges the direction of every import inside the repository:
// dependencies point inward, and no core layer reaches outside its own module.
func layerViolations(packages []internalPackage) []string {
	violations := []string{}
	for _, unit := range packages {
		for _, importPath := range unit.imports {
			target, isInternal := classifyInternalPath(importPath)
			if !isInternal {
				continue
			}
			if rule, violates := layerRule(unit, target, importPath); violates {
				violations = append(violations, rule)
			}
		}
	}
	return violations
}

// layerRule applies the direction rule to one import. An import of the
// composition root is left to the wiring rule, so that one fact has one message.
func layerRule(unit internalPackage, target internalTarget, importPath string) (string, bool) {
	if target.module == "bootstrap" && unit.module != "bootstrap" {
		return "", false
	}
	sameModule := target.module == unit.module
	describe := fmt.Sprintf("%s imports %s", unit.dir, importPath)

	switch unit.layer {
	case layerDomain:
		// The adapter check comes first because a provider or persistence type
		// is what makes this import tempting, and the message has to say so.
		if target.layer == layerAdapters {
			return describe + " — domain must not import adapters: providers and storage are details behind a port", true
		}
		if !sameModule {
			return describe + " — the domain layer is the standard library plus its own module; another module's behaviour arrives through a port", true
		}
		if target.layer != layerDomain {
			return describe + " — domain must not import application, adapters or contract: dependencies point inward", true
		}
	case layerApplication:
		if target.layer == layerAdapters {
			return describe + " — application must not import adapters: the port is the boundary, the adapter implements it", true
		}
		if !sameModule {
			return describe + " — the application layer composes its own domain and declares ports; another module is reached from an adapter, through its public commands, queries and events", true
		}
		if target.layer == layerContract {
			return describe + " — contract is the outer surface; application is what it composes", true
		}
	case layerAdapters:
		if sameModule {
			return "", false
		}
		switch target.module {
		case "platform", "ports", "i18n":
			return "", false
		}
		if publicSurfaceLayers[target.layer] {
			return "", false
		}
		// A foreign adapter is the composition root's business, and the wiring
		// test owns that message; staying silent here keeps one rule per fact.
		return "", false
	case layerContract:
		if sameModule && target.layer == layerAdapters {
			return describe + " — a public surface publishes commands, queries and events, not adapters", true
		}
		return "", false
	}

	switch unit.module {
	case "platform":
		if testOnlyPackage(unit.dir) {
			// The sibling gate proves these packages cannot be imported by
			// delivered code, so they may compose scenarios for tests.
			return "", false
		}
		switch target.module {
		case "platform", "ports", "i18n":
			return "", false
		}
		return describe + " — internal/platform is strictly technical (docs/ARCHITECTURE.md §7): it serves business modules, it does not depend on them", true
	case "ports", "i18n":
		return describe + " — " + unit.dir + " is a shared library with no internal dependency (only the standard library)", true
	}
	return "", false
}

// testOnlyPackage reports whether the directory belongs to the packages the
// sibling gate confines to test code.
func testOnlyPackage(dir string) bool {
	for _, packagePath := range testOnlyPackages {
		if dir == packagePath || strings.HasPrefix(dir, packagePath+"/") {
			return true
		}
	}
	return false
}

// wiringViolations judges the composition root: only bootstrap (and cmd/)
// wires adapters, and the exceptions are the reviewed projections.
func wiringViolations(packages []internalPackage) []string {
	violations := []string{}
	for _, unit := range packages {
		for _, importPath := range unit.imports {
			target, isInternal := classifyInternalPath(importPath)
			if !isInternal {
				continue
			}
			if target.module == "bootstrap" && unit.module != "bootstrap" && unit.module != "cmd" {
				violations = append(violations, fmt.Sprintf(
					"%s imports %s — the composition root is entered from cmd/ only", unit.dir, importPath))
			}
			if target.layer != layerAdapters || target.module == "bootstrap" {
				continue
			}
			wiredByRoot := unit.module == "bootstrap" || unit.module == "cmd"
			if wiredByRoot {
				continue
			}
			if exceptionReason(unit.dir, importPath) != "" {
				continue
			}
			if unit.module == target.module {
				violations = append(violations, fmt.Sprintf(
					"%s imports %s — an adapter does not instantiate a sibling adapter: the composition root wires both to the port it defines",
					unit.dir, importPath))
				continue
			}
			violations = append(violations, fmt.Sprintf(
				"%s imports %s — adapter %q of module %s is never wired by another adapter (that is a port bypass): declare the port, or add a reviewed exception with its reason",
				unit.dir, importPath, target.adapter, target.module))
		}
	}
	return violations
}

// exceptionReason returns the documented reason for an approved adapter import.
func exceptionReason(from, to string) string {
	for _, exception := range adapterImportExceptions {
		if exception.from == from && exception.to == to {
			return exception.reason
		}
	}
	return ""
}

// ledgerBookPairs are the unit vocabularies that must never meet in one
// delivered package: legacy wallet capacity (FREE_INK/PURCHASED_INK) and
// circulating Genesis money (milliINK), at domain and application level.
// The units travel inside application requests, so both layers share the
// ban: naming both types in one scope is what makes a cast, a sum or an
// alias between the books writable. A future opt-in conversion (P33-T04)
// coordinates the books through a new explicit port instead, amending
// this gate in its own task rather than slipping past it.
var ledgerBookPairs = [][2]string{
	{"internal/economy/domain", "internal/wallet/domain"},
	{"internal/economy/application", "internal/wallet/application"},
}

// ledgerIsolationViolations refuses any delivered package importing both
// sides of a ledger pair. The ports stay distinct by construction —
// wallet ports speak Ink, economy ports speak MilliInk — and this rule
// is what keeps them distinct: without both imports no code can convert
// one unit into the other, add them, or alias one as the other.
func ledgerIsolationViolations(packages []internalPackage) []string {
	violations := []string{}
	for _, unit := range packages {
		imported := map[string]bool{}
		for _, importPath := range unit.imports {
			imported[importPath] = true
		}
		for _, pair := range ledgerBookPairs {
			if imported[pair[0]] && imported[pair[1]] {
				violations = append(violations, fmt.Sprintf(
					"%s imports %s and %s — legacy capacity and Genesis money never meet: a cast, sum or alias between the books is refused",
					unit.dir, pair[0], pair[1]))
			}
		}
	}
	sort.Strings(violations)
	return violations
}

// ownedFile is one delivered Go file of the repository, used by the ownership
// rule. It is separate from internalPackage because the ownership of a
// dependency is judged over the whole repository, not only over the backend.
type ownedFile struct {
	path    string   // repo-relative, e.g. "internal/wallet/adapters/postgres/store.go"
	imports []string // every import, module path stripped where it matches
}

// scanOwnedFiles walks the repository and returns every delivered Go file.
func scanOwnedFiles(t *testing.T) []ownedFile {
	t.Helper()
	root := repositoryRoot(t)
	fset := token.NewFileSet()
	files := []ownedFile{}
	skipped := map[string]bool{".git": true, "node_modules": true, "vendor": true, ".local": true, "dist": true}

	walkErr := filepath.WalkDir(root, func(dir string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if skipped[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, dir, nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Errorf("parse %s: %v", dir, parseErr)
			return nil
		}
		rel, relErr := filepath.Rel(root, dir)
		if relErr != nil {
			return relErr
		}
		owned := ownedFile{path: filepath.ToSlash(rel)}
		for _, importSpec := range file.Imports {
			importPath, unquoteErr := strconv.Unquote(importSpec.Path.Value)
			if unquoteErr != nil {
				t.Errorf("%s: unparsable import %s", rel, importSpec.Path.Value)
				continue
			}
			owned.imports = append(owned.imports, strings.TrimPrefix(importPath, modulePrefix))
		}
		sort.Strings(owned.imports)
		files = append(files, owned)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk repository: %v", walkErr)
	}
	if len(files) == 0 {
		t.Fatal("no delivered Go file found: the ownership rule would judge nothing")
	}
	return files
}

// ownershipViolations judges every dependency that has a named owner.
func ownershipViolations(files []ownedFile) []string {
	violations := []string{}
	for _, file := range files {
		dir := path.Dir(filepath.ToSlash(file.path))
		for _, importPath := range file.imports {
			for _, dependency := range ownedDependencies {
				if !hasAnyPrefix(importPath, dependency.prefixes) {
					continue
				}
				if dependency.backendOnly && !backendPath(dir) {
					continue
				}
				if ownedBy(dir, dependency) {
					continue
				}
				violations = append(violations, fmt.Sprintf(
					"%s imports %s — %s: %s; allowed owners: %s",
					file.path, importPath, dependency.name, dependency.reason,
					strings.Join(append(dependency.owners, layerOwnerNames(dependency)...), ", ")))
			}
		}
	}
	return violations
}

// backendPath reports whether a directory belongs to the backend tree.
func backendPath(dir string) bool {
	return dir == "internal" || strings.HasPrefix(dir, "internal/") ||
		dir == "cmd" || strings.HasPrefix(dir, "cmd/")
}

// ownedBy reports whether a directory is an allowed owner of a dependency.
func ownedBy(dir string, dependency ownedDependency) bool {
	for _, pattern := range dependency.owners {
		if matched, err := path.Match(pattern, dir); err == nil && matched {
			return true
		}
	}
	if dependency.ownersByTransport {
		// The transport row is written in terms of layers and modules because
		// its owners are a shape ("the transport layers"), not a directory list;
		// every other row is a directory list, and an inbound adapter is not an
		// owner of the PostgreSQL driver.
		target, ok := classifyInternalPath(dir)
		if ok && (transportLayers[target.layer] || transportModules[target.module]) {
			return true
		}
	}
	return false
}

// layerOwnerNames spells out the layer/module owners of a dependency for the
// failure message, so the reader does not have to open the table.
func layerOwnerNames(dependency ownedDependency) []string {
	if !dependency.ownersByTransport {
		return nil
	}
	names := []string{"the adapters layer"}
	for module := range transportModules {
		names = append(names, "internal/"+module)
	}
	sort.Strings(names)
	return names
}

// hasAnyPrefix reports whether the import path starts with any of the prefixes.
func hasAnyPrefix(importPath string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(importPath, prefix) {
			return true
		}
	}
	return false
}

// moduleComponents returns the strongly connected components of the module
// graph, each one sorted, and only the ones with more than one module: a single
// module is not a cycle.
func moduleComponents(packages []internalPackage) [][]string {
	graph := map[string]map[string]bool{}
	for _, unit := range packages {
		if unit.module == "platform" || unit.module == "cmd" {
			continue
		}
		if graph[unit.module] == nil {
			graph[unit.module] = map[string]bool{}
		}
		for _, importPath := range unit.imports {
			target, isInternal := classifyInternalPath(importPath)
			if !isInternal || target.module == unit.module || target.module == "platform" || target.module == "cmd" {
				continue
			}
			if _, isModule := declaredModules[target.module]; !isModule {
				continue
			}
			graph[unit.module][target.module] = true
		}
	}

	index := map[string]int{}
	low := map[string]int{}
	onStack := map[string]bool{}
	stack := []string{}
	counter := 0
	components := [][]string{}

	var strongConnect func(node string)
	strongConnect = func(node string) {
		index[node] = counter
		low[node] = counter
		counter++
		stack = append(stack, node)
		onStack[node] = true
		neighbours := make([]string, 0, len(graph[node]))
		for neighbour := range graph[node] {
			neighbours = append(neighbours, neighbour)
		}
		sort.Strings(neighbours)
		for _, neighbour := range neighbours {
			if _, seen := index[neighbour]; !seen {
				strongConnect(neighbour)
				if low[neighbour] < low[node] {
					low[node] = low[neighbour]
				}
				continue
			}
			if onStack[neighbour] && index[neighbour] < low[node] {
				low[node] = index[neighbour]
			}
		}
		if low[node] != index[node] {
			return
		}
		component := []string{}
		for {
			last := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[last] = false
			component = append(component, last)
			if last == node {
				break
			}
		}
		if len(component) > 1 {
			sort.Strings(component)
			components = append(components, component)
		}
	}

	nodes := make([]string, 0, len(graph))
	for node := range graph {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)
	for _, node := range nodes {
		if _, seen := index[node]; !seen {
			strongConnect(node)
		}
	}
	sort.Slice(components, func(one, other int) bool {
		return strings.Join(components[one], ",") < strings.Join(components[other], ",")
	})
	return components
}

// unsanctionedCycleViolations reports the components of the module graph that
// nobody has sanctioned. The other half of the rule — an entry that outlived
// its cycle — is a property of the delivered tree, not of a fixture, so it is
// asserted where the real graph is read.
func unsanctionedCycleViolations(packages []internalPackage) []string {
	sanctioned := map[string]bool{}
	for _, entry := range sanctionedModuleCycles {
		sanctioned[entry] = true
	}
	violations := []string{}
	for _, component := range moduleComponents(packages) {
		key := strings.Join(component, ",")
		if !sanctioned[key] {
			violations = append(violations, fmt.Sprintf(
				"the module graph has an unsanctioned cycle: %s — break it with a port or an event, or add it to sanctionedModuleCycles with its reason",
				strings.Join(component, " ↔ ")))
		}
	}
	return violations
}

// missingSanctionedCycles reports the entries of sanctionedModuleCycles that the
// graph no longer has, so the list cannot become a rule about nothing.
func missingSanctionedCycles(packages []internalPackage) []string {
	found := map[string]bool{}
	for _, component := range moduleComponents(packages) {
		found[strings.Join(component, ",")] = true
	}
	violations := []string{}
	for _, entry := range sanctionedModuleCycles {
		if !found[entry] {
			violations = append(violations, fmt.Sprintf(
				"the sanctioned cycle %s no longer exists: remove it from sanctionedModuleCycles instead of keeping a rule about nothing", entry))
		}
	}
	return violations
}

// allViolations runs every rule, which is what the fixtures assert against: a
// fixture that trips two rules is still refused, and the message names the rule
// the test expects.
func allViolations(packages []internalPackage, files []ownedFile) []string {
	violations := []string{}
	violations = append(violations, layerViolations(packages)...)
	violations = append(violations, wiringViolations(packages)...)
	violations = append(violations, ownershipViolations(files)...)
	violations = append(violations, ledgerIsolationViolations(packages)...)
	violations = append(violations, unsanctionedCycleViolations(packages)...)
	return violations
}

// fixturePackage builds a synthetic package for the fixture tables.
func fixturePackage(dir string, imports ...string) internalPackage {
	unit := internalPackage{dir: dir, imports: imports}
	if target, ok := classifyInternalPath(dir); ok {
		unit.module, unit.layer, unit.adapter = target.module, target.layer, target.adapter
	}
	return unit
}

// fixtureFile builds a synthetic file for the ownership fixtures.
func fixtureFile(path string, imports ...string) ownedFile {
	return ownedFile{path: path, imports: imports}
}

// TestTheInternalTreeMatchesItsDeclaredShape judges the filesystem: internal/
// holds declared packages only, every declared module owns exactly its declared
// layers and each layer holds code.
func TestTheInternalTreeMatchesItsDeclaredShape(t *testing.T) {
	if violations := shapeViolations(scanTreeShape(t)); len(violations) > 0 {
		t.Fatalf("the internal tree does not match its declared shape:\n%s", strings.Join(violations, "\n"))
	}
}

// TestTheShapeRuleRefusesFixtures proves the filesystem rule has teeth: an
// undeclared top-level package, an undeclared layer directory and a layer
// declared without code are all refused.
func TestTheShapeRuleRefusesFixtures(t *testing.T) {
	fixtures := []struct {
		name   string
		shape  treeShape
		expect string
	}{
		{
			name: "an undeclared top-level package under internal/",
			shape: treeShape{
				topLevel: []string{"wallet", "toolbox"},
				goFiles:  map[string][]string{"wallet/domain": {"wallet.go"}, "toolbox": {"toolbox.go"}},
			},
			expect: "internal/toolbox is not declared",
		},
		{
			name: "a layer the module never declared",
			shape: treeShape{
				topLevel: []string{"wallet"},
				goFiles: map[string][]string{
					"wallet/domain":      {"wallet.go"},
					"wallet/application": {"use_cases.go"},
					"wallet/adapters":    {"store.go"},
					"wallet/helpers":     {"helpers.go"},
				},
			},
			expect: "internal/wallet/helpers is not a declared layer",
		},
		{
			name: "a declared layer with no code",
			shape: treeShape{
				topLevel: []string{"wallet"},
				goFiles: map[string][]string{
					"wallet/domain":      {"wallet.go"},
					"wallet/application": nil,
					"wallet/adapters":    {"store.go"},
				},
			},
			expect: "internal/wallet/application is declared but holds no Go file",
		},
	}
	for _, fixture := range fixtures {
		violations := shapeViolations(fixture.shape)
		if !containsSubstring(violations, fixture.expect) {
			t.Errorf("%s: the shape rule accepted it (want a violation mentioning %q, got %v)", fixture.name, fixture.expect, violations)
		}
	}

	// And the control: the declared shape of a small module passes.
	clean := treeShape{
		topLevel: []string{"wallet"},
		goFiles: map[string][]string{
			"wallet/domain":      {"wallet.go"},
			"wallet/application": {"use_cases.go"},
			"wallet/adapters":    {"store.go"},
		},
	}
	for _, violation := range shapeViolations(clean) {
		if strings.Contains(violation, "internal/wallet/") {
			t.Errorf("the shape rule refused a declared shape: %s", violation)
		}
	}
}

// TestLayerDependenciesPointInward judges the direction of the imports of the
// real tree.
func TestLayerDependenciesPointInward(t *testing.T) {
	packages := scanInternalPackages(t)
	if violations := layerViolations(packages); len(violations) > 0 {
		t.Fatalf("the dependency direction is broken:\n%s", strings.Join(violations, "\n"))
	}
}

// TestOnlyTheCompositionRootWiresAdapters judges who instantiates an adapter.
func TestOnlyTheCompositionRootWiresAdapters(t *testing.T) {
	packages := scanInternalPackages(t)
	if violations := wiringViolations(packages); len(violations) > 0 {
		t.Fatalf("adapters are wired outside the composition root:\n%s", strings.Join(violations, "\n"))
	}

	// Every exception has to be exercised, or it is a hole nobody reopened.
	for _, exception := range adapterImportExceptions {
		found := false
		for _, unit := range packages {
			if unit.dir != exception.from {
				continue
			}
			for _, importPath := range unit.imports {
				if importPath == exception.to {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("the exception %s → %s (%s) is not exercised by the tree: remove it instead of keeping a rule about nothing",
				exception.from, exception.to, exception.reason)
		}
	}
}

// TestTheBoundaryRulesRefuseFixtures is the task's own validation, made
// executable: a forbidden import, a cross-module access and an adapter
// instantiated outside the composition root all fail, each one with the message
// that names the rule.
func TestTheBoundaryRulesRefuseFixtures(t *testing.T) {
	fixtures := []struct {
		name     string
		packages []internalPackage
		files    []ownedFile
		expect   string
	}{
		{
			name: "a domain importing another module's domain",
			packages: []internalPackage{
				fixturePackage("internal/arenas/domain", "internal/identity/domain"),
			},
			expect: "the domain layer is the standard library plus its own module",
		},
		{
			name: "a domain importing its own application",
			packages: []internalPackage{
				fixturePackage("internal/arenas/domain", "internal/arenas/application"),
			},
			expect: "domain must not import application, adapters or contract",
		},
		{
			name: "an application importing a foreign adapter",
			packages: []internalPackage{
				fixturePackage("internal/search/application", "internal/identity/adapters/postgres"),
			},
			expect: "application must not import adapters",
		},
		{
			name: "an adapter importing a foreign adapter",
			packages: []internalPackage{
				fixturePackage("internal/search/adapters/http", "internal/identity/adapters/postgres"),
			},
			expect: "is never wired by another adapter",
		},
		{
			name: "an adapter importing its own sibling adapter",
			packages: []internalPackage{
				fixturePackage("internal/wallet/adapters/http", "internal/wallet/adapters/postgres"),
			},
			expect: "an adapter does not instantiate a sibling adapter",
		},
		{
			name: "an application importing the composition root",
			packages: []internalPackage{
				fixturePackage("internal/wallet/application", "internal/bootstrap"),
			},
			expect: "the composition root is entered from cmd/ only",
		},
		{
			name: "a layer importing the technical platform",
			packages: []internalPackage{
				fixturePackage("internal/wallet/application", "internal/platform/postgres"),
			},
			expect: "the application layer composes its own domain",
		},
		{
			name: "the platform depending on a business module",
			packages: []internalPackage{
				fixturePackage("internal/platform/httpcache", "internal/wallet/domain"),
			},
			expect: "internal/platform is strictly technical",
		},
		{
			name: "a domain importing the provider adapter (provider types leaking inward)",
			packages: []internalPackage{
				fixturePackage("internal/wallet/domain", "internal/billing/adapters/stripe"),
			},
			expect: "domain must not import adapters: providers and storage are details behind a port",
		},
		{
			name: "an application importing the provider SDK (provider types leaking inward)",
			files: []ownedFile{
				fixtureFile("internal/wallet/application/charge.go", "github.com/stripe/stripe-go/v78"),
			},
			expect: "the payment provider SDK",
		},
		{
			name: "an application importing the SQL driver",
			files: []ownedFile{
				fixtureFile("internal/wallet/application/store.go", "github.com/jackc/pgx/v5"),
			},
			expect: "the PostgreSQL driver",
		},
		{
			name: "an inbound adapter importing the SQL driver directly",
			files: []ownedFile{
				fixtureFile("internal/wallet/adapters/http/handler.go", "github.com/jackc/pgx/v5"),
			},
			expect: "the PostgreSQL driver",
		},
		{
			name: "a layer importing the transport package",
			files: []ownedFile{
				fixtureFile("internal/moderation/contract/contract.go", "net/http"),
			},
			expect: "the transport package",
		},
		{
			name: "a module cycle nobody sanctioned",
			packages: []internalPackage{
				fixturePackage("internal/wallet/adapters/http", "internal/billing/application"),
				fixturePackage("internal/billing/adapters/http", "internal/wallet/application"),
			},
			expect: "unsanctioned cycle",
		},
		{
			name: "a cycle closed by the core layers instead of an adapter",
			packages: []internalPackage{
				fixturePackage("internal/wallet/application", "internal/billing/domain"),
				fixturePackage("internal/billing/application", "internal/wallet/domain"),
			},
			expect: "unsanctioned cycle",
		},
	}

	for _, fixture := range fixtures {
		violations := allViolations(fixture.packages, fixture.files)
		if len(violations) == 0 {
			t.Errorf("%s: every rule accepted the fixture", fixture.name)
			continue
		}
		if !containsSubstring(violations, fixture.expect) {
			t.Errorf("%s: expected a violation mentioning %q, got %v", fixture.name, fixture.expect, violations)
		}
	}

	// The control: a fixture shaped like the sanctioned architecture passes
	// every rule, so the rules are not refusing everything.
	clean := []internalPackage{
		fixturePackage("internal/wallet/domain"),
		fixturePackage("internal/wallet/application", "internal/wallet/domain"),
		fixturePackage("internal/wallet/adapters/http", "internal/wallet/application", "internal/wallet/domain", "internal/platform/httpserver"),
		fixturePackage("internal/wallet/adapters/postgres", "internal/wallet/application", "internal/platform/postgres"),
		fixturePackage("internal/bootstrap", "internal/wallet/adapters/postgres", "internal/wallet/adapters/http"),
		fixturePackage("cmd/arena", "internal/bootstrap"),
	}
	cleanFiles := []ownedFile{
		fixtureFile("internal/wallet/adapters/postgres/store.go", "github.com/jackc/pgx/v5"),
		fixtureFile("internal/bootstrap/bootstrap.go", "github.com/jackc/pgx/v5/pgxpool"),
		fixtureFile("internal/wallet/adapters/http/handler.go", "net/http"),
		fixtureFile("internal/platform/httpserver/server.go", "net/http"),
		fixtureFile("cmd/arena/main.go", "net/http"),
	}
	if violations := allViolations(clean, cleanFiles); len(violations) > 0 {
		t.Errorf("the rules refused an architecture that follows them: %v", violations)
	}
}

// TestLedgerBooksStayDisjoint judges the filesystem: no delivered package
// imports both the legacy wallet units and the Genesis economy units, so
// no scope can cast, sum or alias one book as the other.
func TestLedgerBooksStayDisjoint(t *testing.T) {
	if violations := ledgerIsolationViolations(scanInternalPackages(t)); len(violations) > 0 {
		t.Fatalf("the ledgers meet in delivered code:\n%s", strings.Join(violations, "\n"))
	}
}

// TestLedgerIsolationRefusesFixtures proves the disjointness rule has
// teeth in both directions and at both layers, and that single-book
// packages — including the legacy consumers that predate the economy —
// keep passing.
func TestLedgerIsolationRefusesFixtures(t *testing.T) {
	refused := []struct {
		name     string
		packages []internalPackage
	}{
		{
			name: "a domain mixing legacy and Genesis units",
			packages: []internalPackage{
				fixturePackage("internal/treasury/domain", "internal/economy/domain", "internal/wallet/domain"),
			},
		},
		{
			name: "a use case mixing legacy and Genesis ports",
			packages: []internalPackage{
				fixturePackage("internal/treasury/application", "internal/economy/application", "internal/wallet/application"),
			},
		},
		{
			name: "an adapter mixing both journals",
			packages: []internalPackage{
				fixturePackage("internal/treasury/adapters/postgres", "internal/economy/domain", "internal/wallet/domain"),
			},
		},
	}
	for _, fixture := range refused {
		if violations := ledgerIsolationViolations(fixture.packages); len(violations) == 0 {
			t.Errorf("%s: the isolation rule accepted the fixture", fixture.name)
		}
	}

	clean := []internalPackage{
		fixturePackage("internal/economy/domain"),
		fixturePackage("internal/economy/application", "internal/economy/domain"),
		fixturePackage("internal/economy/adapters/postgres", "internal/economy/application", "internal/platform/postgres"),
		fixturePackage("internal/wallet/domain"),
		fixturePackage("internal/billing/adapters/wallet", "internal/wallet/domain"),
		fixturePackage("internal/arguments/adapters/walletdebit", "internal/wallet/domain"),
	}
	if violations := ledgerIsolationViolations(clean); len(violations) > 0 {
		t.Errorf("the isolation rule refused single-book packages: %v", violations)
	}
}

// TestOwnedDependenciesStayWithTheirOwners judges the homologated dependencies
// over the repository: the SQL driver, the migration tool, the provider SDK,
// the cryptography package, the Unicode library and the transport package.
//
// It replaces the stripe-only scan of the sibling file with the table above:
// the payment SDK keeps the same owners (its adapter and the composition root)
// and the other rows extend the same rule to the details whose owner was
// implicit.
func TestOwnedDependenciesStayWithTheirOwners(t *testing.T) {
	files := scanOwnedFiles(t)
	if violations := ownershipViolations(files); len(violations) > 0 {
		t.Fatalf("a homologated dependency left its owner:\n%s", strings.Join(violations, "\n"))
	}

	// A rule that nobody exercises is a comment: every row has to have an
	// importer and an owner in the tree.
	for _, dependency := range ownedDependencies {
		importers := 0
		for _, file := range files {
			dir := path.Dir(filepath.ToSlash(file.path))
			if dependency.backendOnly && !backendPath(dir) {
				continue
			}
			for _, importPath := range file.imports {
				if hasAnyPrefix(importPath, dependency.prefixes) && ownedBy(dir, dependency) {
					importers++
					break
				}
			}
		}
		if importers == 0 {
			t.Errorf("no package imports %s (%s): the row describes nothing, so remove it instead of keeping a rule about nothing",
				dependency.name, strings.Join(dependency.prefixes, ", "))
		}
	}

	// The other half of the provider rule (P12-T03): the owning adapter is
	// really what imports the SDK, so a rename cannot make the gate vacuous.
	stripeOwned := false
	for _, file := range files {
		if strings.HasPrefix(filepath.ToSlash(file.path), "internal/billing/adapters/stripe/") {
			for _, importPath := range file.imports {
				if strings.HasPrefix(importPath, "github.com/stripe/stripe-go") {
					stripeOwned = true
				}
			}
		}
	}
	if !stripeOwned {
		t.Error("the payment provider adapter does not import the provider SDK: the ownership row would be vacuous")
	}
}

// TestTheModuleGraphHasNoUnsanctionedCycle judges the conceptual circular
// dependency: the components of the module graph have to be exactly the ones
// the tree is allowed to have.
func TestTheModuleGraphHasNoUnsanctionedCycle(t *testing.T) {
	packages := scanInternalPackages(t)
	if violations := unsanctionedCycleViolations(packages); len(violations) > 0 {
		t.Fatalf("the module graph carries an undocumented cycle:\n%s", strings.Join(violations, "\n"))
	}
	if missing := missingSanctionedCycles(packages); len(missing) > 0 {
		t.Fatalf("the sanctioned cycle list is out of date:\n%s", strings.Join(missing, "\n"))
	}
	if len(sanctionedModuleCycles) == 0 {
		t.Fatal("no sanctioned cycle is declared: either the core is acyclic, and the rule belongs in the fixtures, or the list is missing an entry")
	}
}

// containsSubstring reports whether any violation mentions the expected text.
func containsSubstring(violations []string, expected string) bool {
	for _, violation := range violations {
		if strings.Contains(violation, expected) {
			return true
		}
	}
	return false
}
