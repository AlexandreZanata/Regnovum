package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/tools/auditkit"
)

// waiverPath is the register of accepted exceptions. It is JSON and it is
// versioned because the program says so: "Qualquer waiver é JSON versionado, tem
// risco, responsável, justificativa, data de expiração e teste compensatório"
// (`QUALITY_PROGRAM.md`). The gate reads it and never writes it — a refreshed
// register is a decision of a human, not of a gate.
const waiverPath = "quality/test-waivers.json"

// waiverSchemaVersion is the only version this loader understands.
const waiverSchemaVersion = 1

// The risk classes the catalog uses. A critical class is refused here: the phase
// asks for a non-critical waiver, and `Q0` is the class of the rules this
// repository does not trade.
const (
	riskCritical = "Q0"
	riskHigh     = "Q1"
	riskNormal   = "Q2"
)

// waiverRisks is the whole vocabulary, exactly as the catalog states it.
var waiverRisks = []string{riskHigh, riskNormal}

// forbiddenRisk is the class a waiver may not declare.
var forbiddenRisk = riskCritical

// shortestJustification is the floor a justification has to reach: an exception
// whose reason does not fit in a sentence is an exception nobody reconsidered.
const shortestJustification = 20

// dateLayout is the one format a date may use.
const dateLayout = "2006-01-02"

var waiverIDPattern = regexp.MustCompile(`^TST-[0-9]{3}$`)

// addressPattern is what a personal address looks like: a waiver is owned by a
// role or a team, and the register is read by everybody.
var addressPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// waiver is one accepted exception, with everything a reader needs to judge it
// without asking its author.
type waiver struct {
	ID            string `json:"id"`
	Rule          string `json:"rule"`
	Site          string `json:"site"`
	Risk          string `json:"risk"`
	Owner         string `json:"owner"`
	Justification string `json:"justification"`
	Compensation  string `json:"compensation"`
	Created       string `json:"created"`
	Expires       string `json:"expires"`
}

// waiverRegister is the whole document. An empty one is the state a healthy tree
// is in: nothing is waived.
type waiverRegister struct {
	Schema  int      `json:"schema"`
	Note    string   `json:"note"`
	Waivers []waiver `json:"waivers"`
}

// readWaivers reads the register and judges it against the findings of the run.
// The two halves are separate on purpose: the document is refused for what it
// says, and a finding is accepted only when the document names its site.
func readWaivers(root, path string, rules []auditkit.Rule, findings []auditkit.Finding, sites map[auditkit.Finding]string, today time.Time) (waiverRegister, []string, error) {
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return waiverRegister{}, nil, fmt.Errorf("%s: %w", path, err)
	}
	var register waiverRegister
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&register); err != nil {
		return waiverRegister{}, nil, fmt.Errorf("%s does not decode as a waivers register: the format is `schema`, `note` and `waivers`, and a key this loader does not know is a key nobody enforces: %w", path, err)
	}
	if register.Schema != waiverSchemaVersion {
		return waiverRegister{}, nil, fmt.Errorf("%s declares schema %d, and this gate reads %d", path, register.Schema, waiverSchemaVersion)
	}
	knownFiles := map[string]bool{}
	for _, finding := range findings {
		knownFiles[finding.Path] = true
	}
	declared := map[string]bool{} // (site, rule) pairs
	seen := map[string]bool{}
	for index, entry := range register.Waivers {
		name := entry.ID
		if name == "" {
			name = fmt.Sprintf("waiver[%d]", index)
		}
		if trouble := judgeWaiver(entry, rules, knownFiles, today); trouble != "" {
			return waiverRegister{}, nil, fmt.Errorf("%s: %s: %s", path, name, trouble)
		}
		if seen[entry.ID] {
			return waiverRegister{}, nil, fmt.Errorf("%s: %s: o id nomeia duas exceções, e um id que nomeia duas não nomeia nenhuma", path, name)
		}
		seen[entry.ID] = true
		declared[pair(entry.Site, entry.Rule)] = true
	}
	accepted := []string{}
	matched := map[string]bool{}
	for _, finding := range findings {
		site := sites[finding]
		if !declared[pair(site, finding.Rule)] {
			continue
		}
		matched[pair(site, finding.Rule)] = true
		accepted = append(accepted, fmt.Sprintf("%s (%s)", site, finding.Rule))
	}
	// The ratchet: an exception whose site stopped carrying the finding is
	// refused, which is how fixing a site forces the register to shrink instead of
	// leaving behind a permission nobody uses.
	for _, entry := range register.Waivers {
		if !matched[pair(entry.Site, entry.Rule)] {
			return waiverRegister{}, nil, fmt.Errorf("%s: %s: a árvore não produz mais o achado `%s` em `%s`: uma exceção que ninguém usa é uma permissão que sobrou, e ela sai do registro quando o lugar é corrigido", path, entry.ID, entry.Rule, entry.Site)
		}
	}
	return register, accepted, nil
}

// judgeWaiver answers what is wrong with one entry, or the empty string when
// nothing is.
func judgeWaiver(entry waiver, rules []auditkit.Rule, knownFiles map[string]bool, today time.Time) string {
	for _, field := range []struct{ name, value string }{
		{"id", entry.ID}, {"rule", entry.Rule}, {"site", entry.Site}, {"risk", entry.Risk},
		{"owner", entry.Owner}, {"justification", entry.Justification},
		{"compensation", entry.Compensation}, {"created", entry.Created}, {"expires", entry.Expires},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Sprintf("`%s` está em branco: uma exceção que não diz %s é uma exceção que ninguém pode julgar", field.name, field.name)
		}
	}
	if !waiverIDPattern.MatchString(entry.ID) {
		return fmt.Sprintf("`%s` não é um identificador de exceção: a convenção é `TST-< três dígitos >`", entry.ID)
	}
	if !auditkit.RuleKnown(rules, entry.Rule) {
		return fmt.Sprintf("`%s` não é uma regra deste portão: uma exceção de uma regra que ninguém enforça não suspende nada", entry.Rule)
	}
	if !knownFiles[strings.SplitN(entry.Site, "::", 2)[0]] {
		return fmt.Sprintf("`%s` não é um arquivo de teste desta árvore: a exceção tem de nomear o lugar que ela cobre", entry.Site)
	}
	if !signalExists(entry.Site) {
		return fmt.Sprintf("`%s` não resolve para um teste declarado: o lugar nomeado é o lugar coberto", entry.Site)
	}
	if entry.Risk == forbiddenRisk {
		return fmt.Sprintf("`%s` é a classe crítica: a fase exige uma exceção **não crítica**, e a classe crítica é a das regras que este repositório não troca por compensação nenhuma", entry.Risk)
	}
	known := false
	for _, risk := range waiverRisks {
		if risk == entry.Risk {
			known = true
		}
	}
	if !known {
		return fmt.Sprintf("`%s` não é uma classe de risco: o vocabulário é %s", entry.Risk, strings.Join(waiverRisks, ", "))
	}
	if addressPattern.MatchString(entry.Owner) {
		return "`owner` é um endereço pessoal: uma exceção é de um papel ou de um time, e o registro é lido por todos"
	}
	if len(strings.TrimSpace(entry.Justification)) < shortestJustification {
		return fmt.Sprintf("`justification` é menor que %d caracteres: uma exceção cuja razão não cabe numa frase é uma exceção que ninguém reconsiderou", shortestJustification)
	}
	if !signalExists(entry.Compensation) {
		return fmt.Sprintf("`%s` não resolve para um teste que existe: uma compensação que ninguém pode rodar é uma frase, não uma compensação", entry.Compensation)
	}
	created, createdOK := parseDate(entry.Created)
	expires, expiresOK := parseDate(entry.Expires)
	if !createdOK {
		return fmt.Sprintf("`%s` não é uma data: o formato é %s", entry.Created, dateLayout)
	}
	if !expiresOK {
		return fmt.Sprintf("`%s` não é uma data: o formato é %s", entry.Expires, dateLayout)
	}
	if expires.Before(created) {
		return fmt.Sprintf("`%s` expira antes de ter sido criada em `%s`: uma janela que corre para trás nunca expira", entry.Expires, entry.Created)
	}
	if expires.Before(today) {
		return fmt.Sprintf("`%s` expirou em `%s` e hoje é `%s`: uma exceção expirada é um achado que ninguém aceitou", entry.ID, entry.Expires, today.Format(dateLayout))
	}
	return ""
}

// pair keys the exception by the place and the rule it suspends, so that one
// site can carry two exceptions when two rules are about it, and an exception
// never suspends a rule other than the one it names.
func pair(site, rule string) string { return site + "\x00" + rule }

func parseDate(value string) (time.Time, bool) {
	date, err := time.Parse(dateLayout, value)
	if err != nil {
		return time.Time{}, false
	}
	return date, true
}

// signalExists answers whether a `path::Function` reference names a function the
// file declares. The reference is the whole point of the field: whoever reviews
// the exception can run it.
func signalExists(reference string) bool {
	path, name, hasFunction := strings.Cut(reference, "::")
	if path == "" || !strings.HasSuffix(path, ".go") {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	if !hasFunction {
		// A finding about the file itself — a crowd of doubles, an unread fixture
		// — names the file, and the file is what has to be there.
		return true
	}
	if name == "" {
		return false
	}
	declared := regexp.MustCompile(`(?m)^func ` + regexp.QuoteMeta(name) + `\(`)
	return declared.Match(raw)
}
