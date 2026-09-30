// Frontend audit of the delivered build (P18-T08).
//
// docs/FRONTEND.md section 11 declares budgets, and a budget that nothing
// measures is a wish. This file measures the build as it is served: the
// manifest, its modules and its stylesheets, addressed exactly as the browser
// addresses them.
//
// Five questions, each with the artefact it interrogates:
//
//  1. How many compressed bytes does the initial JavaScript of a public page
//     cost, and how many the initial CSS?   (every module of the page closure,
//     every stylesheet of the build)
//  2. Does any module of the delivered graph import something that is not part
//     of it — an external origin, a bare package name, a path the manifest does
//     not publish, or a path outside the build?   (the specifiers themselves)
//  3. Does the delivered code carry a construct the policy of the binary
//     refuses — `eval`, `new Function`, `document.write`, a `javascript:` URL,
//     an HTML sink — or does the policy itself allow inline code?
//     (docs/SECURITY.md section 4 through securityheaders)
//  4. Does a module outside `core/` reach the network directly?
//     (web/src/core owns the only fetch of the product)
//  5. Is the build complete enough to be measured at all — a manifest, the
//     files it declares, at least one page entry?  (the build directory)
//
// Two properties of the measurement are deliberate.
//
// It is per response, not per concatenation. The browser fetches one module and
// one stylesheet per request, each with its own compression stream, so the
// number that matters is the sum of the compressed sizes of the files a page
// actually pulls — never the size of a bundle, which no browser receives here
// because no bundler exists in this pipeline.
//
// And it fails closed. A file the manifest declares and the build does not have
// is an error, not a skip, because a gate that cannot see what it measures
// proves nothing.
package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/platform/assets"
	"github.com/AlexandreZanata/Regnovum/internal/platform/securityheaders"
)

// The initial budgets of docs/FRONTEND.md section 11, read as decimal
// kilobytes: 50 KB is 50,000 bytes and 40 KB is 40,000. "KB" has two readings
// and this is the stricter one, so the reading can never be used to buy
// headroom unnoticed.
const (
	pageJavaScriptBudget = 50_000
	initialCSSBudget     = 40_000
)

// coreDirectory owns the only network primitives of the product. The HTTP core
// is the single place that talks to the server, which is what makes one
// timeout, one CSRF header and one Problem Details reader the behaviour of
// every call instead of a convention repeated by each caller.
const coreDirectory = "core"

// pageDirectory holds the entry module of each public page. It mirrors what the
// server-rendered documents load — `<script type="module" src="{{asset
// "pages/...">}}` — and it is where a page budget has to start, because the
// template is what decides which modules a page pulls.
const pageDirectory = "pages"

// Report is the measurement of one build. Violations is empty when the build
// passes every check; the measurements are always present, because the point of
// a budget is the distance to it, not only the day it is crossed.
type Report struct {
	Build      string
	Assets     int
	Modules    int
	Styles     int
	Pages      []PageMeasure
	CSSBytes   int
	Violations []string
}

// PageMeasure is the initial JavaScript of one public page: the entry module
// plus everything it transitively imports, each compressed on its own.
type PageMeasure struct {
	Name    string
	Modules int
	Bytes   int
}

// Audit measures the build in directory against the budgets and the dependency
// rules of the frontend, and returns every violation it found.
//
// It returns an error only when the build cannot be measured — no manifest, a
// declared file that is missing, an asset kind this gate does not know. A build
// that can be measured and breaks a rule comes back as a Report with
// violations, because "the gate failed" and "the gate could not run" are two
// different things for whoever reads the CI log.
func Audit(directory string) (Report, error) {
	resolved, err := resolveBuild(directory)
	if err != nil {
		return Report{}, err
	}

	manifest, err := assets.LoadFile(resolved)
	if err != nil {
		return Report{}, err
	}

	names := make([]string, 0, len(manifest.Assets))
	for name := range manifest.Assets {
		names = append(names, name)
	}
	sort.Strings(names)

	modules := make(map[string]*module, len(names))
	styles := make(map[string]*stylesheet, len(names))
	for _, name := range names {
		if err := validAssetName(name); err != nil {
			return Report{}, err
		}
		body, err := os.ReadFile(filepath.Join(resolved, filepath.FromSlash(name)))
		if err != nil {
			return Report{}, fmt.Errorf("webaudit: read declared asset %q: %w", name, err)
		}
		delivered := deliverable{name: name, body: body, compressed: compressedSize(body)}
		switch path.Ext(name) {
		case ".js":
			modules[name] = &module{deliverable: delivered, code: stripComments(string(body))}
		case ".css":
			styles[name] = &stylesheet{deliverable: delivered, code: stripComments(string(body))}
		default:
			return Report{}, fmt.Errorf("webaudit: %q is not an asset kind this gate measures", name)
		}
	}

	pages, cssBytes, budgetViolations := measure(modules, styles)
	violations := append(budgetViolations, checkSpecifiers(modules)...)
	violations = append(violations, checkPolicyModules(modules, styles)...)
	violations = append(violations, checkNetworkReach(modules)...)
	sort.Strings(violations)

	return Report{
		Build:      resolved,
		Assets:     len(names),
		Modules:    len(modules),
		Styles:     len(styles),
		Pages:      pages,
		CSSBytes:   cssBytes,
		Violations: violations,
	}, nil
}

// deliverable is what every measured asset carries: the name the graph resolves,
// the bytes as delivered and the size those bytes cost compressed.
type deliverable struct {
	name       string
	body       []byte
	compressed int
}

// module is one delivered ES module, with its comments blanked out for the
// scans that must read code rather than documentation.
type module struct {
	deliverable
	code string
}

// stylesheet is one delivered stylesheet, measured as delivered.
type stylesheet struct {
	deliverable
	code string
}

// resolveBuild names the directory the gate is about, so the report and the
// failure both say which build was measured.
func resolveBuild(directory string) (string, error) {
	if strings.TrimSpace(directory) == "" {
		return "", fmt.Errorf("webaudit: a build directory is required")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("webaudit: resolve build directory: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("webaudit: open build directory %s: %w", absolute, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("webaudit: %s is not a directory", absolute)
	}
	return absolute, nil
}

// validAssetName rejects a manifest key that is not a clean relative path.
// cmd/assetgen only ever writes relative keys, and a key that climbs out of the
// directory would make the gate read a file that is not part of the build.
func validAssetName(name string) error {
	if name == "" || path.IsAbs(name) || strings.Contains(name, "\\") {
		return fmt.Errorf("webaudit: the manifest declares the unusable name %q", name)
	}
	cleaned := path.Clean(name)
	if cleaned != name || cleaned == "." || strings.HasPrefix(cleaned, "..") {
		return fmt.Errorf("webaudit: the manifest declares the unusable name %q", name)
	}
	return nil
}

// compressedSize is what one address costs on the wire: the file compressed on
// its own, the way one response carries it.
//
// gzip and not brotli: brotli would need a dependency the frontend policy does
// not admit, and the budget is a regression guard, not a promise about what the
// edge negotiates. A number that is honest and dependency-free beats a smaller
// number that the build cannot reproduce.
func compressedSize(body []byte) int {
	var buffer bytes.Buffer
	writer, err := gzip.NewWriterLevel(&buffer, gzip.BestCompression)
	if err != nil {
		return 0
	}
	if _, err := writer.Write(body); err != nil {
		return 0
	}
	if err := writer.Close(); err != nil {
		return 0
	}
	return buffer.Len()
}

// measure computes the two budgets and records the page entries.
//
// A page is measured module by module, following the specifiers the browser
// would follow: the sum is what the visitor downloads, not what the directory
// weighs. A specifier that does not resolve is not followed here — it is a
// violation of its own, reported once with its cause — so the budget never
// silently under-counts a module the browser would have fetched.
func measure(modules map[string]*module, styles map[string]*stylesheet) ([]PageMeasure, int, []string) {
	violations := make([]string, 0)
	pages := make([]PageMeasure, 0, 4)
	entries := make([]string, 0, 4)
	for name := range modules {
		if path.Dir(name) == pageDirectory {
			entries = append(entries, name)
		}
	}
	sort.Strings(entries)
	if len(entries) == 0 {
		violations = append(violations, fmt.Sprintf(
			"build: no page entry was found under %s/, so no page budget was measured", pageDirectory))
	}

	for _, entry := range entries {
		closure := closureOf(modules, entry)
		total := 0
		for name := range closure {
			total += modules[name].compressed
		}
		pages = append(pages, PageMeasure{Name: entry, Modules: len(closure), Bytes: total})
		if total > pageJavaScriptBudget {
			violations = append(violations, fmt.Sprintf(
				"budgets: %s costs %d B compressed against the %d B of docs/FRONTEND.md section 11",
				entry, total, pageJavaScriptBudget))
		}
	}

	cssTotal := 0
	for _, style := range styles {
		cssTotal += style.compressed
	}
	if cssTotal > initialCSSBudget {
		violations = append(violations, fmt.Sprintf(
			"budgets: the initial CSS costs %d B compressed against the %d B of docs/FRONTEND.md section 11",
			cssTotal, initialCSSBudget))
	}
	return pages, cssTotal, violations
}

// specifier is one module reference of the delivered graph: the text between
// quotes and the line it was read from, so a failure points at the line instead
// of at the file.
type specifier struct {
	text string
	line int
}

// specifierPattern reads the four shapes an ES module reference takes in what
// `tsc` emits: a static import, a side-effect import, a re-export and a dynamic
// import. Comments are already blanked out by the time this runs, so a path
// quoted inside documentation is not a specifier.
var specifierPattern = regexp.MustCompile(
	`\bimport\b[^;]*?\bfrom\s*["']([^"']+)["']` +
		`|\bimport\s*["']([^"']+)["']` +
		`|\bexport\b[^;]*?\bfrom\s*["']([^"']+)["']` +
		`|\bimport\s*\(\s*["']([^"']+)["']`)

// specifiersOf returns the module references of one delivered module, in the
// order they appear and without repetition.
func specifiersOf(m *module) []specifier {
	found := make([]specifier, 0, 4)
	seen := make(map[string]bool, 4)
	for _, match := range specifierPattern.FindAllStringSubmatchIndex(m.code, -1) {
		text := ""
		for group := 2; group < len(match); group += 2 {
			if match[group] >= 0 {
				text = m.code[match[group]:match[group+1]]
				break
			}
		}
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		found = append(found, specifier{text: text, line: 1 + strings.Count(m.code[:match[0]], "\n")})
	}
	return found
}

// closureOf walks the module graph from one entry, following only the relative
// specifiers that resolve inside the build. Everything else is reported by
// checkSpecifiers; here it only means the walk stops.
func closureOf(modules map[string]*module, entry string) map[string]bool {
	closure := map[string]bool{entry: true}
	pending := []string{entry}
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		for _, reference := range specifiersOf(modules[current]) {
			target, ok := resolveSpecifier(current, reference.text)
			if !ok || closure[target] || modules[target] == nil {
				continue
			}
			closure[target] = true
			pending = append(pending, target)
		}
	}
	return closure
}

// resolveSpecifier turns a relative specifier into a manifest name, the way a
// browser resolves it against the importing module. Anything that is not a
// relative reference, or that escapes the build, does not resolve.
func resolveSpecifier(from, text string) (string, bool) {
	if !strings.HasPrefix(text, "./") && !strings.HasPrefix(text, "../") {
		return "", false
	}
	target := path.Join(path.Dir(from), text)
	if target == "." || strings.HasPrefix(target, "..") {
		return "", false
	}
	return target, true
}

// checkSpecifiers is the dependency rule of the frontend, read off the graph
// that is delivered: every reference is relative, resolves to a file the
// manifest publishes, and stays inside the build.
func checkSpecifiers(modules map[string]*module) []string {
	violations := make([]string, 0)
	for _, name := range sortedModuleNames(modules) {
		for _, reference := range specifiersOf(modules[name]) {
			switch {
			case isExternal(reference.text):
				violations = append(violations, fmt.Sprintf(
					"specifiers: %s:%d imports %q, which leaves the origin; a page loads only what this build publishes",
					name, reference.line, reference.text))
			case strings.HasPrefix(reference.text, "/"):
				violations = append(violations, fmt.Sprintf(
					"specifiers: %s:%d imports %q by absolute path; the module graph is relative to this build",
					name, reference.line, reference.text))
			case !strings.HasPrefix(reference.text, "./") && !strings.HasPrefix(reference.text, "../"):
				violations = append(violations, fmt.Sprintf(
					"specifiers: %s:%d imports %q, a bare specifier; the browser has no resolver and no package directory",
					name, reference.line, reference.text))
			default:
				target, ok := resolveSpecifier(name, reference.text)
				if !ok {
					violations = append(violations, fmt.Sprintf(
						"specifiers: %s:%d imports %q, which escapes the build directory",
						name, reference.line, reference.text))
					continue
				}
				if modules[target] == nil {
					violations = append(violations, fmt.Sprintf(
						"specifiers: %s:%d imports %q, and the manifest publishes no such module",
						name, reference.line, reference.text))
				}
			}
		}
	}
	return violations
}

// isExternal reports whether a specifier names another origin: an address with
// a scheme, or a protocol-relative one, which the page would resolve against
// whatever origin served it.
func isExternal(text string) bool {
	if strings.HasPrefix(text, "//") {
		return true
	}
	scheme, rest, found := strings.Cut(text, ":")
	if !found || rest == "" {
		return false
	}
	// A relative path can contain a colon ("weird:name.js" is a file that
	// exists) but a scheme cannot contain a path separator.
	return !strings.ContainsAny(scheme, "/?#.")
}

// blockedByPolicy is the vocabulary the delivered code may not use. The policy
// of the binary carries no 'unsafe-inline' and no 'unsafe-eval', so an
// executable string is refused by the browser at runtime; the gate refuses it
// at review time, where the fix is cheap. The HTML sinks are the same rule from
// the other side: docs/FRONTEND.md section 9 says user data reaches the DOM as
// a text node, and a sink is what would let it stop being one.
var blockedByPolicy = []string{
	"eval(",
	"new Function",
	"document.write(",
	"document.writeln(",
	"innerHTML",
	"outerHTML",
	"insertAdjacentHTML",
	"javascript:",
}

// blockedInStylesheets is the same rule for CSS: a URL with an executable
// scheme and the legacy dynamic expression.
var blockedInStylesheets = []string{
	"javascript:",
	"expression(",
}

// checkPolicyModules asks both halves of the question: is the delivered code
// compatible with the policy of the binary, and is the policy itself still the
// restrictive one this check leans on?
func checkPolicyModules(modules map[string]*module, styles map[string]*stylesheet) []string {
	violations := checkPolicy()

	for _, name := range sortedModuleNames(modules) {
		for _, token := range blockedByPolicy {
			if line, found := locate(modules[name].code, token); found {
				violations = append(violations, fmt.Sprintf(
					"csp: %s:%d uses %q, which the served policy refuses in delivered code", name, line, token))
			}
		}
	}
	for _, name := range sortedStyleNames(styles) {
		for _, token := range blockedInStylesheets {
			if line, found := locate(styles[name].code, token); found {
				violations = append(violations, fmt.Sprintf(
					"csp: %s:%d uses %q, which the served policy refuses in delivered code", name, line, token))
			}
		}
	}
	return violations
}

// locate finds a token and reports the line it sits on. A token that names a URL
// scheme is matched case-insensitively, because RFC 3986 says schemes are; every
// other token names a JavaScript identifier or a CSS function, both of which
// are case-sensitive, and matching those loosely would fail a build for a name
// that is not the construct being refused.
func locate(code, token string) (int, bool) {
	haystack := code
	if strings.Contains(token, ":") {
		haystack = strings.ToLower(code)
		token = strings.ToLower(token)
	}
	index := strings.Index(haystack, token)
	if index < 0 {
		return 0, false
	}
	return 1 + strings.Count(code[:index], "\n"), true
}

// checkPolicy reads the policy the binary delivers and refuses the three
// widenings that would make every other check here meaningless: inline code,
// eval, and any origin at all. A policy that allowed one of them would let the
// delivered build grow a construct nothing in this file could see.
func checkPolicy() []string {
	policy := securityheaders.ContentSecurityPolicy()
	if strings.TrimSpace(policy) == "" {
		return []string{"csp: the binary delivers no Content-Security-Policy"}
	}
	violations := make([]string, 0, 2)
	for _, directive := range strings.Split(policy, ";") {
		fields := strings.Fields(strings.TrimSpace(directive))
		if len(fields) == 0 {
			continue
		}
		name := strings.ToLower(fields[0])
		if name != "script-src" && name != "style-src" && name != "default-src" {
			continue
		}
		for _, source := range fields[1:] {
			switch strings.ToLower(source) {
			case "'unsafe-inline'":
				violations = append(violations, fmt.Sprintf(
					"csp: %s allows 'unsafe-inline'; this gate asserts there is no inline code to allow", name))
			case "'unsafe-eval'":
				violations = append(violations, fmt.Sprintf(
					"csp: %s allows 'unsafe-eval'; this gate asserts the delivered code evaluates nothing", name))
			case "*":
				violations = append(violations, fmt.Sprintf(
					"csp: %s allows any origin, so no import rule of this gate would hold", name))
			}
		}
	}
	return violations
}

// networkPrimitives are the ways delivered code can talk to the network. They
// exist in exactly one directory, because one place that owns timeout, CSRF,
// request identity and Problem Details is what keeps them from disagreeing.
var networkPrimitives = []string{
	"fetch(",
	"XMLHttpRequest",
	"WebSocket",
	"EventSource",
	"sendBeacon",
}

// checkNetworkReach refuses a network primitive outside core/. A page that
// fetches on its own skips every guarantee the HTTP core makes — including the
// double submit the CSRF policy requires — so the rule is structural rather
// than stylistic.
func checkNetworkReach(modules map[string]*module) []string {
	violations := make([]string, 0)
	for _, name := range sortedModuleNames(modules) {
		directory := path.Dir(name)
		if directory == coreDirectory || strings.HasPrefix(directory, coreDirectory+"/") {
			continue
		}
		for _, token := range networkPrimitives {
			if line, found := locate(modules[name].code, token); found {
				violations = append(violations, fmt.Sprintf(
					"network: %s:%d calls %q outside %s/; the HTTP core is the only module that reaches the server",
					name, line, token, coreDirectory))
			}
		}
	}
	return violations
}

func sortedModuleNames(modules map[string]*module) []string {
	names := make([]string, 0, len(modules))
	for name := range modules {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedStyleNames(styles map[string]*stylesheet) []string {
	names := make([]string, 0, len(styles))
	for name := range styles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// stripComments blanks out every comment of a source, keeping its length and
// every newline in place. Two reasons, and both matter:
//
//   - a path or a forbidden name quoted in documentation is not code. This
//     repository documents its rules in the modules that obey them, so the gate
//     would otherwise fail on its own comments and be weakened until it passed;
//   - the offsets stay the same, so a violation can be reported with the line
//     of the original file instead of the line of a rewritten copy.
//
// Strings and template literals are skipped, so a `//` inside one is not read
// as a comment. The stripper is deliberately small: it does not parse regular
// expression literals, which the delivered code does not use in a form where a
// `/` begins one, and it treats the whole of a template literal as text.
func stripComments(source string) string {
	const (
		code = iota
		lineComment
		blockComment
		singleQuote
		doubleQuote
		template
	)
	blanked := []byte(source)
	state := code
	for i := 0; i < len(source); i++ {
		char := source[i]
		switch state {
		case code:
			switch {
			case char == '/' && i+1 < len(source) && source[i+1] == '/':
				state, blanked[i], blanked[i+1] = lineComment, ' ', ' '
				i++
			case char == '/' && i+1 < len(source) && source[i+1] == '*':
				state, blanked[i], blanked[i+1] = blockComment, ' ', ' '
				i++
			case char == '\'':
				state = singleQuote
			case char == '"':
				state = doubleQuote
			case char == '`':
				state = template
			}
		case lineComment:
			if char == '\n' {
				state = code
			} else {
				blanked[i] = ' '
			}
		case blockComment:
			switch {
			case char == '*' && i+1 < len(source) && source[i+1] == '/':
				blanked[i], blanked[i+1] = ' ', ' '
				state = code
				i++
			case char != '\n':
				blanked[i] = ' '
			}
		case singleQuote, doubleQuote, template:
			if char == '\\' && i+1 < len(source) {
				i++
				continue
			}
			if (state == singleQuote && char == '\'') ||
				(state == doubleQuote && char == '"') ||
				(state == template && char == '`') {
				state = code
			}
		}
	}
	return string(blanked)
}
