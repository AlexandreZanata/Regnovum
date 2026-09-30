// The rule catalogue of the privacy and moderation audit (P20-T06).
//
// One function per rule, each returning the violations it found instead of
// stopping at the first: an audit that reports one problem per run makes the
// next problem as expensive as the first. Every rule is named, and the tests
// hold one mutation for each name — a rule without a mutation is a rule that
// might never fire.
//
// The rules fall into three kinds. Some judge the document against itself (a
// date that parses, a sorted list, a finding with an owner). Some judge it
// against the code, which is where the phase's "validated against allowlist"
// becomes mechanical: the key set of each export, the retention table, the
// reidentification threshold and the analytics vocabulary are read from the
// packages that enforce them and compared in both directions. The third kind
// resolves what the document cites — every evidence path, and the executions it
// names — against the tree, so a claim is either a file that exists or it is a
// violation.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/observability"
	profilesdomain "github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
	transparencydomain "github.com/AlexandreZanata/Regnovum/internal/transparency/domain"
)

// emailShape matches anything that looks like an address, in the prose and in
// the block alike. The report of a privacy review that carries a real address
// is a report that leaked one, so the scan covers the whole document and not
// only its machine-readable half.
var emailShape = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)

// reservedDomains are the domains reserved for documentation and tests
// (RFC 2606 and RFC 6761). An address outside them is refused wherever it
// appears in the register.
var reservedDomains = []string{
	"example.com", "example.org", "example.net", "example.edu",
	"example", "invalid", "test", "localhost",
}

// reservedDomain reports whether a domain may hold a synthetic account.
func reservedDomain(domain string) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if contains(reservedDomains, domain) {
		return true
	}
	for _, reserved := range reservedDomains {
		if strings.HasSuffix(domain, "."+reserved) {
			return true
		}
	}
	return false
}

// Options is what the audit is asked to do.
type Options struct {
	// Root is the repository root the paths are resolved against.
	Root string
	// Document is the register to judge, relative to Root or absolute. Empty
	// means the delivered register. It exists so a test can judge a mutated
	// register against the real tree, which is the only tree that can answer
	// for the paths the document cites.
	Document string
	// Run tells the audit to execute each area's command. Without it the
	// register is judged but nothing runs: that is what the tests use, and
	// what `-check` exposes.
	Run bool
	// Timeout bounds one area's execution.
	Timeout time.Duration
}

// Report is what the audit read, what it ran and what it refused.
type Report struct {
	// Document is the register that was audited.
	Document Document
	// Keys is how many JSON keys the allowlists cover, which is the size of
	// the surface the phase asked to validate.
	Keys int
	// Executions is one entry per area that was run, in register order.
	Executions []Execution
	// Violations is every rule violation, in the order the rules ran.
	Violations []Violation
}

// Counts returns how many areas carry each verdict, which is what the reader of
// the gate output wants before the details.
func (r Report) Counts() map[string]int {
	counts := map[string]int{}
	for _, area := range r.Document.Register.Areas {
		counts[area.Verdict]++
	}
	return counts
}

// Audit reads the register, applies every rule and — when asked — runs the
// seven executions. It never writes and never fixes.
func Audit(options Options) (Report, error) {
	path := options.Document
	if strings.TrimSpace(path) == "" {
		path = filepath.Join(options.Root, documentPath)
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(options.Root, path)
	}
	document, err := readDocument(path)
	if err != nil {
		return Report{}, err
	}
	report := Report{Document: document}

	report.Violations = append(report.Violations, ruleTopLevel(document)...)
	report.Violations = append(report.Violations, ruleAccounts(document)...)
	report.Violations = append(report.Violations, ruleMixing(options.Root, document)...)

	keys, allowlistViolations := ruleAllowlists(options.Root, document)
	report.Keys = keys
	report.Violations = append(report.Violations, allowlistViolations...)

	report.Violations = append(report.Violations, ruleLowCount(document)...)
	report.Violations = append(report.Violations, ruleRetention(document)...)
	report.Violations = append(report.Violations, ruleAnalytics(document)...)
	report.Violations = append(report.Violations, ruleAreas(options.Root, document)...)
	report.Violations = append(report.Violations, ruleFindings(options.Root, document)...)
	report.Violations = append(report.Violations, rulePII(document)...)

	// Running commands on a register that is already refused would spend
	// minutes to reach a conclusion the structure already gave: the register
	// is not a register until it is well formed.
	if options.Run && len(report.Violations) == 0 {
		executions, runViolations := runAreas(options.Root, document.Register.Areas, options.Timeout)
		report.Executions = executions
		report.Violations = append(report.Violations, runViolations...)
	}
	return report, nil
}

// --- the document against itself ------------------------------------------

// ruleTopLevel judges the header: the block has a schema this tool knows, and a
// date a reader can place the review in.
func ruleTopLevel(document Document) []Violation {
	var violations []Violation
	if document.Register.Version != 1 {
		violations = append(violations, Violation{
			Rule: "schema-version",
			Detail: fmt.Sprintf("the register declares version %d and this tool judges version 1",
				document.Register.Version),
		})
	}
	if _, err := time.Parse(dateFormat, document.Register.AuditedOn); err != nil {
		violations = append(violations, Violation{
			Rule:   "audited-on",
			Detail: fmt.Sprintf("audited_on is %q and the register only accepts YYYY-MM-DD", document.Register.AuditedOn),
		})
	}
	if strings.TrimSpace(document.Prose) == "" {
		violations = append(violations, Violation{
			Rule:   "prose",
			Detail: "the document is the block alone: the reader has no review to read",
		})
	}
	return violations
}

// ruleAccounts judges the synthetic subjects: exactly two, distinct, and in
// domains reserved for the purpose. The phase asks for two accounts looking for
// data mixing, so one account is not a review of mixing and a real address is
// not synthetic.
func ruleAccounts(document Document) []Violation {
	var violations []Violation
	accounts := document.Register.Accounts
	if len(accounts) != 2 {
		violations = append(violations, Violation{
			Rule:   "accounts-count",
			Detail: fmt.Sprintf("the register names %d synthetic account(s) and the review is run with two", len(accounts)),
		})
	}
	ids := map[string]bool{}
	emails := map[string]bool{}
	for _, account := range accounts {
		if strings.TrimSpace(account.ID) == "" {
			violations = append(violations, Violation{Rule: "accounts-id", Detail: "an account has no role"})
		}
		if ids[account.ID] {
			violations = append(violations, Violation{
				Rule: "accounts-id", Detail: fmt.Sprintf("two accounts share the role %q", account.ID),
			})
		}
		ids[account.ID] = true

		at := strings.LastIndex(account.Email, "@")
		if at < 0 {
			violations = append(violations, Violation{
				Rule:   "accounts-synthetic",
				Detail: fmt.Sprintf("the account %q has no address", account.ID),
			})
			continue
		}
		domain := account.Email[at+1:]
		if !reservedDomain(domain) {
			violations = append(violations, Violation{
				Rule: "accounts-synthetic",
				Detail: fmt.Sprintf("the account %q uses the domain %q, which is not reserved for documentation and tests",
					account.ID, domain),
			})
		}
		if emails[account.Email] {
			violations = append(violations, Violation{
				Rule:   "accounts-distinct",
				Detail: fmt.Sprintf("two accounts share the address %q, so nothing is being compared", account.Email),
			})
		}
		emails[account.Email] = true
	}
	return violations
}

// ruleMixing resolves the mixing evidence: the file has to exist and has to
// name both accounts. "Two accounts were used" is a claim; a test that holds
// both addresses and asserts one does not appear in the other is evidence.
func ruleMixing(root string, document Document) []Violation {
	mixing := document.Register.Mixing
	if strings.TrimSpace(mixing.Path) == "" {
		return []Violation{{
			Rule:   "mixing-path",
			Detail: "the register names no execution that looks for data mixing between the two accounts",
		}}
	}
	raw, err := os.ReadFile(filepath.Join(root, mixing.Path))
	if err != nil {
		return []Violation{{
			Path: mixing.Path, Rule: "mixing-path",
			Detail: fmt.Sprintf("the mixing evidence does not exist: %v", err),
		}}
	}
	var violations []Violation
	for _, account := range document.Register.Accounts {
		if !strings.Contains(string(raw), account.Email) {
			violations = append(violations, Violation{
				Path: mixing.Path, Rule: "mixing-evidence",
				Detail: fmt.Sprintf("%s never names %s, so the comparison between the accounts is not the one the register describes",
					mixing.Path, account.Email),
			})
		}
	}
	return violations
}

// rulePII refuses an address outside the reserved domains anywhere in the
// document. It is the phase's own "o relatório não contém PII real", made
// mechanical: the review talks about data, and the one thing it must never
// carry is somebody's.
func rulePII(document Document) []Violation {
	var violations []Violation
	seen := map[string]bool{}
	for _, match := range emailShape.FindAllString(document.Text, -1) {
		if seen[match] {
			continue
		}
		seen[match] = true
		at := strings.LastIndex(match, "@")
		domain := match[at+1:]
		if !reservedDomain(domain) {
			violations = append(violations, Violation{
				Rule:   "register-pii",
				Detail: fmt.Sprintf("the document carries the address %q, whose domain is not reserved: a privacy report holds no real address", match),
			})
		}
	}
	return violations
}

// --- the document against the code ----------------------------------------

// ruleAllowlists compares each declared allowlist with the keys the file it
// names can emit, and returns how many keys the declarations cover.
func ruleAllowlists(root string, document Document) (int, []Violation) {
	var violations []Violation
	ids := map[string]bool{}
	total := 0
	for _, entry := range document.Register.Allowlists {
		if strings.TrimSpace(entry.ID) == "" {
			violations = append(violations, Violation{Rule: "allowlist-id", Detail: "an allowlist has no name"})
			continue
		}
		if ids[entry.ID] {
			violations = append(violations, Violation{
				Rule: "allowlist-id", Subject: entry.ID, Detail: "two allowlists share a name",
			})
		}
		ids[entry.ID] = true

		if !contains(surfaces, entry.Surface) {
			violations = append(violations, Violation{
				Path: entry.Path, Rule: "allowlist-surface", Subject: entry.ID,
				Detail: fmt.Sprintf("the surface %q is not one of %s", entry.Surface, strings.Join(surfaces, ", ")),
			})
			continue
		}
		keys, err := scanJSONKeys(filepath.Join(root, entry.Path))
		if err != nil {
			violations = append(violations, Violation{
				Path: entry.Path, Rule: "allowlist-path", Subject: entry.ID,
				Detail: fmt.Sprintf("the declared file cannot be read: %v", err),
			})
			continue
		}
		total += len(keys)
		violations = append(violations, allowlistProblems(root, entry, keys)...)
	}
	if len(document.Register.Allowlists) == 0 {
		violations = append(violations, Violation{
			Rule: "allowlist-empty", Detail: "the register declares no allowlist at all",
		})
	}
	return total, violations
}

// ruleLowCount compares the declared reidentification threshold with the one
// the aggregates apply, and exercises it: a threshold is a number, and the
// behaviour of the number is what keeps a small population from being published.
func ruleLowCount(document Document) []Violation {
	declared := document.Register.LowCount.Threshold
	actual := int(transparencydomain.LowCountThreshold)
	var violations []Violation
	if declared != actual {
		violations = append(violations, Violation{
			Rule:   "low-count-threshold",
			Detail: fmt.Sprintf("the register publishes %d and the aggregates suppress below %d", declared, actual),
		})
	}
	if declared < 2 {
		violations = append(violations, Violation{
			Rule:   "low-count-floor",
			Detail: fmt.Sprintf("a threshold of %d does not suppress anything", declared),
		})
	}
	if got := transparencydomain.Suppress(transparencydomain.LowCountThreshold - 1); got != 0 {
		violations = append(violations, Violation{
			Rule:   "low-count-behaviour",
			Detail: fmt.Sprintf("a population below the threshold published %d", got),
		})
	}
	if got := transparencydomain.Suppress(transparencydomain.LowCountThreshold); got != transparencydomain.LowCountThreshold {
		violations = append(violations, Violation{
			Rule:   "low-count-behaviour",
			Detail: fmt.Sprintf("the threshold population itself published %d", got),
		})
	}
	if got := transparencydomain.Suppress(0); got != 0 {
		violations = append(violations, Violation{
			Rule: "low-count-behaviour", Detail: fmt.Sprintf("an empty population published %d", got),
		})
	}
	return violations
}

// ruleRetention compares the published retention table with the policy the job
// enforces, row by row and in both directions: a class the code governs and the
// document omits is a window nobody ratified, and a row the code does not have
// is a promise nobody keeps.
func ruleRetention(document Document) []Violation {
	declared := map[string]Retention{}
	var violations []Violation
	for _, row := range document.Register.Retention {
		if _, present := declared[row.Class]; present {
			violations = append(violations, Violation{
				Rule: "retention-duplicate", Subject: row.Class, Detail: "the class appears twice in the table",
			})
		}
		declared[row.Class] = row
	}

	actual := map[string]profilesdomain.RetentionSchedule{}
	for _, schedule := range profilesdomain.RetentionSchedules() {
		class := string(schedule.Class)
		actual[class] = schedule
		row, present := declared[class]
		if !present {
			violations = append(violations, Violation{
				Rule: "retention-missing", Subject: class,
				Detail: "the policy governs this class and the register does not publish it",
			})
			continue
		}
		if row.Action != string(schedule.Action) {
			violations = append(violations, Violation{
				Rule: "retention-action", Subject: class,
				Detail: fmt.Sprintf("the register publishes %q and the policy does %q", row.Action, schedule.Action),
			})
		}
		if row.Indefinite != schedule.Indefinite {
			violations = append(violations, Violation{
				Rule: "retention-indefinite", Subject: class,
				Detail: fmt.Sprintf("the register says indefinite=%t and the policy says %t", row.Indefinite, schedule.Indefinite),
			})
		}
		if hours := int(schedule.Window.Hours()); row.WindowHours != hours {
			violations = append(violations, Violation{
				Rule: "retention-window", Subject: class,
				Detail: fmt.Sprintf("the register publishes %dh and the policy enforces %dh", row.WindowHours, hours),
			})
		}
		if row.ReasonCode != schedule.ReasonCode {
			violations = append(violations, Violation{
				Rule: "retention-reason", Subject: class,
				Detail: fmt.Sprintf("the register publishes %q and the policy records %q", row.ReasonCode, schedule.ReasonCode),
			})
		}
	}
	for class := range declared {
		if _, present := actual[class]; !present {
			violations = append(violations, Violation{
				Rule: "retention-unknown", Subject: class,
				Detail: "the register governs a class that no policy enforces",
			})
		}
	}
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].Subject != violations[j].Subject {
			return violations[i].Subject < violations[j].Subject
		}
		return violations[i].Rule < violations[j].Rule
	})
	return violations
}

// ruleAnalytics compares the published event vocabulary with the allowlist the
// dispatcher enforces, and refuses a property whose name is shaped like
// something the analytics payload may not carry. The comparison is exact in
// both directions: an event the code can send and the document does not publish
// is telemetry nobody reviewed, and a documented event that does not exist is a
// review of something else.
func ruleAnalytics(document Document) []Violation {
	var violations []Violation
	if len(document.Register.Analytics) == 0 {
		violations = append(violations, Violation{
			Rule: "analytics-empty", Detail: "the register publishes no analytics event at all",
		})
	}

	declared := map[string][]string{}
	for _, event := range document.Register.Analytics {
		if _, present := declared[event.Event]; present {
			violations = append(violations, Violation{
				Rule: "analytics-duplicate", Subject: event.Event, Detail: "the event appears twice",
			})
		}
		if !sorted(event.Properties) {
			violations = append(violations, Violation{
				Rule: "analytics-order", Subject: event.Event,
				Detail: "the properties are not sorted and unique",
			})
		}
		for _, property := range event.Properties {
			if token := forbiddenShape(surfaceSubject, property); token != "" {
				violations = append(violations, Violation{
					Rule: "analytics-forbidden", Subject: event.Event,
					Detail: fmt.Sprintf("the event admits the property %q, whose name carries %q", property, token),
				})
			}
		}
		declared[event.Event] = event.Properties
	}

	for _, name := range observability.AllowlistedEvents() {
		properties, present := declared[name]
		if !present {
			violations = append(violations, Violation{
				Rule: "analytics-missing", Subject: name,
				Detail: "the dispatcher may send this event and the register does not publish it",
			})
			continue
		}
		if !sameSet(properties, observability.AllowedProperties(name)) {
			violations = append(violations, Violation{
				Rule: "analytics-properties", Subject: name,
				Detail: fmt.Sprintf("the register publishes %v and the allowlist admits %v",
					properties, observability.AllowedProperties(name)),
			})
		}
	}
	for name := range declared {
		if !observability.IsAllowlisted(name) {
			violations = append(violations, Violation{
				Rule: "analytics-unknown", Subject: name,
				Detail: "the register publishes an event the dispatcher refuses",
			})
		}
	}
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].Subject != violations[j].Subject {
			return violations[i].Subject < violations[j].Subject
		}
		return violations[i].Rule < violations[j].Rule
	})
	return violations
}

// --- what the document cites ---------------------------------------------

// ruleAreas checks that the seven areas of the phase are all present, in the
// order the document presents them, that each one carries a verdict from the
// vocabulary, that every path it cites exists, and that an area declaring a gap
// names a finding that carries the residual.
func ruleAreas(root string, document Document) []Violation {
	var violations []Violation
	areas := document.Register.Areas

	present := make([]string, 0, len(areas))
	for _, area := range areas {
		present = append(present, area.Key)
	}
	if !sameSet(present, requiredAreas) {
		violations = append(violations, Violation{
			Rule:   "area-set",
			Detail: fmt.Sprintf("the register audits %v and the phase names %v", present, requiredAreas),
		})
	}
	if len(present) == len(requiredAreas) {
		for i, key := range requiredAreas {
			if present[i] != key {
				violations = append(violations, Violation{
					Rule:   "area-order",
					Detail: fmt.Sprintf("the areas are published in a different order than the phase names them: %v", present),
				})
				break
			}
		}
	}

	findings := map[string]bool{}
	for _, finding := range document.Register.Findings {
		findings[finding.ID] = true
	}

	for _, area := range areas {
		if !contains(verdicts, area.Verdict) {
			violations = append(violations, Violation{
				Rule: "area-verdict", Subject: area.Key,
				Detail: fmt.Sprintf("the verdict %q is not one of %s", area.Verdict, strings.Join(verdicts, ", ")),
			})
		}
		if strings.TrimSpace(area.Note) == "" {
			violations = append(violations, Violation{
				Rule: "area-note", Subject: area.Key, Detail: "the area states nothing it confirmed",
			})
		}
		if strings.TrimSpace(area.Execution) == "" {
			violations = append(violations, Violation{
				Rule: "area-execution", Subject: area.Key,
				Detail: "the area names no command, so nothing was run for it",
			})
		}
		if len(area.Evidence) == 0 {
			violations = append(violations, Violation{
				Rule: "area-evidence", Subject: area.Key, Detail: "the area cites no path",
			})
		}
		for _, path := range area.Evidence {
			if _, err := os.Stat(filepath.Join(root, path)); err != nil {
				violations = append(violations, Violation{
					Path: path, Rule: "area-evidence", Subject: area.Key,
					Detail: "the cited path does not exist",
				})
			}
		}
		if area.Verdict == verdictGap {
			if strings.TrimSpace(area.Finding) == "" {
				violations = append(violations, Violation{
					Rule: "area-gap", Subject: area.Key,
					Detail: "the area declares a gap and names no finding to carry the residual",
				})
			} else if !findings[area.Finding] {
				violations = append(violations, Violation{
					Rule: "area-gap", Subject: area.Key,
					Detail: fmt.Sprintf("the area points at the finding %q, which the register does not declare", area.Finding),
				})
			}
		} else if strings.TrimSpace(area.Finding) != "" {
			violations = append(violations, Violation{
				Rule: "area-finding", Subject: area.Key,
				Detail: fmt.Sprintf("the area is %s and points at the finding %q", area.Verdict, area.Finding),
			})
		}
	}
	return violations
}

// ruleFindings judges the defects: the vocabulary, the minimum validation of the
// phase (no Crítica or Alta left open), the acceptance discipline (a residual
// someone accepted has an owner and a date) and the evidence each finding cites.
func ruleFindings(root string, document Document) []Violation {
	var violations []Violation
	seen := map[string]bool{}
	for _, finding := range document.Register.Findings {
		refuse := func(rule, detail string) {
			violations = append(violations, Violation{Rule: rule, Subject: finding.ID, Detail: detail})
		}
		if strings.TrimSpace(finding.ID) == "" {
			violations = append(violations, Violation{Rule: "finding-id", Detail: "a finding has no identifier"})
			continue
		}
		if seen[finding.ID] {
			refuse("finding-id", "two findings share an identifier")
		}
		seen[finding.ID] = true

		if !contains(severities, finding.Severity) {
			refuse("finding-severity", fmt.Sprintf("the severity %q is not one of %s", finding.Severity, strings.Join(severities, ", ")))
		}
		if !contains(statuses, finding.Status) {
			refuse("finding-status", fmt.Sprintf("the status %q is not one of %s", finding.Status, strings.Join(statuses, ", ")))
		}
		if strings.TrimSpace(finding.Title) == "" {
			refuse("finding-title", "the finding is not stated")
		}
		if strings.TrimSpace(finding.Plan) == "" {
			refuse("finding-plan", "the finding names no work that closes it")
		}
		if !contains(requiredAreas, finding.Area) {
			refuse("finding-area", fmt.Sprintf("the finding belongs to %q, which is not an area of the phase", finding.Area))
		}
		if len(finding.Evidence) == 0 {
			refuse("finding-evidence", "the finding cites nothing")
		}
		for _, path := range finding.Evidence {
			if _, err := os.Stat(filepath.Join(root, path)); err != nil {
				violations = append(violations, Violation{
					Path: path, Rule: "finding-evidence", Subject: finding.ID,
					Detail: "the cited path does not exist",
				})
			}
		}
		if finding.Status == statusOpen && (finding.Severity == severityCritical || finding.Severity == severityHigh) {
			refuse("finding-open", "the phase's minimum validation refuses a Crítica or Alta finding left open")
		}
		if finding.Status == statusAccepted {
			if strings.TrimSpace(finding.Owner) == "" {
				refuse("finding-owner", "an accepted residual has no owner")
			}
			if _, err := time.Parse(dateFormat, finding.AcceptedOn); err != nil {
				refuse("finding-accepted-on", fmt.Sprintf("the acceptance date %q is not YYYY-MM-DD", finding.AcceptedOn))
			}
		}
		if finding.Status == statusOpen {
			if strings.TrimSpace(finding.Owner) != "" || strings.TrimSpace(finding.AcceptedOn) != "" {
				refuse("finding-acceptance", "an open finding carries an owner or an acceptance date, which is an acceptance in disguise")
			}
		}
	}
	return violations
}
