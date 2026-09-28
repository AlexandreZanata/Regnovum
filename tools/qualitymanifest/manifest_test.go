package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// cleanInputs builds valid inputs over synthetic bytes: the contents do
// not matter, only that every required name exists and carries nothing
// the sensitive detector knows.
func cleanInputs() Inputs {
	tree := map[string][]byte{}
	for _, want := range treeArtifacts {
		tree[want.name] = []byte("fixture " + want.name + " content\n")
	}
	return Inputs{
		Commit:     "abc1234",
		Clean:      true,
		Toolchains: map[string]string{"go": "1.27.1"},
		Tree:       tree,
		Evidence:   map[string][]byte{"results.json": []byte(`{"suites":[]}` + "\n")},
	}
}

func buildTo(t *testing.T, inputs Inputs) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "bundle")
	if findings := Build(dir, inputs); len(findings) != 0 {
		t.Fatalf("build findings = %v", findings)
	}
	return dir
}

// TestAssembleAcceptsCleanInputs proves the happy path assembles without
// findings and every required name is described.
func TestAssembleAcceptsCleanInputs(t *testing.T) {
	t.Parallel()

	manifest, findings := Assemble(cleanInputs())
	if len(findings) != 0 {
		t.Fatalf("findings = %v", findings)
	}
	if manifest.Schema != ManifestSchema || manifest.Commit != "abc1234" || !manifest.Clean {
		t.Fatalf("manifest header = %+v", manifest)
	}
	if len(manifest.Artifacts) != len(treeArtifacts) || len(manifest.Evidence) != 1 {
		t.Fatalf("artifacts = %d, evidence = %d", len(manifest.Artifacts), len(manifest.Evidence))
	}
}

// TestSameEvidenceProducesCanonicalManifest proves determinism: two
// builds of the same inputs are byte-identical.
func TestSameEvidenceProducesCanonicalManifest(t *testing.T) {
	t.Parallel()

	first, second := buildTo(t, cleanInputs()), buildTo(t, cleanInputs())
	for _, name := range []string{"manifest.json", "manifest.sha256"} {
		a, err := os.ReadFile(filepath.Join(first, name))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(second, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(a) != string(b) {
			t.Fatalf("%s differs between identical builds", name)
		}
	}
}

// TestAssembleRefusesIncompleteInputs drives every refusal: a missing
// field, a dirty tree, a missing artifact and an empty evidence set.
func TestAssembleRefusesIncompleteInputs(t *testing.T) {
	t.Parallel()

	empty := cleanInputs()
	empty.Commit = ""
	if _, findings := Assemble(empty); !hasRule(findings, "missing-field") {
		t.Errorf("empty commit findings = %v, want missing-field", findings)
	}

	dirty := cleanInputs()
	dirty.Clean = false
	if _, findings := Assemble(dirty); !hasRule(findings, "dirty-tree") {
		t.Errorf("dirty findings = %v, want dirty-tree", findings)
	}

	missing := cleanInputs()
	delete(missing.Tree, "catalog")
	if _, findings := Assemble(missing); !hasRule(findings, "missing-artifact") {
		t.Errorf("missing artifact findings = %v, want missing-artifact", findings)
	}

	none := cleanInputs()
	none.Evidence = map[string][]byte{}
	if _, findings := Assemble(none); !hasRule(findings, "missing-field") {
		t.Errorf("empty evidence findings = %v, want missing-field", findings)
	}
}

// TestAssembleRefusesSensitiveContent proves no PII or secret reaches a
// bundle: a real-domain address in any artifact refuses the build.
func TestAssembleRefusesSensitiveContent(t *testing.T) {
	t.Parallel()

	leaked := cleanInputs()
	leaked.Evidence["results.json"] = []byte("contact leak@gmail.com\n")
	if _, findings := Assemble(leaked); !hasRule(findings, "sensitive-content") {
		t.Errorf("leaked evidence findings = %v, want sensitive-content", findings)
	}

	dirty := cleanInputs()
	dirty.Tree["slo"] = []byte("owner operator@gmail.com\n")
	if _, findings := Assemble(dirty); !hasRule(findings, "sensitive-content") {
		t.Errorf("leaked tree findings = %v, want sensitive-content", findings)
	}
}

// TestVerifyDetectsTampering builds a clean bundle, then proves every
// tamper shape fails: edited bytes, removed files and a rewritten seal.
func TestVerifyDetectsTampering(t *testing.T) {
	t.Parallel()

	tampered := func(t *testing.T, mutate func(dir string)) []Finding {
		t.Helper()
		dir := buildTo(t, cleanInputs())
		mutate(dir)
		return Verify(dir)
	}

	edited := tampered(t, func(dir string) {
		path := filepath.Join(dir, "artifacts", "catalog")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		raw[0] ^= 0xff
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	})
	if !hasRule(edited, "checksum-mismatch") {
		t.Errorf("edited findings = %v, want checksum-mismatch", edited)
	}

	removed := tampered(t, func(dir string) {
		if err := os.Remove(filepath.Join(dir, "evidence", "results.json")); err != nil {
			t.Fatal(err)
		}
	})
	if !hasRule(removed, "missing-artifact") {
		t.Errorf("removed findings = %v, want missing-artifact", removed)
	}

	resealed := tampered(t, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "manifest.sha256"), []byte("0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	if !hasRule(resealed, "checksum-mismatch") {
		t.Errorf("resealed findings = %v, want checksum-mismatch", resealed)
	}

	if findings := Verify(buildTo(t, cleanInputs())); len(findings) != 0 {
		t.Fatalf("clean bundle findings = %v", findings)
	}
}

// initRepo lays out a committable tree (the ten required files, the
// toolchain declaration and one evidence file) inside a fresh git
// repository, so the end-to-end CLI test never depends on the checkout
// it runs in.
func initRepo(t *testing.T) (root, evidence string) {
	t.Helper()
	root = t.TempDir()
	run := func(args ...string) {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = root
		command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+root)
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "synthetic@example.invalid")
	run("config", "user.name", "synthetic")
	for _, want := range treeArtifacts {
		full := filepath.Join(root, want.path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("fixture "+want.name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	toolchain := filepath.Join(root, "quality", "toolchain.json")
	if err := os.WriteFile(toolchain, []byte(`{"pinned":{"go":"1.27.1"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	evidence = filepath.Join(root, "evidence")
	if err := os.MkdirAll(evidence, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evidence, "results.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "fixture")
	return root, evidence
}

// TestCLIBuildsAndVerifiesEndToEnd proves the command contract on a
// hermetic repository: build writes a bundle the check accepts, usage
// errors name themselves, and the same evidence rebuilt is identical.
func TestCLIBuildsAndVerifiesEndToEnd(t *testing.T) {
	root, evidence := initRepo(t)
	first := filepath.Join(t.TempDir(), "one")
	if code := run([]string{"-root", root, "-evidence", evidence, "-out", first}, io.Discard, io.Discard); code != exitOK {
		t.Fatalf("build exit = %d, want %d", code, exitOK)
	}
	if code := run([]string{"-check", first}, io.Discard, io.Discard); code != exitOK {
		t.Fatalf("check exit = %d, want %d", code, exitOK)
	}
	second := filepath.Join(t.TempDir(), "two")
	if code := run([]string{"-root", root, "-evidence", evidence, "-out", second}, io.Discard, io.Discard); code != exitOK {
		t.Fatalf("rebuild exit = %d, want %d", code, exitOK)
	}
	a, _ := os.ReadFile(filepath.Join(first, "manifest.json"))
	b, _ := os.ReadFile(filepath.Join(second, "manifest.json"))
	if string(a) != string(b) {
		t.Fatal("identical evidence produced different manifests")
	}
	if code := run([]string{"-check", filepath.Join(t.TempDir(), "absent")}, io.Discard, io.Discard); code != exitViolation {
		t.Errorf("absent bundle exit = %d, want %d", code, exitViolation)
	}
	if code := run([]string{"-root", root}, io.Discard, io.Discard); code != exitUsage {
		t.Errorf("missing flags exit = %d, want %d", code, exitUsage)
	}
}

func hasRule(findings []Finding, rule string) bool {
	for _, finding := range findings {
		if finding.Rule == rule {
			return true
		}
	}
	return false
}
