// Tests of the asset generator (P18-T05 extension of P18-T01).
//
// The generator is the only step between the compiled frontend and the
// deployment, so what it writes is asserted directly: an immutable hashed name
// for every asset — the address the manifest publishes and the server-rendered
// pages reference — and the stable logical path alongside it, which is what an
// ES module resolves its own imports from.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile creates one source file of the fixture tree.
func writeFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create %s: %v", relative, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", relative, err)
	}
}

func TestGenerateWritesHashedAndStableNames(t *testing.T) {
	t.Parallel()

	source := t.TempDir()
	output := filepath.Join(t.TempDir(), "dist")
	manifestPath := filepath.Join(output, "manifest.json")

	// A page module that imports another module and a stylesheet, which is the
	// shape the account journey ships.
	writeFile(t, source, "pages/auth.js", "import \"./submission.js\";\n")
	writeFile(t, source, "pages/submission.js", "export const busy = true;\n")
	writeFile(t, source, "styles/auth.css", ".ga-auth { display: block; }\n")
	writeFile(t, source, "pages/notes.txt", "not served\n")
	writeFile(t, source, "realm/crest.svg", `<svg xmlns="http://www.w3.org/2000/svg"></svg>`)
	writeFile(t, source, "realm/portrait.webp", "RIFF\x00\x01\xffWEBP")

	if err := generate([]string{source}, output, manifestPath); err != nil {
		t.Fatalf("generate() error = %v", err)
	}

	encoded, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var generated manifest
	if err := json.Unmarshal(encoded, &generated); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	if len(generated.Assets) != 5 {
		t.Fatalf("manifest lists %d assets, want the five approved script/style/image assets", len(generated.Assets))
	}

	for _, name := range []string{"pages/auth.js", "pages/submission.js", "styles/auth.css", "realm/crest.svg", "realm/portrait.webp"} {
		record, ok := generated.Assets[name]
		if !ok {
			t.Fatalf("manifest is missing %s", name)
		}
		if !strings.HasPrefix(record.Path, "/assets/") || !strings.Contains(record.Path, record.SHA256[:12]) {
			t.Errorf("%s publishes %q, want an immutable address carrying its digest", name, record.Path)
		}

		hashed := filepath.Join(output, filepath.FromSlash(strings.TrimPrefix(record.Path, "/assets/")))
		stable := filepath.Join(output, filepath.FromSlash(name))
		hashedBody, err := os.ReadFile(hashed)
		if err != nil {
			t.Fatalf("the hashed copy of %s is missing: %v", name, err)
		}
		stableBody, err := os.ReadFile(stable)
		if err != nil {
			t.Fatalf("the stable copy of %s is missing, so a module graph cannot resolve it: %v", name, err)
		}
		if string(hashedBody) != string(stableBody) {
			t.Errorf("%s differs between its two names", name)
		}
	}

	if _, err := os.Stat(filepath.Join(output, "pages", "notes.txt")); err == nil {
		t.Error("a file that is neither script nor stylesheet was emitted")
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	t.Parallel()

	source := t.TempDir()
	writeFile(t, source, "pages/auth.js", "export const version = 1;\n")

	firstOutput := filepath.Join(t.TempDir(), "dist")
	if err := generate([]string{source}, firstOutput, filepath.Join(firstOutput, "manifest.json")); err != nil {
		t.Fatalf("first generate() error = %v", err)
	}
	secondOutput := filepath.Join(t.TempDir(), "dist")
	if err := generate([]string{source}, secondOutput, filepath.Join(secondOutput, "manifest.json")); err != nil {
		t.Fatalf("second generate() error = %v", err)
	}

	first, err := os.ReadFile(filepath.Join(firstOutput, "manifest.json"))
	if err != nil {
		t.Fatalf("read first manifest: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(secondOutput, "manifest.json"))
	if err != nil {
		t.Fatalf("read second manifest: %v", err)
	}
	if string(first) != string(second) {
		t.Error("the same sources produced two different manifests")
	}
}

func TestGenerateRefusesDuplicateAssetNames(t *testing.T) {
	t.Parallel()

	first := t.TempDir()
	second := t.TempDir()
	writeFile(t, first, "styles/auth.css", ".ga-auth { display: block; }\n")
	writeFile(t, second, "styles/auth.css", ".ga-auth { display: flex; }\n")

	output := filepath.Join(t.TempDir(), "dist")
	err := generate([]string{first, second}, output, filepath.Join(output, "manifest.json"))
	if err == nil {
		t.Fatal("generate() accepted two sources claiming the same asset name")
	}
	if !strings.Contains(err.Error(), "duplicate asset") {
		t.Errorf("the failure does not name the problem: %v", err)
	}
}
