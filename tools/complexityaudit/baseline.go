package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// The two shapes a finding comes in. The baseline holds both, and the kind is
// part of the identity: a function and a copied block are never the same entry.
const (
	kindFunction    = "function"
	kindDuplication = "duplication"
)

// baselineSchema is the only document version this gate reads. A baseline read
// by the wrong reader is a baseline that accepts the wrong things.
const baselineSchema = 1

// baselineNote is written by the tool, so that the document's own account of
// itself cannot drift from the gate that produced it. It states the two things a
// reader has to know before reading a line of it.
const baselineNote = "Dívida de complexidade medida quando o portão entrou em vigor (P23-T03). O documento é um trinco: um achado novo ou que cresceu reprova, um achado que encolheu exige que a linha desça até o valor medido, e a entrada cujo achado a árvore não produz mais tem de sair. Cada entrada nomeia como dono o diretório do pacote que carrega a dívida — a P23-T03 não tem tarefa seguinte que a pague, e um dono inventado seria uma dívida que ninguém pode cobrar. O portão nunca reescreve este arquivo; `-print-findings` imprime a lista inteira para um humano atualizar."

// Entry is one accepted finding: what it is, how much of it there is, and who
// answers for it.
//
// The identity of a function entry is (path, function, metric): the line is
// absent on purpose, because moving a function does not make its complexity a
// new finding. The identity of a duplicated block is (digest, paths): the digest
// is of the shared text, so the same copy renamed in place is the same entry, and
// a copy edited into a different block is a different one.
type Entry struct {
	Kind     string   `json:"kind"`
	Path     string   `json:"path,omitempty"`
	Function string   `json:"function,omitempty"`
	Metric   string   `json:"metric,omitempty"`
	Value    int      `json:"value"`
	Owner    string   `json:"owner"`
	Digest   string   `json:"digest,omitempty"`
	Paths    []string `json:"paths,omitempty"`
}

// Baseline is the versioned list of accepted findings.
type Baseline struct {
	Schema  int     `json:"schema"`
	Note    string  `json:"note"`
	Entries []Entry `json:"entries"`
}

// key is the identity the comparison matches on, derived the same way from both
// sides: a finding of the tree and a line of the document.
func (e Entry) key() string {
	if e.Kind == kindDuplication {
		paths := append([]string{}, e.Paths...)
		sort.Strings(paths)
		return kindDuplication + "\x00" + e.Digest + "\x00" + strings.Join(paths, "\x00")
	}
	return kindFunction + "\x00" + e.Path + "\x00" + e.Function + "\x00" + e.Metric
}

// entryOf turns a measured finding into the line that accepts it.
func entryOf(f finding) Entry {
	if f.Kind == kindDuplication {
		return Entry{Kind: kindDuplication, Value: f.Value, Owner: f.Owner, Digest: f.Digest, Paths: f.Paths()}
	}
	return Entry{Kind: kindFunction, Path: f.Path, Function: f.Function, Metric: f.Metric, Value: f.Value, Owner: f.Owner}
}

// loadBaseline reads the document and judges the document itself before any
// comparison: a line that cannot be held to anything is not a line that accepts
// a finding.
func loadBaseline(path string) (*Baseline, []string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, []string{fmt.Sprintf("read the baseline %s: %v", path, err)}
	}
	baseline := &Baseline{}
	if err := json.Unmarshal(raw, baseline); err != nil {
		return nil, []string{fmt.Sprintf("parse the baseline %s: %v", path, err)}
	}
	violations := judgeDocument(baseline)
	if len(violations) > 0 {
		return nil, violations
	}
	return baseline, nil
}

// judgeDocument holds the document to its own shape, line by line.
func judgeDocument(baseline *Baseline) []string {
	violations := []string{}
	if baseline.Schema != baselineSchema {
		violations = append(violations, fmt.Sprintf("the baseline declares schema %d; this gate reads schema %d", baseline.Schema, baselineSchema))
	}
	seen := map[string]bool{}
	for _, entry := range baseline.Entries {
		line := describe(entry)
		switch entry.Kind {
		case kindFunction:
			if entry.Path == "" || entry.Function == "" || entry.Metric == "" {
				violations = append(violations, fmt.Sprintf("%s has no path, function or metric: an accepted finding says what it accepts", line))
			}
			if _, known := metricBudget(entry.Path, entry.Metric); !known {
				violations = append(violations, fmt.Sprintf("%s names the %q measure, which no budget covers", line, entry.Metric))
			}
		case kindDuplication:
			// A block copied inside one file is one path and still a copy: the
			// document holds the digest of the shared text and where it lives,
			// and the number of places is what the report prints.
			if entry.Digest == "" || len(entry.Paths) < 1 {
				violations = append(violations, fmt.Sprintf("%s is a duplicated block without a digest or without a place", line))
			}
		default:
			violations = append(violations, fmt.Sprintf("%s declares the kind %q, and this gate knows %q and %q", line, entry.Kind, kindFunction, kindDuplication))
		}
		if entry.Value < 1 {
			violations = append(violations, fmt.Sprintf("%s accepts %d: an accepted finding is one or more", line, entry.Value))
		}
		if strings.TrimSpace(entry.Owner) == "" {
			violations = append(violations, fmt.Sprintf("%s names no owner: an accepted finding says who answers for it", line))
		}
		if key := entry.key(); seen[key] {
			violations = append(violations, fmt.Sprintf("%s is listed twice: one finding, one line", line))
		} else {
			seen[key] = true
		}
	}
	return violations
}

// judgeEntries holds the document against the floors and against the tree. It is
// the ratchet: nothing new, nothing bigger, nothing that accepts what the budget
// already allows, and nothing that outlived the finding it accepted.
func judgeEntries(findings []finding, baseline *Baseline, path string) []string {
	violations := []string{}
	accepted := map[string]Entry{}
	for _, entry := range baseline.Entries {
		accepted[entry.key()] = entry
		if entry.Value <= entryFloor(entry) {
			violations = append(violations, fmt.Sprintf(
				"the baseline entry %s accepts %d, and the budget already allows %d: an entry that accepts what the budget allows is a hole, not a floor — delete the line",
				describe(entry), entry.Value, entryFloor(entry)))
		}
		if owner := derivedOwner(entry); owner != "" && owner != entry.Owner {
			violations = append(violations, fmt.Sprintf(
				"the baseline entry %s names the owner %q, and the package that carries it is %q: a debt with the wrong owner is a debt nobody can be asked about",
				describe(entry), entry.Owner, owner))
		}
	}

	current := map[string]bool{}
	for _, measured := range findings {
		entry, known := accepted[measured.identity()]
		current[measured.identity()] = true
		if !known {
			violations = append(violations, fmt.Sprintf(
				"%s — not in the baseline (%s): simplify it, or have a human accept it with the owner that answers for it",
				measured.String(), path))
			continue
		}
		switch {
		case measured.Value > entry.Value:
			violations = append(violations, fmt.Sprintf(
				"%s: %s is measured at %d and the baseline accepts %d — the baseline is a ratchet, and it only goes down",
				location(measured), measured.Metric, measured.Value, entry.Value))
		case measured.Value < entry.Value:
			violations = append(violations, fmt.Sprintf(
				"%s: %s is measured at %d and the baseline accepts %d — lower the line to the measured value: the ratchet records the improvement, it does not keep the old number",
				location(measured), measured.Metric, measured.Value, entry.Value))
		}
	}
	for _, entry := range baseline.Entries {
		if !current[entry.key()] {
			violations = append(violations, fmt.Sprintf(
				"the baseline still accepts %s, which the tree no longer produces: remove the line (owner was %s)",
				describe(entry), entry.Owner))
		}
	}
	sort.Strings(violations)
	return violations
}

// entryFloor is the value a line has to exceed to be a floor rather than a hole:
// the budget of the measure for the scope of the file, or the shortest copied
// block the gate recognizes.
func entryFloor(entry Entry) int {
	if entry.Kind == kindDuplication {
		return duplicationWindow - 1
	}
	if declared, known := metricBudget(entry.Path, entry.Metric); known {
		return declared
	}
	return 0
}

// metricBudget answers the budget of one measure for the file it was measured in,
// which is what makes an entry's floor decidable from the document alone.
func metricBudget(path, metric string) (int, bool) {
	scope, classified := scopeOf(path)
	if !classified {
		return 0, false
	}
	declared, known := budgetFor(scope, metric)
	if !known {
		return 0, false
	}
	return declared.Budget, true
}

// derivedOwner is the owner the document has to name for one line: the package
// directory of the file a function lives in, and the first place (in order) of a
// duplicated block. A block copied between two packages is owned where its first
// copy is; every place is printed by the report, so nothing is hidden by it.
func derivedOwner(entry Entry) string {
	if entry.Kind == kindDuplication {
		if len(entry.Paths) == 0 {
			return ""
		}
		paths := append([]string{}, entry.Paths...)
		sort.Strings(paths)
		return ownerOf(paths[0])
	}
	if entry.Path == "" {
		return ""
	}
	return ownerOf(entry.Path)
}

func describe(entry Entry) string {
	if entry.Kind == kindDuplication {
		return fmt.Sprintf("the copied block %s in %s", shortDigest(entry.Digest), strings.Join(entry.Paths, ", "))
	}
	return fmt.Sprintf("%s %s of %s", entry.Metric, entry.Function, entry.Path)
}

func shortDigest(digest string) string {
	if len(digest) > 8 {
		return digest[:8]
	}
	return digest
}

func location(f finding) string {
	return fmt.Sprintf("%s:%d", f.Path, f.Line)
}

// render writes the document the way it is read: one entry per line, so that a
// tree carrying a hundred of them is a hundred-line diff and not a file nobody
// opens. The tool is the only writer; the reader accepts any whitespace.
func render(baseline *Baseline) string {
	builder := strings.Builder{}
	builder.WriteString("{\n")
	fmt.Fprintf(&builder, "  \"schema\": %d,\n", baseline.Schema)
	note, err := json.Marshal(baseline.Note)
	if err != nil {
		note = []byte(`""`)
	}
	fmt.Fprintf(&builder, "  \"note\": %s,\n", note)
	builder.WriteString("  \"entries\": [\n")
	for index, entry := range baseline.Entries {
		encoded, err := json.Marshal(entry)
		if err != nil {
			continue
		}
		fmt.Fprintf(&builder, "    %s", encoded)
		if index < len(baseline.Entries)-1 {
			builder.WriteString(",")
		}
		builder.WriteString("\n")
	}
	builder.WriteString("  ]\n}\n")
	return builder.String()
}
