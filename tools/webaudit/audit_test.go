package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// fixture copies the checked-in build, so a test can break it without leaving a
// mark on the repository. Every test that asserts a failure starts here: the
// broken build of one rule is the passing build of every other rule, which is
// what makes the failure attributable.
func fixture(t *testing.T) string {
	t.Helper()
	destination := filepath.Join(t.TempDir(), "build")
	if err := os.CopyFS(destination, os.DirFS(filepath.Join("testdata", "build"))); err != nil {
		t.Fatalf("copy the fixture build: %v", err)
	}
	return destination
}

// audit measures a build the way the gate does, and fails the test when the
// build cannot be measured at all.
func audit(t *testing.T, build string) Report {
	t.Helper()
	report, err := Audit(build)
	if err != nil {
		t.Fatalf("Audit(%s): %v", build, err)
	}
	return report
}

// violationsOf is the audit's answer for a build that is measurable: what it
// found, so a test can name the rule it drove past the limit.
func violationsOf(t *testing.T, build string) []string {
	t.Helper()
	return audit(t, build).Violations
}

func appendTo(t *testing.T, build, name, text string) {
	t.Helper()
	write(t, build, name, read(t, build, name)+text)
}

func write(t *testing.T, build, name, text string) {
	t.Helper()
	target := filepath.Join(build, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(target), err)
	}
	if err := os.WriteFile(target, []byte(text), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func read(t *testing.T, build, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(build, filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(body)
}

// writeManifest rewrites the manifest for the named assets, with the digest and
// the hashed address cmd/assetgen would produce. A test that changes which
// files a build declares cannot use the fixture's manifest, and a gate reading a
// record of bytes that no longer exist would be a lie about the build.
func writeManifest(t *testing.T, build string, names ...string) {
	t.Helper()
	records := make(map[string]map[string]string, len(names))
	for _, name := range names {
		body, err := os.ReadFile(filepath.Join(build, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		sum := sha256.Sum256(body)
		digest := hex.EncodeToString(sum[:])
		extension := path.Ext(name)
		records[name] = map[string]string{
			"path":   "/assets/" + strings.TrimSuffix(name, extension) + "-" + digest[:12] + extension,
			"sha256": digest,
		}
	}
	encoded, err := json.MarshalIndent(map[string]any{"version": 1, "assets": records}, "", "  ")
	if err != nil {
		t.Fatalf("encode the manifest: %v", err)
	}
	write(t, build, "manifest.json", string(append(encoded, '\n')))
}

func has(violations []string, fragment string) bool {
	for _, violation := range violations {
		if strings.Contains(violation, fragment) {
			return true
		}
	}
	return false
}

func TestAuditMeasuresTheFixtureBuild(t *testing.T) {
	report := audit(t, fixture(t))

	if len(report.Violations) != 0 {
		t.Fatalf("the fixture build was refused: %v", report.Violations)
	}
	if report.Assets != 3 || report.Modules != 2 || report.Styles != 1 {
		t.Fatalf("the fixture measured %d assets, %d modules and %d stylesheets, want 3, 2 and 1",
			report.Assets, report.Modules, report.Styles)
	}
	if len(report.Pages) != 1 {
		t.Fatalf("the fixture measured %d page entries, want the single pages/ module", len(report.Pages))
	}
	page := report.Pages[0]
	if page.Name != "pages/arena.js" {
		t.Fatalf("the page entry is %s, want pages/arena.js", page.Name)
	}
	// The closure is the entry plus what it imports, because that is what the
	// browser fetches when the document loads this one module.
	if page.Modules != 2 {
		t.Fatalf("the closure of %s holds %d modules, want the entry and the core it imports", page.Name, page.Modules)
	}
	if page.Bytes <= 0 || page.Bytes > pageJavaScriptBudget {
		t.Fatalf("the page costs %d B compressed, which is not a measurement inside the budget", page.Bytes)
	}
	if report.CSSBytes <= 0 || report.CSSBytes > initialCSSBudget {
		t.Fatalf("the CSS costs %d B compressed, which is not a measurement inside the budget", report.CSSBytes)
	}

	var out bytes.Buffer
	report.write(&out)
	for _, expected := range []string{"webaudit: ok", "pages/arena.js", "initial CSS"} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("the report does not mention %q:\n%s", expected, out.String())
		}
	}
}

func TestAuditMeasuresStaticImagesSeparatelyAndRefusesMissingMedia(t *testing.T) {
	build := fixture(t)
	baseline := audit(t, build)
	svg, webp := `<svg xmlns="http://www.w3.org/2000/svg"></svg>`, "RIFF\x00\x01\xffWEBP"
	write(t, build, "realm/crest.svg", svg)
	write(t, build, "realm/portrait.webp", webp)
	writeManifest(t, build, "pages/arena.js", "core/http.js", "styles/base.css", "realm/crest.svg", "realm/portrait.webp")
	report := audit(t, build)
	if report.Assets != 5 || report.Images != 2 || report.ImageBytes != len(svg)+len(webp) || report.CSSBytes != baseline.CSSBytes || report.Pages[0].Bytes != baseline.Pages[0].Bytes || len(report.Violations) != 0 {
		t.Fatalf("media contaminated code budgets or was not measured: %+v", report)
	}
	if err := os.Remove(filepath.Join(build, "realm/portrait.webp")); err != nil {
		t.Fatal(err)
	}
	if _, err := Audit(build); err == nil || !strings.Contains(err.Error(), "portrait.webp") {
		t.Fatal("missing declared image was not refused by name")
	}
}

func TestAuditRefusesAnExternalImport(t *testing.T) {
	build := fixture(t)
	appendTo(t, build, "pages/arena.js", "\nimport \"https://cdn.example.invalid/analytics.js\";\n")

	violations := violationsOf(t, build)
	if !has(violations, "leaves the origin") || !has(violations, "https://cdn.example.invalid/analytics.js") {
		t.Fatalf("an external import was not refused: %v", violations)
	}
	if !has(violations, "pages/arena.js:") {
		t.Fatalf("the violation does not name the module that carries it: %v", violations)
	}
}

func TestAuditRefusesABareSpecifier(t *testing.T) {
	build := fixture(t)
	appendTo(t, build, "pages/arena.js", "\nimport { html } from \"lit\";\n")

	if violations := violationsOf(t, build); !has(violations, "bare specifier") || !has(violations, "\"lit\"") {
		t.Fatalf("a bare specifier was not refused: %v", violations)
	}
}

func TestAuditRefusesAnAbsoluteSpecifier(t *testing.T) {
	build := fixture(t)
	appendTo(t, build, "pages/arena.js", "\nimport \"/assets/core/http.js\";\n")

	if violations := violationsOf(t, build); !has(violations, "by absolute path") {
		t.Fatalf("an absolute specifier was not refused: %v", violations)
	}
}

func TestAuditRefusesAnUnresolvedImport(t *testing.T) {
	build := fixture(t)
	appendTo(t, build, "pages/arena.js", "\nimport \"./missing.js\";\n")

	if violations := violationsOf(t, build); !has(violations, "publishes no such module") {
		t.Fatalf("an import of an unpublished module was not refused: %v", violations)
	}
}

func TestAuditRefusesASpecifierThatEscapesTheBuild(t *testing.T) {
	build := fixture(t)
	appendTo(t, build, "core/http.js", "\nimport \"../../outside.js\";\n")

	if violations := violationsOf(t, build); !has(violations, "escapes the build") {
		t.Fatalf("a specifier leaving the build was not refused: %v", violations)
	}
}

func TestAuditRefusesADynamicImport(t *testing.T) {
	build := fixture(t)
	appendTo(t, build, "pages/arena.js", "\nexport const late = () => import(\"./late.js\");\n")

	if violations := violationsOf(t, build); !has(violations, "\"./late.js\"") {
		t.Fatalf("a dynamic import of an unpublished module was not refused: %v", violations)
	}
}

func TestAuditRefusesAnOversizedPage(t *testing.T) {
	build := fixture(t)
	// Incompressible on purpose: prose compresses to almost nothing and the
	// budget would never be crossed, which would make this test prove nothing.
	write(t, build, "core/http.js", randomCode(200_000))

	violations := violationsOf(t, build)
	if !has(violations, "budgets: pages/arena.js") {
		t.Fatalf("a page over the JavaScript budget was not refused: %v", violations)
	}
}

func TestAuditRefusesOversizedCSS(t *testing.T) {
	build := fixture(t)
	write(t, build, "styles/base.css", randomCSS(300_000))

	if violations := violationsOf(t, build); !has(violations, "budgets: the initial CSS") {
		t.Fatalf("a build over the CSS budget was not refused: %v", violations)
	}
}

func TestAuditRefusesCodeThePolicyWouldBlock(t *testing.T) {
	for _, construct := range []string{
		`const value = eval("1 + 1");`,
		`const build = new Function("return 1");`,
		`document.write("<p>hello</p>");`,
		`const link = "javascript:alert(1)";`,
	} {
		t.Run(construct, func(t *testing.T) {
			build := fixture(t)
			appendTo(t, build, "core/http.js", "\n"+construct+"\n")

			if violations := violationsOf(t, build); !has(violations, "csp: core/http.js:") {
				t.Fatalf("%q in delivered code was not refused: %v", construct, violations)
			}
		})
	}
}

func TestAuditRefusesAnHtmlSinkInCode(t *testing.T) {
	build := fixture(t)
	appendTo(t, build, "pages/arena.js", "\nconst render = (node, value) => { node.innerHTML = value; };\n")

	if violations := violationsOf(t, build); !has(violations, "\"innerHTML\"") {
		t.Fatalf("an HTML sink in delivered code was not refused: %v", violations)
	}
}

func TestAuditReadsCodeAndNotComments(t *testing.T) {
	// The fixture already quotes `innerHTML` and an https:// address inside a
	// comment, which is how this repository documents its own rules. If a
	// comment counted as code, the gate would fail on its own documentation.
	build := fixture(t)
	report := audit(t, build)

	if len(report.Violations) != 0 {
		t.Fatalf("a comment counted as delivered code: %v", report.Violations)
	}
	if !strings.Contains(read(t, build, "pages/arena.js"), "innerHTML") {
		t.Fatal("the fixture no longer quotes a forbidden name in a comment, so this test proves nothing")
	}
}

func TestAuditRefusesAFetchOutsideTheCore(t *testing.T) {
	for _, construct := range []string{
		`fetch("/api/v1/arenas");`,
		`const socket = new WebSocket("wss://example.invalid/live");`,
		`const stream = new EventSource("/api/v1/events");`,
	} {
		t.Run(construct, func(t *testing.T) {
			build := fixture(t)
			appendTo(t, build, "pages/arena.js", "\n"+construct+"\n")

			if violations := violationsOf(t, build); !has(violations, "network: pages/arena.js:") {
				t.Fatalf("%q outside core/ was not refused: %v", construct, violations)
			}
		})
	}
}

func TestAuditAllowsTheNetworkInsideTheCore(t *testing.T) {
	build := fixture(t)
	if !strings.Contains(read(t, build, "core/http.js"), "fetch(") {
		t.Fatal("the fixture core no longer fetches, so this test proves nothing")
	}
	if violations := violationsOf(t, build); len(violations) != 0 {
		t.Fatalf("the fetch of the core was refused: %v", violations)
	}
}

func TestAuditRefusesABuildWithoutAPageEntry(t *testing.T) {
	build := fixture(t)
	writeManifest(t, build, "core/http.js", "styles/base.css")

	if violations := violationsOf(t, build); !has(violations, "no page entry") {
		t.Fatalf("a build with no page entry was not refused: %v", violations)
	}
}

func TestAuditFailsClosedWhenADeclaredFileIsMissing(t *testing.T) {
	build := fixture(t)
	if err := os.Remove(filepath.Join(build, "core", "http.js")); err != nil {
		t.Fatalf("remove the declared module: %v", err)
	}

	if _, err := Audit(build); err == nil {
		t.Fatal("a manifest declaring a file the build does not have was measured instead of refused")
	}
}

func TestAuditFailsClosedWithoutAManifest(t *testing.T) {
	build := fixture(t)
	if err := os.Remove(filepath.Join(build, "manifest.json")); err != nil {
		t.Fatalf("remove the manifest: %v", err)
	}

	if _, err := Audit(build); err == nil {
		t.Fatal("a build without a manifest was measured instead of refused")
	}
}

func TestAuditFailsClosedOnAManifestNameThatLeavesTheBuild(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "build")
	write(t, filepath.Dir(directory), "build/manifest.json",
		`{"version":1,"assets":{"../secret.js":{"path":"/assets/secret.js","sha256":"0"}}}`)

	if _, err := Audit(directory); err == nil {
		t.Fatal("a manifest naming a path outside the build was measured instead of refused")
	}
}

func TestAuditMeasuresTheRealBuild(t *testing.T) {
	build := filepath.Join("..", "..", "web", "dist")
	if _, err := os.Stat(filepath.Join(build, "manifest.json")); err != nil {
		t.Skip("web/dist is not built in this workspace; make build-web produces it")
	}
	report := audit(t, build)

	if len(report.Violations) != 0 {
		t.Fatalf("the delivered build breaks the frontend rules: %v", report.Violations)
	}
	if len(report.Pages) == 0 {
		t.Fatal("the delivered build has no page entry, so no budget was measured")
	}
}

func TestIsExternal(t *testing.T) {
	for _, testCase := range []struct {
		specifier string
		external  bool
	}{
		{"", false},
		{"./problem.js", false},
		{"../core/http.js", false},
		// A colon is legal in a file name, and a relative specifier that
		// happens to carry one is still a file of this build.
		{"./weird:name.js", false},
		{"https://cdn.example.invalid/x.js", true},
		{"http://127.0.0.1:8080/x.js", true},
		{"//cdn.example.invalid/x.js", true},
	} {
		if external := isExternal(testCase.specifier); external != testCase.external {
			t.Fatalf("isExternal(%q) = %v, want %v", testCase.specifier, external, testCase.external)
		}
	}
}

func TestStripCommentsKeepsLineNumbersAndStrings(t *testing.T) {
	source := strings.Join([]string{
		`const marker = "// not a comment";`, // a separator inside a string is text
		`/* a block comment`,
		`   spanning lines, with eval( inside */`,
		`const other = 'also // text';`,
		`// eval( and innerHTML in a line comment`,
		`const template = ` + "`innerHTML ${1 + 1}`" + `;`,
		`const sink = eval("1");`,
	}, "\n")

	stripped := stripComments(source)
	if len(stripped) != len(source) {
		t.Fatalf("stripping changed the length: %d bytes became %d", len(source), len(stripped))
	}
	if strings.Count(stripped, "\n") != strings.Count(source, "\n") {
		t.Fatal("stripping changed the line count, so a violation could not be reported on its own line")
	}
	for _, gone := range []string{"a block comment", "spanning lines", "eval( and innerHTML in a line comment"} {
		if strings.Contains(stripped, gone) {
			t.Fatalf("the comment %q survived the stripping:\n%s", gone, stripped)
		}
	}
	if !strings.Contains(stripped, `"// not a comment"`) || !strings.Contains(stripped, "`innerHTML ${1 + 1}`") {
		t.Fatalf("a string was stripped as if it were a comment:\n%s", stripped)
	}
	if line, found := locate(stripped, "eval("); !found || line != 7 {
		t.Fatalf("the surviving eval( was located at line %d (found %v), want the seventh line", line, found)
	}
}

func TestStripCommentsLeavesAnUnterminatedCommentBlanked(t *testing.T) {
	stripped := stripComments("const a = 1; /* open\nconst b = eval(1);")
	if strings.Contains(stripped, "eval(") {
		t.Fatalf("an unterminated comment kept its content:\n%s", stripped)
	}
}

// randomCode returns a module whose bytes barely compress: identifiers drawn
// from an alphanumeric alphabet, generated from a linear congruential sequence
// so the fixture is deterministic.
//
// Incompressible on purpose. Prose compresses to a fraction of its size, and a
// fixture that never crosses the budget would make the two oversize tests prove
// nothing. The alphabet carries no quote or parenthesis, so the noise cannot
// spell a construct another check refuses and the failure stays attributable to
// the budget.
func randomCode(size int) string {
	var builder strings.Builder
	for seed := uint64(1); builder.Len() < size; {
		builder.WriteString("const v")
		builder.WriteString(noise(&seed, 32))
		builder.WriteString(" = ")
		builder.WriteString(noise(&seed, 32))
		builder.WriteString(";\n")
	}
	return builder.String()
}

func randomCSS(size int) string {
	var builder strings.Builder
	for seed := uint64(7); builder.Len() < size; {
		builder.WriteString(".c")
		builder.WriteString(noise(&seed, 12))
		builder.WriteString("{ margin: 0 0 0 ")
		builder.WriteString(noise(&seed, 6))
		builder.WriteString("px; color: #")
		builder.WriteString(noise(&seed, 6))
		builder.WriteString("; }\n")
	}
	return builder.String()
}

// noise advances the generator and returns length characters of it.
func noise(seed *uint64, length int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	expanded := make([]byte, length)
	for index := range expanded {
		*seed = *seed*6364136223846793005 + 1442695040888963407
		expanded[index] = alphabet[(*seed>>33)%uint64(len(alphabet))]
	}
	return string(expanded)
}
