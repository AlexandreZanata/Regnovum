// Tests of internal/i18ngen (P02-T07), covering the minimum validation of
// the task: key/placeholder parity, invalid JSON, duplicate keys, unknown
// locales and deterministic generation, plus drift detection.
package i18ngen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/i18ngen"
)

// writeTree materializes a locales tree from locale -> filename -> content.
func writeTree(t *testing.T, tree map[string]map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for locale, files := range tree {
		if err := os.MkdirAll(filepath.Join(root, locale), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", locale, err)
		}
		for name, content := range files {
			path := filepath.Join(root, locale, name)
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
		}
	}
	return root
}

const validPT = `{"errors":{"internal":{"title":"Erro interno","detail":"Falhou."}}}` + "\n"
const validEN = `{"errors":{"internal":{"title":"Internal error","detail":"It failed."}}}` + "\n"

func outputsIn(root string) i18ngen.Outputs {
	return i18ngen.Outputs{
		TSTarget:  filepath.Join(root, "ts", "generated.ts"),
		GoTarget:  filepath.Join(root, "go", "generated.go"),
		GOPackage: "i18n",
	}
}

func TestLoadBundleAcceptsValidParityTree(t *testing.T) {
	t.Parallel()

	root := writeTree(t, map[string]map[string]string{
		"pt-BR": {"errors.json": validPT},
		"en-US": {"errors.json": validEN},
	})

	bundle, err := i18ngen.LoadBundle(root)
	if err != nil {
		t.Fatalf("LoadBundle: %v", err)
	}
	if got := len(bundle.Namespaces); got != 1 {
		t.Fatalf("namespaces = %d, want 1", got)
	}
	values := bundle.Messages["errors"]["errors.internal.title"]
	if values["pt-BR"] != "Erro interno" || values["en-US"] != "Internal error" {
		t.Errorf("unexpected values: %v", values)
	}
}

func TestLoadBundleRejectsUnknownLocale(t *testing.T) {
	t.Parallel()

	root := writeTree(t, map[string]map[string]string{
		"pt-BR": {"errors.json": validPT},
		"en-US": {"errors.json": validEN},
		"xx-XX": {"errors.json": validPT},
	})

	_, err := i18ngen.LoadBundle(root)
	if err == nil {
		t.Fatal("expected failure for unknown locale directory")
	}
	if !strings.Contains(err.Error(), "unknown locale directory") {
		t.Errorf("error should name the problem, got: %v", err)
	}
}

func TestLoadBundleRejectsInvalidJSON(t *testing.T) {
	t.Parallel()

	root := writeTree(t, map[string]map[string]string{
		"pt-BR": {"errors.json": "{not json"},
		"en-US": {"errors.json": validEN},
	})

	_, err := i18ngen.LoadBundle(root)
	if err == nil {
		t.Fatal("expected failure for invalid JSON")
	}
	if !strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("error should name invalid JSON, got: %v", err)
	}
}

func TestLoadBundleRejectsDuplicateKey(t *testing.T) {
	t.Parallel()

	// encoding/json would silently keep the last value; the scanner must
	// reject the file instead.
	duplicated := `{"errors":{"internal":{"title":"a","title":"b","detail":"x"}}}` + "\n"
	root := writeTree(t, map[string]map[string]string{
		"pt-BR": {"errors.json": duplicated},
		"en-US": {"errors.json": validEN},
	})

	_, err := i18ngen.LoadBundle(root)
	if err == nil {
		t.Fatal("expected failure for duplicate key")
	}
	if !strings.Contains(err.Error(), "duplicate key") {
		t.Errorf("error should name the duplicate key, got: %v", err)
	}
}

func TestLoadBundleRejectsKeyParityViolation(t *testing.T) {
	t.Parallel()

	extra := `{"errors":{"internal":{"title":"Erro","detail":"Falhou.","ghost":"extra"}}}` + "\n"
	root := writeTree(t, map[string]map[string]string{
		"pt-BR": {"errors.json": extra},
		"en-US": {"errors.json": validEN},
	})

	_, err := i18ngen.LoadBundle(root)
	if err == nil {
		t.Fatal("expected failure for key parity violation")
	}
	if !strings.Contains(err.Error(), "missing in locale en-US") {
		t.Errorf("error should name the missing locale, got: %v", err)
	}
}

func TestLoadBundleRejectsPlaceholderDivergence(t *testing.T) {
	t.Parallel()

	withName := `{"errors":{"conflict":{"title":"Olá {userName}"}}}` + "\n"
	withoutName := `{"errors":{"conflict":{"title":"Hello {user}"}}}` + "\n"
	root := writeTree(t, map[string]map[string]string{
		"pt-BR": {"errors.json": withName},
		"en-US": {"errors.json": withoutName},
	})

	_, err := i18ngen.LoadBundle(root)
	if err == nil {
		t.Fatal("expected failure for placeholder divergence")
	}
	if !strings.Contains(err.Error(), "placeholder divergence") {
		t.Errorf("error should name placeholder divergence, got: %v", err)
	}
}

func TestLoadBundleRejectsMalformedPlaceholder(t *testing.T) {
	t.Parallel()

	malformed := `{"errors":{"internal":{"title":"Ola {} mundo"}}}` + "\n"
	root := writeTree(t, map[string]map[string]string{
		"pt-BR": {"errors.json": malformed},
		"en-US": {"errors.json": `{"errors":{"internal":{"title":"Hi"}}}` + "\n"},
	})

	_, err := i18ngen.LoadBundle(root)
	if err == nil {
		t.Fatal("expected failure for malformed placeholder")
	}
	if !strings.Contains(err.Error(), "malformed placeholder") {
		t.Errorf("error should name the malformed placeholder, got: %v", err)
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	t.Parallel()

	root := writeTree(t, map[string]map[string]string{
		"pt-BR": {"errors.json": validPT},
		"en-US": {"errors.json": validEN},
	})
	outputs := outputsIn(root)

	tsOne, goOne, err := i18ngen.RenderAll(root, outputs)
	if err != nil {
		t.Fatalf("RenderAll one: %v", err)
	}
	tsTwo, goTwo, err := i18ngen.RenderAll(root, outputs)
	if err != nil {
		t.Fatalf("RenderAll two: %v", err)
	}
	if string(tsOne) != string(tsTwo) {
		t.Error("TS output is not deterministic")
	}
	if string(goOne) != string(goTwo) {
		t.Error("Go output is not deterministic")
	}
	if !strings.Contains(string(tsOne), "Code generated by internal/i18ngen") {
		t.Error("TS output missing generated header")
	}
	if !strings.Contains(string(goOne), "package i18n") {
		t.Error("Go output missing package clause")
	}
}

func TestWriteIfChangedAndHasDrift(t *testing.T) {
	t.Parallel()

	root := writeTree(t, map[string]map[string]string{
		"pt-BR": {"errors.json": validPT},
		"en-US": {"errors.json": validEN},
	})
	outputs := outputsIn(root)
	tsSource, goSource, err := i18ngen.RenderAll(root, outputs)
	if err != nil {
		t.Fatalf("RenderAll: %v", err)
	}

	// Missing artifacts count as drift.
	drifted, err := i18ngen.HasDrift(outputs, tsSource, goSource)
	if err != nil || !drifted {
		t.Fatalf("missing artifacts must be drift (drifted=%v err=%v)", drifted, err)
	}

	if _, err := i18ngen.WriteIfChanged(outputs.TSTarget, tsSource); err != nil {
		t.Fatalf("write TS: %v", err)
	}
	if _, err := i18ngen.WriteIfChanged(outputs.GoTarget, goSource); err != nil {
		t.Fatalf("write Go: %v", err)
	}

	drifted, err = i18ngen.HasDrift(outputs, tsSource, goSource)
	if err != nil || drifted {
		t.Fatalf("fresh artifacts must not drift (drifted=%v err=%v)", drifted, err)
	}

	// Divergent content is drift.
	drifted, err = i18ngen.HasDrift(outputs, []byte("// stale"), goSource)
	if err != nil || !drifted {
		t.Fatalf("stale artifacts must be drift (drifted=%v err=%v)", drifted, err)
	}
}
