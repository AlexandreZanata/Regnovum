// Package architecture_test enforces the backend dependency direction.
//
// Domain and application layers must depend only on the Go standard library
// (minus transport and persistence details) and on their own module packages.
// Adapters, platform, bootstrap, cross-module internals and unapproved
// external dependencies are forbidden in these layers, per
// docs/ARCHITECTURE.md (Ports and Adapters) and AGENTS.md. Adding an approved
// dependency to a layer requires an ADR and a matching change here.
//
// Since P02-T01, direct process-environment reads are gated to the typed
// configuration package (and cmd/bootstrap when it appears). Since P02-T02
// (ADR-012), time.Now and crypto/math randomness readers are gated to
// internal/platform/clockseed and future adapters that document the need.
// Since P22-T02 (ADR-015) the gate judges the expression rather than the call —
// `Now: time.Now` is the same read as `time.Now()` — identifier libraries join
// the vocabulary, and the deterministic sources of internal/platform/testsource
// are the second allowlisted owner, proven unreachable from delivered code.
package architecture_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/AlexandreZanata/Regnovum"

const modulePrefix = modulePath + "/"

// forbiddenStdlib lists standard library packages that are transport,
// persistence or serialization details. Domain and application layers must
// not import them.
var forbiddenStdlib = map[string]string{
	"database/sql":      "persistence detail",
	"encoding/json":     "serialization detail",
	"html/template":     "transport detail",
	"net/http":          "transport detail",
	"net/http/httputil": "transport detail",
	"net/url":           "transport detail",
}

// forbiddenExternal prefixes are dependencies not approved for domain and
// application layers. Approved adapters (pgx, sqlc, goose, x/crypto, Stripe,
// Resend, Sentry, PostHog) live exclusively in adapter packages. The
// grapheme segmentation library is approved by ADR-013 for the platform
// owner internal/platform/text only: domain and application stay
// standard-library only and consume it through ports.
var forbiddenExternal = []string{
	"github.com/golang/",
	"github.com/getsentry/",
	"github.com/jackc/",
	"github.com/posthog/",
	"github.com/pressly/",
	"github.com/resend/",
	"github.com/rivo/",
	"github.com/sqlc-dev/",
	"github.com/stripe/",
	"golang.org/x/",
}

// The P12-T03 rule for the payment provider SDK ("busca confirma imports Stripe
// somente no adapter/bootstrap") is now one row of the ownership table in
// architecture_boundaries_test.go, which judges the provider SDK, the SQL
// driver, the migration tool, the cryptography package and the Unicode library
// with the same code instead of five scans that drift apart.

// businessModules are the module directories required by the plan and
// docs/ARCHITECTURE.md §4.
var businessModules = []string{
	"arenas", "arguments", "audit", "billing", "economy", "identity", "jobs",
	"metering", "moderation", "persuasion", "positions", "pricing", "profiles",
	"transparency", "wallet",
}

// envReadAllowlist lists the internal packages allowed to touch the process
// environment directly, per the P02-T01 gate ("nenhum package lê os.Getenv
// fora de config/bootstrap"). cmd/bootstrap joins this list when it appears.
//
// internal/platform/testsource reads exactly one variable (ARENA_TEST_SEED, the
// seed of a test run, P22-T02) and nothing else, and no delivered file may
// import it — the test below proves that — so the read cannot reach a process.
// It is listed here rather than reading the environment in a test file because
// a helper nobody can share is a helper every test package reimplements.
//
// internal/platform/testguard (P22-T06) is the third owner, and it owns the
// other half of the same effect: it sets a variable for one test and restores
// it, and it compares the whole environment at the end of the test so that a
// mutation somebody left behind is a failure instead of a suite that depends on
// the order its tests ran in. No delivered file may import it either, proven by
// the same test, so the read cannot reach a process.
var envReadAllowlist = map[string]bool{
	"internal/platform/config":     true,
	"internal/platform/testsource": true,
	"internal/platform/testguard":  true,
}

// clockAndRandomAllowlist lists the internal packages allowed to read the wall
// clock and the randomness readers directly, per the P02-T02 gate and ADR-012.
// Adapters join this list when their ADR documents the need.
//
// It has exactly two owners, and the second one exists because of the phase
// that made the test platform honest (P22-T02): a deterministic source of time
// and entropy for tests is itself an implementation of the effect, so it cannot
// borrow the production one without making every "deterministic" scenario read
// the real clock. Nothing else joins: `internal/platform/testsource` is test
// support and no delivered process imports it.
var clockAndRandomAllowlist = map[string]bool{
	"internal/platform/clockseed":  true,
	"internal/platform/testsource": true,
}

// effectPackages maps import paths to the kind of effect they carry, used by
// the P02-T02 gate and extended by P22-T02 with the identifier libraries.
//
// The identifier libraries are listed for the same reason the entropy readers
// are: a value they generate is entropy wearing a name. The product's
// identifiers are opaque strings made by ports.IDGenerator (production:
// clockseed.RandomIDs), so a layer that needs one receives it as a parameter
// and a scenario that needs a fixed one takes it from testsource.NewIDs. An
// adapter with a need it can document joins the allowlist above — the escape
// hatch this map already has for the clock.
var effectPackages = map[string]string{
	"time":                      "time",
	"math/rand":                 "rand",
	"math/rand/v2":              "rand",
	"crypto/rand":               "rand",
	"github.com/google/uuid":    "uuid",
	"github.com/gofrs/uuid":     "uuid",
	"github.com/gofrs/uuid/v5":  "uuid",
	"github.com/satori/go.uuid": "uuid",
}

// effectRemedy names, per kind, what the caller should do instead. The remedy
// depends on the kind because the ports do: a clock read is answered by
// ports.Clock, a randomness read by ports.Random, and a generated identifier by
// ports.IDGenerator.
var effectRemedy = map[string]string{
	"time": "inject the ports.Clock port instead (P02-T02, ADR-012)",
	"rand": "inject the ports.Random port instead (P02-T02, ADR-012)",
	"uuid": "create identifiers through ports.IDGenerator instead (P22-T02, ADR-015)",
}

// clockReads are the identifiers of the `time` package that read the current
// instant, and the whole of what the gate forbids. It is a closed set on
// purpose (P22-T02):
//
//   - `Now` counts as a read whether it is called or handed over as a value,
//     because `Now: time.Now` on a struct field is the same wall-clock read as
//     `time.Now()` — and it was the shape that slipped through the first
//     version of this gate, in four packages at once.
//   - `Since` and `Until` are `Now` in disguise: they subtract the wall clock
//     from an injected instant, so a rule that decides with them cannot be
//     replayed with a fixed clock.
//
// Everything else the package offers is either pure (`Unix`, `Parse`, `Add`,
// `Sub`, `Before`, `After`, durations) or waiting (`Sleep`, `After`, `NewTimer`,
// `NewTicker`, `Tick`, `AfterFunc`) — and waiting is not a reading: a timer
// pauses a goroutine without letting any decision depend on what the clock
// says. Forbidding those would be forbidding `time.Sleep`, which would trade a
// real guarantee for a wrong one.
var clockReads = map[string]bool{"Now": true, "Since": true, "Until": true}

// waitingEffects are the `time` calls the gate deliberately allows, kept as a
// list so that the test below can prove the allowance is exercised by the
// delivered tree instead of being a comment about nothing.
var waitingEffects = []string{"Sleep", "After", "NewTimer", "NewTicker", "Tick", "AfterFunc"}

func repositoryRoot(t *testing.T) string {
	t.Helper()

	// This test lives in internal/; the repository root is one level above.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	root := filepath.Dir(wd)

	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	if !strings.Contains(string(data), "module "+modulePath) {
		t.Fatalf("go.mod does not declare module %s", modulePath)
	}
	return root
}

// forbiddenImportForDomainOrApplication reports whether the given import is
// forbidden for domain/application code of the given module, and why.
func forbiddenImportForDomainOrApplication(module, importPath string) (string, bool) {
	switch {
	case strings.HasPrefix(importPath, modulePrefix+"internal/"):
		rest := strings.TrimPrefix(importPath, modulePrefix+"internal/")
		parts := strings.Split(rest, "/")
		switch parts[0] {
		case "platform":
			return "domain/application must not import internal/platform", true
		case "bootstrap":
			return "domain/application must not import internal/bootstrap", true
		case module:
			if len(parts) >= 2 && parts[1] == "adapters" {
				return "domain/application must not import adapters; application defines ports and adapters implement them", true
			}
			return "", false
		default:
			return "cross-module imports are forbidden outside publicly exposed commands, queries and events", true
		}
	case strings.HasPrefix(importPath, modulePrefix):
		return "domain/application must not import cmd/", true
	}

	if reason, forbidden := forbiddenStdlib[importPath]; forbidden {
		return "domain/application must not import " + reason, true
	}
	for _, prefix := range forbiddenExternal {
		if strings.HasPrefix(importPath, prefix) {
			return "unapproved external dependency in domain/application (requires ADR and an adapter)", true
		}
	}
	return "", false
}

// importAliases maps the local package name of each import to its effect
// kind, honoring import aliases such as `cryptorand "crypto/rand"`.
func importAliases(source *ast.File) map[string]string {
	aliases := make(map[string]string)
	for _, importSpec := range source.Imports {
		importPath, err := strconv.Unquote(importSpec.Path.Value)
		if err != nil {
			continue
		}
		kind, isEffect := effectPackages[importPath]
		if !isEffect {
			continue
		}
		localName := importPath[strings.LastIndex(importPath, "/")+1:]
		if importSpec.Name != nil {
			localName = importSpec.Name.Name
		}
		aliases[localName] = kind
	}
	return aliases
}

// effectViolation reports direct clock, randomness or identifier-generation
// access outside the allowlisted packages (P02-T02, ADR-012; P22-T02, ADR-015).
//
// It inspects a selector expression rather than a call, which is the difference
// between a gate and a gesture: `time.Now()` and `Now: time.Now` are one read
// expressed two ways, and only the second one used to get through.
func effectViolation(expression ast.Expr, aliases map[string]string, pkgDir string) (string, bool) {
	selector, isSelector := expression.(*ast.SelectorExpr)
	if !isSelector {
		return "", false
	}
	ident, isIdent := selector.X.(*ast.Ident)
	if !isIdent {
		return "", false
	}
	kind, isEffect := aliases[ident.Name]
	if !isEffect {
		return "", false
	}
	if kind == "time" && !clockReads[selector.Sel.Name] {
		// Constructing values from injected instants (time.Unix, .Add, ...) is
		// pure, and waiting (time.Sleep, time.NewTimer, ...) is not a reading.
		return "", false
	}
	if clockAndRandomAllowlist[pkgDir] {
		return "", false
	}
	// Unlike `time`, an identifier library has no pure half to carve out: its
	// constructors are the effect, and its parsers exist so a caller can hold a
	// library type where the product holds an opaque string. Every selector of
	// one is therefore a violation outside the allowlist.
	remedy := effectRemedy[kind]
	if remedy == "" {
		remedy = "inject the port instead"
	}
	return fmt.Sprintf(
		"%s.%s reads the %s effect directly — %s",
		ident.Name, selector.Sel.Name, kind, remedy,
	), true
}

// envReadViolation inspects one expression for direct environment access and
// reports a violation message when the containing package is not allowlisted.
func envReadViolation(expression ast.Expr, pkgDir string) (string, bool) {
	call, isCall := expression.(*ast.CallExpr)
	if !isCall {
		return "", false
	}
	selector, isSelector := call.Fun.(*ast.SelectorExpr)
	if !isSelector {
		return "", false
	}
	ident, isIdent := selector.X.(*ast.Ident)
	if !isIdent || ident.Name != "os" {
		return "", false
	}
	switch selector.Sel.Name {
	// Environ is in the vocabulary for the same reason the others are, and it
	// was missing until P22-T06: reading the whole environment is reading it,
	// and the guard that compares two snapshots of it is what made the hole
	// visible (the fixture that closes it lives in
	// internal/platform/testguard).
	case "Getenv", "LookupEnv", "Setenv", "Unsetenv", "Clearenv", "Expandenv", "Environ":
	default:
		return "", false
	}
	if envReadAllowlist[pkgDir] {
		return "", false
	}
	return fmt.Sprintf(
		"os.%s reads the process environment directly — read it only in the typed config package (P02-T01 gate)",
		selector.Sel.Name,
	), true
}

func TestDependencyDirectionAndEnvironmentGate(t *testing.T) {
	root := repositoryRoot(t)
	internalDir := filepath.Join(root, "internal")
	fset := token.NewFileSet()

	var violations []string
	walkErr := filepath.WalkDir(internalDir, func(path string, dirEntry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if dirEntry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		pkgDir := filepath.Dir(filepath.ToSlash(relPath))

		source, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Errorf("parse %s: %v", filepath.ToSlash(relPath), err)
			return nil
		}

		// Environment gate applies to every internal package.
		ast.Inspect(source, func(node ast.Node) bool {
			if expression, isExpression := node.(ast.Expr); isExpression {
				if rule, forbidden := envReadViolation(expression, pkgDir); forbidden {
					violations = append(violations, fmt.Sprintf("%s — %s", filepath.ToSlash(relPath), rule))
				}
			}
			return true
		})

		// Dependency direction applies to domain and application layers.
		segments := strings.Split(filepath.ToSlash(relPath), "/")
		if len(segments) < 3 {
			return nil
		}
		layer := segments[2]
		if layer != "domain" && layer != "application" {
			return nil
		}
		module := segments[1]
		for _, importSpec := range source.Imports {
			importPath, err := strconv.Unquote(importSpec.Path.Value)
			if err != nil {
				t.Errorf("%s: unparsable import %s", filepath.ToSlash(relPath), importSpec.Path.Value)
				continue
			}
			if rule, forbidden := forbiddenImportForDomainOrApplication(module, importPath); forbidden {
				violations = append(violations, fmt.Sprintf(
					"%s (%s) imports %q — %s",
					filepath.ToSlash(relPath), layer, importPath, rule,
				))
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk internal/: %v", walkErr)
	}

	if len(violations) > 0 {
		t.Fatalf("architecture violations:\n%s", strings.Join(violations, "\n"))
	}
}

// TestNoDirectClockOrRandomOutsidePlatform automates the P02-T02 validation
// (the `rg 'time\.Now|rand\.' internal` search): non-test internal files may
// only read the wall clock or the randomness readers inside documented
// allowlisted packages. Test files are exempt — they define the
// deterministic stubs.
//
// Since P22-T02 the search is a parse, not a grep: it covers `time.Now` handed
// over as a value, `time.Since`/`time.Until` (which read the clock without
// naming it) and every selector of a randomness reader, and it deliberately
// leaves `time.Sleep` and the timers alone, because waiting is not reading.
func TestNoDirectClockOrRandomOutsidePlatform(t *testing.T) {
	root := repositoryRoot(t)
	internalDir := filepath.Join(root, "internal")
	fset := token.NewFileSet()

	var violations []string
	walkErr := filepath.WalkDir(internalDir, func(path string, dirEntry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if dirEntry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		pkgDir := filepath.Dir(filepath.ToSlash(relPath))

		source, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Errorf("parse %s: %v", filepath.ToSlash(relPath), err)
			return nil
		}
		aliases := importAliases(source)

		ast.Inspect(source, func(node ast.Node) bool {
			if expression, isExpression := node.(ast.Expr); isExpression {
				if rule, forbidden := effectViolation(expression, aliases, pkgDir); forbidden {
					violations = append(violations, fmt.Sprintf("%s — %s", filepath.ToSlash(relPath), rule))
				}
			}
			return true
		})
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk internal/: %v", walkErr)
	}

	if len(violations) > 0 {
		t.Fatalf("clock/random gate violations:\n%s", strings.Join(violations, "\n"))
	}
}

// TestTheEffectGateLeavesWaitingAlone proves the allowance above is exercised:
// the delivered tree uses timers to wait (the job worker between polls, the
// circuit breaker between attempts, the telemetry sink between flushes), and a
// gate that forbade them would be a gate somebody has to weaken. Without this
// test the allowance could be a rule about code that no longer exists, and the
// next person would delete it and then reintroduce the ban on `time.Sleep`.
func TestTheEffectGateLeavesWaitingAlone(t *testing.T) {
	root := repositoryRoot(t)
	internalDir := filepath.Join(root, "internal")
	fset := token.NewFileSet()

	seen := map[string]string{}
	walkErr := filepath.WalkDir(internalDir, func(path string, dirEntry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if dirEntry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		source, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			t.Errorf("parse %s: %v", filepath.ToSlash(relPath), parseErr)
			return nil
		}
		aliases := importAliases(source)
		allowed := map[string]bool{}
		for _, name := range waitingEffects {
			allowed[name] = true
		}
		ast.Inspect(source, func(node ast.Node) bool {
			selector, isSelector := node.(*ast.SelectorExpr)
			if !isSelector {
				return true
			}
			ident, isIdent := selector.X.(*ast.Ident)
			if !isIdent || aliases[ident.Name] != "time" || !allowed[selector.Sel.Name] {
				return true
			}
			seen[selector.Sel.Name] = filepath.ToSlash(relPath)
			return true
		})
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk internal/: %v", walkErr)
	}
	if len(seen) == 0 {
		t.Fatalf("no internal package waits with the time package: the allowance in this gate describes nothing, so remove it instead of keeping a rule about nothing")
	}
}

// TestTheEffectGateItself drives the gate over synthetic sources: one table of
// sources that must be refused and one of sources that must pass. The delivered
// tree passing is an observation; this is the proof that the checker has teeth,
// and it is what fails when somebody relaxes the rule to make a package build.
func TestTheEffectGateItself(t *testing.T) {
	refused := []struct {
		source string
		reason string
	}{
		{source: `package sample
import "time"
func read() time.Time { return time.Now() }`, reason: "calling the wall clock"},
		{source: `package sample
import "time"
var now = time.Now
func read() time.Time { return now() }`, reason: "handing the wall clock over as a value"},
		{source: `package sample
import "time"
func waited(instant time.Time) bool { return time.Since(instant) > time.Minute }`, reason: "subtracting the wall clock from an injected instant"},
		{source: `package sample
import "time"
func left(deadline time.Time) time.Duration { return time.Until(deadline) }`, reason: "measuring against the wall clock"},
		{source: `package sample
import "crypto/rand"
func entropy() []byte { buffer := make([]byte, 8); rand.Read(buffer); return buffer }`, reason: "reading the entropy source"},
		{source: `package sample
import cryptorand "crypto/rand"
var reader = cryptorand.Reader`, reason: "aliasing the entropy source and holding the reader"},
		{source: `package sample
import "math/rand"
func roll() int { return rand.Intn(6) }`, reason: "the global pseudo-random source"},
		{source: `package sample
import "github.com/google/uuid"
func newID() string { return uuid.NewString() }`, reason: "generating an identifier with a library instead of ports.IDGenerator"},
		{source: `package sample
import "github.com/google/uuid"
var make = uuid.New
func newID() uuid.UUID { return make() }`, reason: "handing an identifier generator over as a value"},
		{source: `package sample
import guuid "github.com/satori/go.uuid"
func newID() guuid.UUID { return guuid.NewV4() }`, reason: "aliasing an identifier library and generating with it"},
	}
	for _, testCase := range refused {
		violations := effectViolationsInSource(t, testCase.source, "internal/sample/application")
		if len(violations) == 0 {
			t.Errorf("the gate accepted a source that %s", testCase.reason)
		}
	}

	accepted := []struct {
		source string
		reason string
	}{
		{source: `package sample
import "time"
func add(instant time.Time) time.Time { return instant.Add(time.Minute) }`, reason: "arithmetic on an injected instant"},
		{source: `package sample
import "time"
func from(seconds int64) time.Time { return time.Unix(seconds, 0) }`, reason: "building an instant from a value"},
		{source: `package sample
import "time"
func compare(one, other time.Time) bool { return one.Before(other) }`, reason: "comparing two injected instants"},
		{source: `package sample
import "time"
func pause(d time.Duration) { time.Sleep(d) }`, reason: "waiting, which decides nothing"},
		{source: `package sample
import "time"
func wait(d time.Duration) <-chan time.Time { return time.After(d) }`, reason: "waiting on a channel"},
		{source: `package sample
import "time"
func tick() *time.Ticker { return time.NewTicker(time.Second) }`, reason: "a ticker, which pauses instead of reading"},
		{source: `package sample
import "time"
func deadline(seconds int64) time.Time { return time.Unix(seconds, 0).Add(time.Hour) }`, reason: "a deadline computed from a value"},
	}
	for _, testCase := range accepted {
		if violations := effectViolationsInSource(t, testCase.source, "internal/sample/application"); len(violations) > 0 {
			t.Errorf("the gate refused a source that only %s: %v", testCase.reason, violations)
		}
	}

	// And the other half of the rule: the same source is refused in an ordinary
	// package and admitted in an allowlisted one, so the allowlist is what the
	// gate really turns on.
	clockSource := `package sample
import "time"
func read() time.Time { return time.Now() }`
	if violations := effectViolationsInSource(t, clockSource, "internal/platform/clockseed"); len(violations) > 0 {
		t.Errorf("the gate refused the package that owns the effect: %v", violations)
	}
	if violations := effectViolationsInSource(t, clockSource, "internal/platform/testsource"); len(violations) > 0 {
		t.Errorf("the gate refused the deterministic test source: %v", violations)
	}
}

// effectViolationsInSource runs the delivered checker over one synthetic source,
// so the rules are exercised by the same code that judges the tree.
func effectViolationsInSource(t *testing.T, source, pkgDir string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "sample.go", source, 0)
	if err != nil {
		t.Fatalf("the fixture source does not parse: %v", err)
	}
	aliases := importAliases(file)
	violations := []string{}
	ast.Inspect(file, func(node ast.Node) bool {
		if expression, isExpression := node.(ast.Expr); isExpression {
			if rule, forbidden := effectViolation(expression, aliases, pkgDir); forbidden {
				violations = append(violations, rule)
			}
		}
		return true
	})
	return violations
}

// testOnlyPackages are the packages of the test platform that no delivered
// process may import: the deterministic sources of P22-T02, which answer with a
// seeded stream where production reads the system clock and crypto/rand, the
// scenario builders of P22-T03, which exist to compose tests, the provider
// simulators of P22-T05, which stand in for Stripe, Resend, Turnstile, Sentry
// and PostHog and would answer a call the product believes it made to a
// provider, and the lifecycle guard of P22-T06, which closes resources and
// reports leaks — a guard in the delivered process would be a process that
// reports about itself.
//
// They are listed here rather than trusted by convention because the failure
// they guard against is silent: a builder imported by an adapter would compile,
// pass every test, and answer deterministic values in production.
var testOnlyPackages = []string{
	"internal/platform/testsource",
	"internal/platform/testsupport",
	"internal/platform/providersim",
	"internal/platform/testguard",
}

// TestTheTestOnlyPackagesStayInTests is what keeps the test platform from
// becoming a way to ship a fake: no non-test file under internal/ (outside the
// package itself) and nothing under cmd/ may import a package listed above. A
// test may, which is the whole point — that is how a scenario stops depending
// on the machine it runs on.
func TestTheTestOnlyPackagesStayInTests(t *testing.T) {
	root := repositoryRoot(t)
	fset := token.NewFileSet()

	forbidden := map[string]string{}
	for _, packagePath := range testOnlyPackages {
		forbidden[modulePrefix+packagePath] = packagePath
	}
	violations := []string{}

	for _, directory := range []string{filepath.Join(root, "internal"), filepath.Join(root, "cmd")} {
		walkErr := filepath.WalkDir(directory, func(path string, dirEntry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if dirEntry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				// Test files are the only place the packages are allowed, so they
				// are skipped instead of inspected.
				return nil
			}
			relPath, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			relPath = filepath.ToSlash(relPath)
			owner := false
			for _, packagePath := range testOnlyPackages {
				if strings.HasPrefix(relPath, packagePath+"/") {
					owner = true
				}
			}
			if owner {
				return nil
			}
			source, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if parseErr != nil {
				t.Errorf("parse %s: %v", relPath, parseErr)
				return nil
			}
			for _, importSpec := range source.Imports {
				importPath, unquoteErr := strconv.Unquote(importSpec.Path.Value)
				if unquoteErr != nil {
					t.Errorf("%s: unparsable import %s", relPath, importSpec.Path.Value)
					continue
				}
				if packagePath, isTestOnly := forbidden[importPath]; isTestOnly {
					violations = append(violations, fmt.Sprintf("%s imports %s — it answers test scenarios and belongs to tests only (P22-T02, P22-T03)", relPath, packagePath))
				}
			}
			return nil
		})
		if walkErr != nil {
			t.Fatalf("walk %s: %v", directory, walkErr)
		}
	}

	if len(violations) > 0 {
		t.Fatalf("the test platform escaped into delivered code:\n%s", strings.Join(violations, "\n"))
	}

	// And the other direction: every package has to exist and to declare a
	// source, or this gate would pass over a tree that no longer has them.
	for _, packagePath := range testOnlyPackages {
		directory := filepath.Join(append([]string{root}, strings.Split(packagePath, "/")...)...)
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatalf("the test-only package %s is missing: %v", packagePath, err)
		}
		found := false
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") && !strings.HasSuffix(entry.Name(), "_test.go") {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s declares no source: the gate above would accept anything", packagePath)
		}
	}
}

func TestBusinessModulesExist(t *testing.T) {
	root := repositoryRoot(t)
	moduleNamePattern := regexp.MustCompile(`const ModuleName = "([^"]+)"`)
	packageNamePattern := regexp.MustCompile(`(?m)^package (\w+)$`)

	seen := make(map[string]bool, len(businessModules))
	for _, module := range businessModules {
		moduleFile := filepath.Join(root, "internal", module, module+".go")
		data, err := os.ReadFile(moduleFile)
		if err != nil {
			t.Errorf("module %s: read %s: %v", module, filepath.ToSlash(moduleFile), err)
			continue
		}
		content := string(data)

		match := packageNamePattern.FindStringSubmatch(content)
		if match == nil || match[1] != module {
			t.Errorf("module %s: file %s.go must declare package %s", module, module, module)
		}
		match = moduleNamePattern.FindStringSubmatch(content)
		if match == nil {
			t.Errorf("module %s: missing ModuleName constant in %s.go", module, module)
			continue
		}
		if match[1] != module {
			t.Errorf("module %s: ModuleName is %q, want %q", module, match[1], module)
		}
		if seen[match[1]] {
			t.Errorf("module %s: duplicate ModuleName %q", module, match[1])
		}
		seen[match[1]] = true
	}
}
