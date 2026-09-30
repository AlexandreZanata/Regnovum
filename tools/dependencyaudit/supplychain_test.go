package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// readTestFile reads a tree file for the certification tests.
func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}

// parseSBOMModules answers name->version of the module lines of the bill
// of materials, reusing the gate's own document shape.
func parseSBOMModules(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	var document sbom
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode sbom: %v", err)
	}
	modules := map[string]string{}
	for _, component := range document.Components {
		if component.Kind == "module" {
			modules[component.Name] = component.Version
		}
	}
	if len(modules) == 0 {
		t.Fatal("sbom lists no modules")
	}
	return modules
}

// P26-T11 — certificação supply chain do binário entregue.
//
// O `make vuln` (govulncheck) e o `make audit-deps` (este portão) julgam a
// árvore; estes testes prendem as pontas que os portões declaram mas só
// esta prova amarra ao artefato: o pacote vulnerável inalcançável fora do
// binário, a lista de materiais igual ao build, e a receita da imagem sem
// root, toolchain ou segredo. Sem rede, sem daemon, sem skip.

// goListDeps responde o fecho de importação de um pacote pelo toolchain
// que o testa: funciona offline com o cache de módulos completo.
func goListDeps(t *testing.T, pattern string) []string {
	t.Helper()
	output, err := exec.Command("go", "list", "-deps", pattern).Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pattern, pattern)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	dependencies := make([]string, 0, len(lines))
	for _, line := range lines {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			dependencies = append(dependencies, trimmed)
		}
	}
	return dependencies
}

// TestSupplyChainUnreachableVulnAbsentFromBinary prende a triagem do
// GO-2026-5932 (x/crypto/openpgp sem manutenção): o módulo é exigido,
// nenhum pacote dele é importado, e o binário entregue prova com a
// própria lista de deps — além da lista de banidos do registro, que
// nenhum import pode casar.
func TestSupplyChainUnreachableVulnAbsentFromBinary(t *testing.T) {
	t.Chdir("../..")
	dependencies := goListDeps(t, "./cmd/arena")
	for _, dependency := range dependencies {
		if strings.Contains(dependency, "golang.org/x/crypto/openpgp") {
			t.Fatalf("binary links unreachable vulnerable package %q (GO-2026-5932)", dependency)
		}
	}
	document, err := readRegister(".", "quality/dependencies.json")
	if err != nil {
		t.Fatalf("read register: %v", err)
	}
	for _, dependency := range dependencies {
		if match := bannedMatch(document.Banned, dependency); match != "" {
			t.Fatalf("binary links banned package %q (matched by %q)", dependency, match)
		}
	}
}

// goModRequires lê os blocos require do go.mod sem resolver nada: nome e
// versão exatos como declarados, diretos e indiretos.
func goModRequires(t *testing.T, path string) map[string]string {
	t.Helper()
	raw := readTestFile(t, path)
	requires := map[string]string{}
	inside := false
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "require (") {
			inside = true
			continue
		}
		if inside && trimmed == ")" {
			inside = false
			continue
		}
		if !inside || trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) >= 2 {
			requires[fields[0]] = fields[1]
		}
	}
	if len(requires) == 0 {
		t.Fatal("go.mod declares no requirements")
	}
	return requires
}

// TestSupplyChainSBOMMatchesGoMod prova que a lista de materiais é o
// build: todo require do go.mod está no SBOM na mesma versão, e todo
// módulo do SBOM ainda é require (as duas direções que o drift tem).
func TestSupplyChainSBOMMatchesGoMod(t *testing.T) {
	t.Chdir("../..")
	requires := goModRequires(t, "go.mod")
	document := parseSBOMModules(t, readTestFile(t, "quality/sbom.json"))
	for name, version := range requires {
		got, ok := document[name]
		if !ok {
			t.Errorf("go.mod requires %s %s, missing from sbom.json", name, version)
			continue
		}
		if got != version {
			t.Errorf("sbom pins %s %s, go.mod resolves %s", name, got, version)
		}
	}
	for name := range document {
		if _, ok := requires[name]; !ok {
			t.Errorf("sbom lists %s, no longer required by go.mod", name)
		}
	}
}

// TestSupplyChainImageRecipe prova a receita entregue sem daemon: estágio
// de runtime não-root numérico, sem toolchain além do necessário, sem
// credencial em ARG/ENV e digest em toda base.
func TestSupplyChainImageRecipe(t *testing.T) {
	t.Chdir("../..")
	recipe := string(readTestFile(t, "Dockerfile"))
	stages := strings.Count(recipe, "\nFROM ")
	if stages < 1 {
		t.Fatal("Dockerfile declares no multi-stage build")
	}
	runtime := recipe[strings.LastIndex(recipe, "\nFROM "):]
	if !strings.Contains(runtime, "distroless") {
		t.Fatal("runtime stage is not distroless")
	}
	if !strings.Contains(runtime, "USER 65532") {
		t.Fatal("runtime stage does not run as numeric non-root")
	}
	for _, line := range strings.Split(recipe, "\n") {
		trimmed := strings.TrimSpace(line)
		if !(strings.HasPrefix(trimmed, "ARG ") || strings.HasPrefix(trimmed, "ENV ")) {
			continue
		}
		upper := strings.ToUpper(trimmed)
		for _, marker := range []string{"PASSWORD", "SECRET", "TOKEN", "PRIVATE_KEY", "DSN", "DATABASE_URL"} {
			if strings.Contains(upper, marker) && !strings.Contains(upper, "ARENA_ADDR") {
				t.Errorf("Dockerfile carries a credential-shaped variable: %q", trimmed)
			}
		}
	}
	for _, line := range strings.Split(recipe, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "FROM ") {
			continue
		}
		if !strings.Contains(trimmed, "@sha256:") {
			t.Errorf("unpinned base image: %q", trimmed)
		}
	}
	if strings.Contains(recipe, "\nADD ") {
		t.Error("Dockerfile uses ADD instead of COPY")
	}
}
