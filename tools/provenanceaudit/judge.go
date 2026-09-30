package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/i18ngen"
	"github.com/AlexandreZanata/Regnovum/tools/auditkit"
)

// The rules of this gate. Every finding names one of them, and the report has no
// bucket called "other".
const (
	RuleMissingFamily    = "missing-family"
	RuleUnknownArtifact  = "unknown-artifact"
	RuleMissingOutput    = "missing-output"
	RuleMissingHeader    = "missing-header"
	RuleOutputDrift      = "output-drift"
	RuleStaleInput       = "stale-input"
	RuleVersionDrift     = "version-drift"
	RuleNotReproducible  = "not-reproducible"
	RuleUncoveredProof   = "uncovered-determinism"
	RuleUnignoredProduct = "unignored-build-output"
)

// measured is everything one run observed, including the counts behind the gap
// the report prints: a number that is not printed is a number nobody reviews.
type measured struct {
	Disclosed    int
	Claimed      int
	Families     int
	Outputs      int
	Inputs       int
	Sources      int
	Compared     int
	Rendered     []string
	BuildOutputs int
}

// judge runs every rule over one tree and answers what it refused. The required
// names are the pipelines the phase names — a register that does not cover one of
// them is a register with a hole, and the hole is the finding —, and source is
// the register file itself, which is where a missing family is fixed.
func judge(root string, record register, required []string, source string) ([]auditkit.Finding, measured, error) {
	result := measured{Families: len(record.Families)}
	build := buildOutputs(record)
	result.BuildOutputs = len(build)
	disclosed, err := census(root, build)
	if err != nil {
		return nil, result, err
	}
	result.Disclosed = len(disclosed)
	claimed, err := claimedPaths(root, record)
	if err != nil {
		return nil, result, err
	}
	result.Claimed = len(claimed)

	findings := []auditkit.Finding{}
	if err := verifyRegister(root, record, &result, &findings); err != nil {
		return nil, result, err
	}
	findings = append(findings, judgeCompleteness(record, required, source)...)
	findings = append(findings, judgeCensus(root, disclosed, claimed)...)
	findings = append(findings, judgeBuildOutputs(root, record)...)
	auditkit.SortFindings(findings)
	return findings, result, nil
}

// verifyRegister judges every family against the tree: what it declares it reads
// and writes, the pin of its version, how it is regenerated and how its
// determinism is proven.
func verifyRegister(root string, record register, result *measured, findings *[]auditkit.Finding) error {
	for _, pipe := range record.Families {
		for _, set := range []struct {
			what  string
			items []entry
		}{
			{"insumo", pipe.Inputs},
			{"gerador", pipe.Sources},
			{"artefato", pipe.Outputs},
		} {
			for _, item := range set.items {
				if err := judgeEntry(root, pipe, set.what, item, result, findings); err != nil {
					return err
				}
			}
		}
		*findings = append(*findings, judgeVersion(root, pipe)...)
		rendered, err := judgeReproducible(root, pipe)
		if err != nil {
			return err
		}
		if rendered != nil {
			result.Rendered = append(result.Rendered, pipe.Name)
			*findings = append(*findings, rendered...)
		}
		*findings = append(*findings, judgeProof(root, pipe)...)
	}
	return nil
}

// judgeEntry compares one declared path with the tree: it has to be there, its
// digest has to be the recorded one, and a declared artifact has to carry the
// disclosure that says it is generated.
func judgeEntry(root string, pipe pipeline, what string, item entry, result *measured, findings *[]auditkit.Finding) error {
	digest, files, err := digestEntry(root, item)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			*findings = append(*findings, auditkit.Finding{
				Rule: ruleFor(what, false), Path: location(root, item.Path),
				Detail: fmt.Sprintf("a família %q declara o %s %s e a árvore não o tem: rode %s, ou corrija o registro",
					pipe.Name, what, item.Path, pipe.Command),
			})
			return nil
		}
		return fmt.Errorf("family %q: %s %s: %w", pipe.Name, what, item.Path, err)
	}
	result.Compared++
	countEntry(result, what, len(files))
	if item.Digest != "" && digest != item.Digest {
		*findings = append(*findings, auditkit.Finding{
			Rule: ruleFor(what, true), Path: location(root, item.Path),
			Detail: fmt.Sprintf("o %s %s mudou desde a regeneração (registro %s, árvore %s): rode %s e atualize o registro",
				what, item.Path, item.Digest, digest, pipe.Command),
		})
	}
	if what != "artefato" || item.Header == "" {
		return nil
	}
	return judgeHeader(root, pipe, item, files, findings)
}

// judgeHeader requires every declared artifact to carry the disclosure of its
// family: a generated file that does not say so is a generated file a reader will
// edit by hand.
func judgeHeader(root string, pipe pipeline, item entry, files []string, findings *[]auditkit.Finding) error {
	for _, relative := range files {
		_, found, err := disclosure(root, relative)
		if err != nil {
			return err
		}
		if found {
			continue
		}
		*findings = append(*findings, auditkit.Finding{
			Rule: RuleMissingHeader, Path: location(root, relative), Line: 1,
			Detail: fmt.Sprintf("a família %q declara %s como artefato e o arquivo não carrega o aviso %q: um gerado que não se anuncia é um gerado que alguém edita à mão",
				pipe.Name, relative, item.Header),
		})
	}
	return nil
}

func countEntry(result *measured, what string, files int) {
	switch what {
	case "artefato":
		result.Outputs += files
	case "insumo":
		result.Inputs += files
	default:
		result.Sources += files
	}
}

func ruleFor(what string, drift bool) string {
	switch {
	case what == "artefato" && drift:
		return RuleOutputDrift
	case what == "artefato":
		return RuleMissingOutput
	default:
		return RuleStaleInput
	}
}

// judgeVersion reads the pin where the register says the tree declares it. A
// version refreshed in the register without regenerating the artifact is refused
// here, which is the whole point of recording the evidence path — and a pin
// without an evidence path is refused as an annotation nobody can check.
func judgeVersion(root string, pipe pipeline) []auditkit.Finding {
	if pipe.Version == "" {
		if pipe.VersionEvidence == "" {
			return nil
		}
		return []auditkit.Finding{{
			Rule: RuleVersionDrift, Path: location(root, pipe.VersionEvidence),
			Detail: fmt.Sprintf("a família %q aponta a evidência %s e não declara a versão: a evidência de um pino é a versão que ela prova",
				pipe.Name, pipe.VersionEvidence),
		}}
	}
	if pipe.VersionEvidence == "" {
		return []auditkit.Finding{{
			Rule: RuleVersionDrift, Path: pipe.Name,
			Detail: fmt.Sprintf("a família %q fixa %s e não declara onde a árvore declara esse texto: um pino sem evidência é uma anotação",
				pipe.Name, pipe.Version),
		}}
	}
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(pipe.VersionEvidence)))
	if err != nil {
		return []auditkit.Finding{{
			Rule: RuleVersionDrift, Path: location(root, pipe.VersionEvidence),
			Detail: fmt.Sprintf("a família %q fixa %s em %s e o arquivo não pôde ser lido: %v",
				pipe.Name, pipe.Version, pipe.VersionEvidence, err),
		}}
	}
	if strings.Contains(string(body), pipe.Version) {
		return nil
	}
	return []auditkit.Finding{{
		Rule: RuleVersionDrift, Path: location(root, pipe.VersionEvidence),
		Detail: fmt.Sprintf("a família %q fixa %s e %s não declara esse texto: a versão mudou sem regenerar o artefato",
			pipe.Name, pipe.Version, pipe.VersionEvidence),
	}}
}

// judgeReproducible re-runs the generator in process, twice, for the families
// that declare one this gate can call, and compares both runs with the tree. A
// nil answer is a family that declared no renderer; the empty answer is a
// generator whose artifacts are exactly what it produces.
func judgeReproducible(root string, pipe pipeline) ([]auditkit.Finding, error) {
	if pipe.Render == nil {
		return nil, nil
	}
	if pipe.Render.Kind != renderI18N {
		return nil, fmt.Errorf("family %q declares the renderer %q, and this gate runs %q",
			pipe.Name, pipe.Render.Kind, renderI18N)
	}
	outputs, err := i18nOutputs(root, pipe)
	if err != nil {
		return []auditkit.Finding{{Rule: RuleNotReproducible, Path: pipe.Name, Detail: err.Error()}}, nil
	}
	root_ := filepath.Join(root, filepath.FromSlash(pipe.Render.Root))
	firstTS, firstGo, err := i18ngen.RenderAll(root_, outputs)
	if err != nil {
		return nil, fmt.Errorf("family %q: render: %w", pipe.Name, err)
	}
	secondTS, secondGo, err := i18ngen.RenderAll(root_, outputs)
	if err != nil {
		return nil, fmt.Errorf("family %q: render again: %w", pipe.Name, err)
	}
	if string(firstTS) != string(secondTS) || string(firstGo) != string(secondGo) {
		return []auditkit.Finding{{
			Rule: RuleNotReproducible, Path: pipe.Name,
			Detail: fmt.Sprintf("duas execuções do gerador %s da família %q não produzem os mesmos bytes: um artefato que o gerador não reproduz não pode ser conferido por checksum",
				pipe.Generator, pipe.Name),
		}}, nil
	}
	return compareRender(root, pipe, outputs, [2][]byte{firstTS, firstGo}), nil
}

// compareRender holds the produced bytes against the tree, one declared artifact
// at a time: the finding names the artifact a reader has to regenerate.
func compareRender(root string, pipe pipeline, outputs i18ngen.Outputs, produced [2][]byte) []auditkit.Finding {
	findings := []auditkit.Finding{}
	for index, target := range []string{outputs.TSTarget, outputs.GoTarget} {
		declared := pipe.Outputs[index]
		inTree, err := os.ReadFile(target)
		if err != nil {
			findings = append(findings, auditkit.Finding{
				Rule: RuleNotReproducible, Path: location(root, declared.Path),
				Detail: fmt.Sprintf("a família %q declara este artefato e ele não está na árvore: rode %s", pipe.Name, pipe.Command),
			})
			continue
		}
		if string(inTree) == string(produced[index]) {
			continue
		}
		findings = append(findings, auditkit.Finding{
			Rule: RuleNotReproducible, Path: location(root, declared.Path),
			Detail: fmt.Sprintf("a árvore não é o que o gerador %s da família %q produz agora: rode %s",
				pipe.Generator, pipe.Name, pipe.Command),
		})
	}
	return findings
}

// i18nOutputs reads where the family writes: the renderer is given the declared
// artifacts as its targets, so what the gate compares is exactly what the
// generator would write over the tree.
func i18nOutputs(root string, pipe pipeline) (i18ngen.Outputs, error) {
	if len(pipe.Outputs) != 2 {
		return i18ngen.Outputs{}, fmt.Errorf("family %q declares %d artifact(s) and the i18n renderer writes two",
			pipe.Name, len(pipe.Outputs))
	}
	return i18ngen.Outputs{
		TSTarget:  filepath.Join(root, filepath.FromSlash(pipe.Outputs[0].Path)),
		GoTarget:  filepath.Join(root, filepath.FromSlash(pipe.Outputs[1].Path)),
		GOPackage: pipe.Render.GOPackage,
	}, nil
}

// judgeProof refuses the family that leaves its determinism unproven and
// unstated: either this gate re-runs the generator, or a target runs the
// generator again and compares, or a test of the tree owns the property. A family
// that says none of the three has no answer to "what proves this is
// deterministic".
func judgeProof(root string, pipe pipeline) []auditkit.Finding {
	if pipe.Render != nil || pipe.VerifyCommand != "" {
		return nil
	}
	if pipe.OwnerTest == nil {
		return []auditkit.Finding{{
			Rule: RuleUncoveredProof, Path: pipe.Name,
			Detail: fmt.Sprintf("a família %q não declara como a reprodutibilidade do seu artefato é provada: um gerador que ninguém roda duas vezes é um gerador que ninguém sabe se é determinístico", pipe.Name),
		}}
	}
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(pipe.OwnerTest.Path)))
	if err != nil {
		return []auditkit.Finding{{
			Rule: RuleUncoveredProof, Path: location(root, pipe.OwnerTest.Path),
			Detail: fmt.Sprintf("a família %q delega a prova de determinismo a %s e o arquivo não está lá",
				pipe.Name, pipe.OwnerTest.Path),
		}}
	}
	if !strings.Contains(string(body), "func "+pipe.OwnerTest.Function+"(") {
		return []auditkit.Finding{{
			Rule: RuleUncoveredProof, Path: location(root, pipe.OwnerTest.Path),
			Detail: fmt.Sprintf("a família %q delega a prova de determinismo a %s e o teste não está em %s",
				pipe.Name, pipe.OwnerTest.Function, pipe.OwnerTest.Path),
		}}
	}
	return nil
}

// judgeCompleteness refuses a pipeline the phase names and the register forgets:
// a generator outside the register is an artifact whose origin nobody records.
func judgeCompleteness(record register, required []string, source string) []auditkit.Finding {
	findings := []auditkit.Finding{}
	for _, name := range required {
		if _, found := record.named(name); found {
			continue
		}
		findings = append(findings, auditkit.Finding{
			Rule: RuleMissingFamily, Path: source, Line: 1,
			Detail: fmt.Sprintf("a fase nomeia a família %q e o registro não a declara: um pipeline sem registro é um artefato cujo gerador ninguém sabe qual é", name),
		})
	}
	return findings
}

// judgeCensus refuses the artifact that announces itself as generated and that no
// family declares: it was produced by something, and the register is where that
// something is written down.
func judgeCensus(root string, disclosed []string, claimed map[string]bool) []auditkit.Finding {
	findings := []auditkit.Finding{}
	for _, relative := range disclosed {
		if claimed[relative] {
			continue
		}
		line, _, _ := disclosure(root, relative)
		findings = append(findings, auditkit.Finding{
			Rule: RuleUnknownArtifact, Path: location(root, relative), Line: line,
			Detail: "o arquivo se anuncia gerado e nenhuma família o declara: registre o gerador, os insumos e o checksum, ou remova o artefato da árvore",
		})
	}
	return findings
}

// judgeBuildOutputs requires every declared product of a build to be held by
// `.gitignore`: the products are the reason a build exists, and one of them
// committed is one of them aged inside the repository.
func judgeBuildOutputs(root string, record register) []auditkit.Finding {
	findings := []auditkit.Finding{}
	for _, entry := range record.Families {
		for _, path := range entry.BuildOutputs {
			ignored, err := ignoredByGit(root, path)
			if err == nil && ignored {
				continue
			}
			findings = append(findings, auditkit.Finding{
				Rule: RuleUnignoredProduct, Path: location(root, path),
				Detail: fmt.Sprintf("a família %q declara %s como produto de build e o .gitignore raiz não o cobre: um produto de build que o repositório aceita é um produto de build que envelhece dentro dele",
					entry.Name, path),
			})
		}
	}
	return findings
}

// claimedPaths is every file the register declares as an artifact: the census
// answers what is left.
func claimedPaths(root string, record register) (map[string]bool, error) {
	claimed := map[string]bool{}
	for _, entry := range record.Families {
		for _, item := range entry.Outputs {
			files, err := resolve(root, item)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			for _, file := range files {
				claimed[file] = true
			}
		}
	}
	return claimed, nil
}

// buildOutputs is every path the register declares as a product of a build.
func buildOutputs(record register) []string {
	paths := []string{}
	for _, entry := range record.Families {
		paths = append(paths, entry.BuildOutputs...)
	}
	return paths
}

// location names a path the way a reader reaches it from the repository root.
func location(root, relative string) string {
	return filepath.ToSlash(filepath.Join(root, filepath.FromSlash(relative)))
}
