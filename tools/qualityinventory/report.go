package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The two artifacts of the phase: the machine report and the document a person
// reads. Both are generated from the same value, and the command refuses to
// leave them disagreeing.
const (
	ReportPath = "quality/coverage.json"
	DocPath    = "docs/quality/COVERAGE.md"
)

// Render encodes the report. The encoding is stable — fixed field order, sorted
// lists, indented, one trailing newline — because the command compares the
// bytes it generates with the bytes that are committed, and an unstable
// encoding would report drift on every run.
func Render(report Report) ([]byte, error) {
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("the report cannot be encoded: %w", err)
	}
	return append(encoded, '\n'), nil
}

// Parse reads a committed report, strictly: a report written against a contract
// this tool does not know is refused rather than half-read.
func Parse(raw []byte) (Report, error) {
	var report Report
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return Report{}, fmt.Errorf("the report does not decode: %w", err)
	}
	return report, nil
}

// RenderMarkdown writes the inventory a person reads. It states no number the
// report does not carry: the document is a rendering, not a second source of
// truth, and a count typed here would rot the first day nobody regenerated it.
func RenderMarkdown(report Report) []byte {
	var out strings.Builder
	out.WriteString("# Inventário de cobertura semântica\n\n")
	out.WriteString("**Status:** relatório gerado por `tools/qualityinventory` — não editar à mão; rode `make quality-inventory-write` e revise o diff.\n\n")
	out.WriteString("**Documentos cruzados:** [catalog.json](../../quality/catalog.json) · [evidence.json](../../quality/evidence.json) · [REQUIREMENTS.md](../REQUIREMENTS.md)\n\n")
	out.WriteString("**Famílias lidas:** rotas do contrato (`api/openapi.json`), migrations (`internal/platform/dbmigrate/migrations/`), casos de uso (`internal/<módulo>/application/`), tipos de job (`internal/jobs/domain/types.go`) e comandos de CLI (`cmd/arena/main.go`), além das referências de teste que os três documentos citam.\n\n")
	out.WriteString("---\n\n")

	out.WriteString("## 1. Resumo\n\n")
	out.WriteString("| Medida | Valor |\n| --- | --- |\n")
	fmt.Fprintf(&out, "| Regras no catálogo | %d |\n", report.Catalog.Rules)
	fmt.Fprintf(&out, "| Regras críticas (Q0) | %d |\n", report.Catalog.Critical)
	fmt.Fprintf(&out, "| **Cobertas** | **%d** |\n", report.Summary.Covered)
	fmt.Fprintf(&out, "| **Ausentes** | **%d** |\n", report.Summary.Absent)
	fmt.Fprintf(&out, "| Identidades de evidência | %d |\n", report.Summary.Evidence)
	fmt.Fprintf(&out, "| Citações resolvidas | %d de %d |\n", report.Summary.Citations-report.Summary.Obsolete, report.Summary.Citations)
	fmt.Fprintf(&out, "| **Obsoletas** | **%d** |\n", report.Summary.Obsolete)
	fmt.Fprintf(&out, "| Artefatos lidos | %d |\n", report.Summary.Artifacts)
	fmt.Fprintf(&out, "| **Órfãos** | **%d** |\n\n", report.Summary.Orphans)
	out.WriteString("Cobertura é contada **por regra, nunca por linha**: uma regra está coberta quando um teste que a prova, uma rota, um caso de uso ou uma migration que a carrega, ou uma identidade de evidência que a declara existe de verdade no checkout. Um arquivo executado por um teste não torna coberta nenhuma regra.\n\n")

	out.WriteString("## 2. As seis famílias\n\n")
	out.WriteString("| Família | Artefatos | Rastreados | Órfãos | Citações | Não resolvidas |\n| --- | --- | --- | --- | --- | --- |\n")
	for _, family := range report.Families {
		fmt.Fprintf(&out, "| %s | %d | %d | %d | %d | %d |\n", family.Label, family.Artifacts, family.Named, family.Orphans, family.Citations, family.Unresolved)
	}
	out.WriteString("\nUm artefato é rastreado quando um documento o cita pelo nome **ou** quando uma regra do catálogo cobre o pacote que o possui — o segundo critério é grosseiro de propósito, porque uma rota pertence ao módulo que a serve e esse módulo é o que uma regra alcança. O que ninguém cita e nenhuma regra cobre é a superfície que a qualidade ainda não alcança: um achado, não um portão.\n\nA família **Referências de teste** não tem universo de artefatos: uma referência é uma citação, não algo que o inventário enumere, e por isso ela aparece nas duas últimas colunas.\n\n")

	out.WriteString("## 3. Coberto\n\n")
	byRisk := map[string]int{}
	for _, covered := range report.Covered {
		byRisk[covered.Risk]++
	}
	for _, class := range []string{"Q0", "Q1", "Q2"} {
		fmt.Fprintf(&out, "- **%s**: %d regra(s)\n", class, byRisk[class])
	}
	out.WriteString("\n| Regra | Classe | Âncoras | Testes | Evidências |\n| --- | --- | --- | --- | --- |\n")
	for _, covered := range report.Covered {
		fmt.Fprintf(&out, "| `%s` | %s | %s | %d | %s |\n",
			covered.Rule, covered.Risk, strings.Join(covered.Anchors, ", "), covered.Tests, emptyAsDash(strings.Join(covered.Evidence, ", ")))
	}
	out.WriteString("\n")

	out.WriteString("## 4. Ausente\n\n")
	writeCoveredSection(&out, report.Absent, "Nenhuma regra está sem âncora: toda regra do catálogo é provada por algo que existe no checkout.")

	out.WriteString("## 5. Obsoleto\n\n")
	if len(report.Obsolete) == 0 {
		out.WriteString("Nenhuma citação aponta para o vazio: tudo o que os três documentos nomeiam existe no checkout.\n\n")
	} else {
		out.WriteString("| Documento | Linha | Família | Referência |\n| --- | --- | --- | --- |\n")
		for _, obsolete := range report.Obsolete {
			fmt.Fprintf(&out, "| `%s` | `%s` | %s | `%s` |\n", obsolete.Document, obsolete.Row, obsolete.Family, obsolete.Reference)
		}
		out.WriteString("\n")
	}

	out.WriteString("## 6. Órfão\n\n")
	if len(report.Orphans) == 0 {
		out.WriteString("Nenhum artefato está sem rastro.\n\n")
	} else {
		grouped := map[string][]Orphan{}
		for _, orphan := range report.Orphans {
			grouped[orphan.Family] = append(grouped[orphan.Family], orphan)
		}
		families := make([]string, 0, len(grouped))
		for family := range grouped {
			families = append(families, family)
		}
		sort.Strings(families)
		for _, family := range families {
			fmt.Fprintf(&out, "**%s** (%d)\n\n", Labels[family], len(grouped[family]))
			for _, orphan := range grouped[family] {
				fmt.Fprintf(&out, "- `%s` — pacote `%s`\n", orphan.Name, orphan.Owner)
			}
			out.WriteString("\n")
		}
		out.WriteString("Um comando de operador ou um tipo de job pode ser órfão sem ser defeito: o que este relatório afirma é que nenhuma regra de negócio o alcança, e é a quem lê que cabe decidir se deveria.\n\n")
	}

	out.WriteString("## 7. O que este relatório decide\n\n")
	out.WriteString("`make quality-inventory` **falha** quando uma regra não tem âncora alguma, quando uma citação não resolve e quando o relatório versionado diverge do que a árvore gera. Ele **não** falha por órfão: a lista é o achado, e transformá-la em portão exigiria uma política que a fase não deu.\n")
	return []byte(out.String())
}

// writeCoveredSection prints the rows of a bucket, or the sentence that says
// the bucket is empty and why that is the good news.
func writeCoveredSection(out *strings.Builder, rows []Covered, empty string) {
	if len(rows) == 0 {
		out.WriteString(empty + "\n\n")
		return
	}
	out.WriteString("| Regra | Classe | Âncoras | Testes | Evidências |\n| --- | --- | --- | --- | --- |\n")
	for _, row := range rows {
		fmt.Fprintf(out, "| `%s` | %s | %s | %d | %s |\n",
			row.Rule, row.Risk, emptyAsDash(strings.Join(row.Anchors, ", ")), row.Tests, emptyAsDash(strings.Join(row.Evidence, ", ")))
	}
	out.WriteString("\n")
}

// emptyAsDash keeps a table cell readable when it carries nothing: an empty
// cell in a Markdown table reads as a broken row.
func emptyAsDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}
