// Measurement of the internationalization surface (P20-T09).
//
// The audit answers one question with two halves: what the tree measures, and
// what the register claims about it. This file is the first half. Every number
// it produces is recomputed from the tree at check time — never read from the
// document — so editing the register cannot make the audit agree with a tree
// that changed underneath it.
//
// What it deliberately does not do: run the gates. Executing them is the
// harness's job (`verify.sh`), which records each execution and refuses a red
// one; a measurement that also ran the commands would be unable to tell "the
// gate passed" from "the gate was never asked".
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	notificationsdomain "github.com/AlexandreZanata/Regnovum/internal/notifications/domain"
)

// Facts is one measurement of the tree.
type Facts struct {
	// Commit is the revision the measurement was taken at.
	Commit string `json:"commit"`
	// Measured is the instant of the measurement, in UTC.
	Measured string `json:"measured"`

	Catalog  catalogFacts `json:"catalog"`
	Emails   emailFacts   `json:"emails"`
	Codes    codesFacts   `json:"codes"`
	Plural   pluralFacts  `json:"plural"`
	Cache    cacheFacts   `json:"cache"`
	Search   searchFacts  `json:"search"`
	Pseudo   pseudoFacts  `json:"pseudo"`
	Journeys journeyFacts `json:"journeys"`
}

// catalogFacts is the completeness of the catalogs.
type catalogFacts struct {
	// Locales are the interface locales with catalogs, sorted.
	Locales []string `json:"locales"`
	// Namespaces are the namespaces any locale declares, sorted.
	Namespaces []string `json:"namespaces"`
	// Keys is the number of messages each locale declares.
	Keys map[string]int `json:"keys_per_locale"`
	// Missing[locale] lists keys the default locale declares and that locale
	// does not.
	Missing map[string][]string `json:"missing"`
	// Extra[locale] lists keys a locale declares and the default locale does
	// not.
	Extra map[string][]string `json:"extra"`
	// PlaceholderDivergence lists keys whose placeholder sets differ between
	// locales.
	PlaceholderDivergence []string `json:"placeholder_divergence"`
	// Empty lists keys a locale declares with a blank message.
	Empty []string `json:"empty"`
	// Orphans lists keys no delivered code can reach: neither the key itself
	// nor any prefix that composes it appears in the source.
	Orphans []string `json:"orphans"`
	// Unknown lists literals shaped like a catalog key that the catalog does
	// not declare and that is not a prefix of one.
	Unknown []string `json:"unknown_references"`
	// Referenced is the number of keys the delivered source reaches by its own
	// literal or by a prefix.
	Referenced int `json:"referenced_keys"`
	// Literals and Prefixes are what the scan found: a key spelled out at its
	// call site, and a stem a call site completes at runtime. They are reported
	// because a scan that silently found nothing would report every message as
	// an orphan.
	Literals int `json:"key_literals"`
	Prefixes int `json:"key_prefixes"`
}

// emailFacts is the committed email evidence.
type emailFacts struct {
	// Templates are the transactional templates the domain declares.
	Templates []string `json:"templates"`
	// Locales are the locales the domain declares for email, sorted.
	Locales []string `json:"locales"`
	// Expected and Present are the numbers of snapshots the templates times the
	// locales require and the tree holds.
	Expected int `json:"goldens_expected"`
	Present  int `json:"goldens_present"`
	// Missing lists the snapshots a template and a locale require and that are
	// not committed.
	Missing []string `json:"missing_goldens"`
}

// codesFacts is the stable error-code vocabulary of the Problem Details.
type codesFacts struct {
	// Codes[locale] lists the codes the locale declares, sorted.
	Codes map[string][]string `json:"codes_per_locale"`
	// Drift lists codes declared in some locales and not in all of them.
	Drift []string `json:"drift"`
}

// pluralFacts is what the catalog says about plural variants.
type pluralFacts struct {
	// Variants are the catalog keys that carry a CLDR plural category.
	Variants int      `json:"variants"`
	Keys     []string `json:"keys"`
}

// cacheFacts is the locale dimension of the response cache.
type cacheFacts struct {
	// PublicSurfaces is the number of delivered call sites that mark a response
	// publicly cacheable.
	PublicSurfaces int `json:"public_cacheable_surfaces"`
	// RawAcceptLanguage lists places where a response is varied by the raw
	// `Accept-Language` header, which §4 forbids.
	RawAcceptLanguage []string `json:"vary_by_raw_accept_language"`
}

// searchFacts is what the delivered source says about search engines and about
// the browser's own formatting.
type searchFacts struct {
	// Hreflang is the number of `hreflang` occurrences in delivered documents.
	// §7 allows an alternate only for pages that really are translated into the
	// language it names, so the number is reported instead of assumed.
	Hreflang []string `json:"hreflang"`
	// ToLocaleString lists the places that format a value with the browser's
	// own locale instead of an explicit one, which §6 forbids.
	ToLocaleString []string `json:"to_locale_string"`
}

// pseudoFacts is the mechanism of the pseudo-locale gate.
type pseudoFacts struct {
	// Enabled is the file that declares the derived catalog behind the build
	// tag, and Disabled the file that proves the tag is what registers it.
	Enabled  string `json:"enabled_file"`
	Disabled string `json:"disabled_file"`
	// InProductionCatalog reports whether the pseudo-locale leaked into the
	// delivered catalog, which would make it negotiable in production.
	InProductionCatalog bool `json:"in_production_catalog"`
}

// journeyFacts is the language dimension of the browser journeys.
type journeyFacts struct {
	// Locales are the interface locales the journeys iterate over, read from
	// the module that declares them.
	Locales []string `json:"locales"`
	// Specs are the journey files of the suite, sorted.
	Specs []string `json:"specs"`
	// ContentLanguageHarness and ContentLanguageSeed are the content language
	// the harness exports and the one the seed declares: two copies of one fact
	// that the audit refuses to let drift.
	ContentLanguageHarness string `json:"content_language_harness"`
	ContentLanguageSeed    string `json:"content_language_seed"`
	// Gate is the target that runs the journeys.
	Gate string `json:"gate"`
}

// Area is one property of the phase, the command that executes it and what the
// execution produced. The command comes from the audit's own catalogue; the
// execution is recorded by the harness.
type Area struct {
	// ID names the area, as the phase names it.
	ID string `json:"id"`
	// Command is the command that executes it.
	Command string `json:"command"`
	// Evidence is the file the execution left behind, or the mechanism it
	// proves.
	Evidence string `json:"evidence"`
	// Status is the outcome of the execution: `pass`, or `finding`.
	Status string `json:"status"`
	// Seconds is how long the execution took.
	Seconds float64 `json:"seconds"`
}

// Outcomes an area may record.
const (
	// StatusPass is an execution that answered zero.
	StatusPass = "pass"
	// StatusFinding is an execution whose answer is a finding in the register.
	StatusFinding = "finding"
)

// Measure reads the tree and returns everything the register is judged against.
func Measure(root string) (Facts, error) {
	catalog, err := loadCatalog(root)
	if err != nil {
		return Facts{}, err
	}
	references, err := scanDeliveredSource(root, catalog)
	if err != nil {
		return Facts{}, err
	}

	facts := Facts{
		Commit:   commitOf(root),
		Measured: time.Now().UTC().Format(time.RFC3339),
	}
	facts.Catalog = measureCatalog(catalog, references)
	facts.Emails = measureEmails(root)
	facts.Codes = measureCodes(catalog)
	facts.Plural = pluralFacts{Keys: catalog.pluralVariantKeys()}
	facts.Plural.Variants = len(facts.Plural.Keys)
	facts.Cache, facts.Search, err = measureSource(root)
	if err != nil {
		return Facts{}, err
	}
	facts.Pseudo, err = measurePseudo(root)
	if err != nil {
		return Facts{}, err
	}
	facts.Journeys, err = measureJourneys(root, catalog)
	if err != nil {
		return Facts{}, err
	}
	return facts, nil
}

// measureCatalog is everything a locales tree and one scan of the delivered
// source can say: completeness, reachability and the literals that look like a
// key without being one. It is separate from Measure so a falsification test
// can measure a fixture tree without also owing it a pseudo-locale mechanism
// and a browser suite.
func measureCatalog(catalog *catalog, references sourceReferences) catalogFacts {
	facts := catalogFacts{
		Locales:    catalog.locales,
		Namespaces: catalog.namespaceNames(),
		Keys:       map[string]int{},
		Missing:    map[string][]string{},
		Extra:      map[string][]string{},
	}

	reference := catalog.messages[defaultLocale]
	for _, locale := range catalog.locales {
		keys := catalog.keys(locale)
		facts.Keys[locale] = len(keys)
		if locale == defaultLocale {
			continue
		}
		messages := catalog.messages[locale]
		for key := range reference {
			if _, ok := messages[key]; !ok {
				facts.Missing[locale] = append(facts.Missing[locale], key)
			}
		}
		for key := range messages {
			if _, ok := reference[key]; !ok {
				facts.Extra[locale] = append(facts.Extra[locale], key)
			}
		}
		sort.Strings(facts.Missing[locale])
		sort.Strings(facts.Extra[locale])
	}

	for _, key := range catalog.allKeys() {
		want := catalog.placeholders[key]
		diverges := false
		for _, locale := range catalog.locales {
			message, ok := catalog.messages[locale][key]
			if !ok {
				continue
			}
			if strings.TrimSpace(message) == "" {
				facts.Empty = append(facts.Empty, locale+" "+key)
			}
			if !equalStrings(want, placeholderNames(message)) {
				diverges = true
			}
		}
		if diverges {
			facts.PlaceholderDivergence = append(facts.PlaceholderDivergence, key)
		}
	}
	sort.Strings(facts.Empty)

	facts.Literals = len(references.literals)
	facts.Prefixes = len(references.prefixes)
	for _, key := range catalog.allKeys() {
		if reached(key, references) {
			facts.Referenced++
			continue
		}
		facts.Orphans = append(facts.Orphans, key)
	}
	sort.Strings(facts.Orphans)

	declared := map[string]bool{}
	for _, key := range catalog.allKeys() {
		declared[key] = true
	}
	for literal, path := range references.literals {
		if declared[literal] || isPrefixOfDeclared(literal, declared) || namesAFile(literal) {
			continue
		}
		facts.Unknown = append(facts.Unknown, path+": "+literal)
	}
	sort.Strings(facts.Unknown)
	return facts
}

// reached reports whether delivered source reaches a key, either by the key's
// own literal or by a prefix literal that composes it at the call site.
func reached(key string, references sourceReferences) bool {
	if _, ok := references.literals[key]; ok {
		return true
	}
	parts := strings.Split(key, ".")
	for index := 1; index < len(parts); index++ {
		if _, ok := references.prefixes[strings.Join(parts[:index], ".")+"."]; ok {
			return true
		}
	}
	return false
}

// isPrefixOfDeclared reports whether a literal is an object of the catalog
// rather than a message: `arenas.document` is a prefix of messages and not one
// itself, and calling it unknown would be a false positive.
func isPrefixOfDeclared(literal string, declared map[string]bool) bool {
	for key := range declared {
		if strings.HasPrefix(key, literal+".") {
			return true
		}
	}
	return false
}

// measureEmails compares the committed email snapshots with the templates and
// locales the product declares.
func measureEmails(root string) emailFacts {
	templates := notificationsdomain.TemplateIDs()
	locales := notificationsdomain.Locales()
	facts := emailFacts{
		Locales:   []string{},
		Templates: []string{},
	}
	for _, template := range templates {
		facts.Templates = append(facts.Templates, template.String())
	}
	for _, locale := range locales {
		facts.Locales = append(facts.Locales, locale.String())
	}
	sort.Strings(facts.Templates)
	sort.Strings(facts.Locales)

	directory := filepath.Join(root, "internal", "notifications", "adapters", "renderer", "testdata")
	for _, template := range templates {
		for _, locale := range locales {
			facts.Expected++
			name := template.String() + "." + locale.String() + ".golden"
			if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
				facts.Missing = append(facts.Missing, name)
				continue
			}
			facts.Present++
		}
	}
	sort.Strings(facts.Missing)
	return facts
}

// measureCodes reads the stable error-code vocabulary out of the catalog: a
// Problem Details code is `errors.<code>.title`, and the set has to be the same
// in every locale, because a client depends on the code and not on the
// sentence.
func measureCodes(catalog *catalog) codesFacts {
	facts := codesFacts{Codes: map[string][]string{}}
	seen := map[string]bool{}
	for _, locale := range catalog.locales {
		codes := map[string]bool{}
		for key := range catalog.messages[locale] {
			parts := strings.Split(key, ".")
			if len(parts) == 3 && parts[0] == "errors" && parts[2] == "title" {
				codes[parts[1]] = true
			}
		}
		for code := range codes {
			facts.Codes[locale] = append(facts.Codes[locale], code)
			seen[code] = true
		}
		sort.Strings(facts.Codes[locale])
	}
	for code := range seen {
		for _, locale := range catalog.locales {
			if !contains(facts.Codes[locale], code) {
				facts.Drift = append(facts.Drift, locale+" "+code)
			}
		}
	}
	sort.Strings(facts.Drift)
	return facts
}

// measureSource reads the delivered source for the two properties a document
// cannot state about itself: how a cacheable response declares its variation,
// and whether the browser is left to decide how a value is formatted.
func measureSource(root string) (cacheFacts, searchFacts, error) {
	cache := cacheFacts{}
	search := searchFacts{}
	toLocaleString := regexp.MustCompile(`\.toLocaleString\s*\(`)

	for _, directory := range []string{"internal", "web/src", "tools"} {
		base := filepath.Join(root, directory)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if name := entry.Name(); name == "node_modules" || name == "dist" || strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !hasSourceSuffix(entry.Name()) || strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			// The audit's own directory is skipped, and its absence is declared in
			// the register: it necessarily carries the words it searches for, and
			// a scanner that counted itself would measure its own vocabulary. It
			// is not a surface that answers a client, which is what the rule is
			// about.
			if relative == filepath.Join("internal", "i18n", "generated.go") ||
				relative == filepath.Join("web", "src", "i18n", "generated.ts") ||
				strings.HasPrefix(relative, filepath.Join("tools", "i18nrelease")) {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text := string(raw)
			for number, line := range strings.Split(text, "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
					continue
				}
				if strings.Contains(trimmed, "httpcache.Public(") {
					cache.PublicSurfaces++
				}
				if strings.Contains(trimmed, "Vary") && strings.Contains(trimmed, "Accept-Language") {
					cache.RawAcceptLanguage = append(cache.RawAcceptLanguage,
						fmt.Sprintf("%s:%d: %s", relative, number+1, trimmed))
				}
				if strings.Contains(trimmed, "hreflang") {
					search.Hreflang = append(search.Hreflang,
						fmt.Sprintf("%s:%d", relative, number+1))
				}
			}
			for _, match := range toLocaleString.FindAllStringIndex(text, -1) {
				if isCommentLine(text, match[0]) {
					continue
				}
				search.ToLocaleString = append(search.ToLocaleString,
					fmt.Sprintf("%s:%d", relative, lineOf(text, match[0])))
			}
			return nil
		})
		if err != nil {
			return cache, search, err
		}
	}
	sort.Strings(cache.RawAcceptLanguage)
	sort.Strings(search.Hreflang)
	sort.Strings(search.ToLocaleString)
	return cache, search, nil
}

// measurePseudo reads the mechanism of the pseudo-locale gate: the two files
// behind the build tag, and whether the derived catalog leaked into the
// delivered one.
func measurePseudo(root string) (pseudoFacts, error) {
	facts := pseudoFacts{
		Enabled:  filepath.Join("internal", "i18n", "pseudo_enabled.go"),
		Disabled: filepath.Join("internal", "i18n", "pseudo_disabled.go"),
	}
	for _, path := range []string{facts.Enabled, facts.Disabled} {
		raw, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			return facts, fmt.Errorf("the pseudo-locale mechanism is incomplete: %w", err)
		}
		text := string(raw)
		if !strings.Contains(text, "go:build") {
			return facts, fmt.Errorf("%s carries no build constraint", path)
		}
	}
	generated, err := os.ReadFile(filepath.Join(root, "internal", "i18n", "generated.go"))
	if err != nil {
		return facts, err
	}
	facts.InProductionCatalog = strings.Contains(string(generated), "qps-Ploc")
	return facts, nil
}

// measureJourneys reads the language dimension of the browser journeys.
func measureJourneys(root string, catalog *catalog) (journeyFacts, error) {
	facts := journeyFacts{Gate: "make test-e2e"}
	support := filepath.Join(root, "tools", "e2e", "support", "locales.js")
	raw, err := os.ReadFile(support)
	if err != nil {
		return facts, fmt.Errorf("the journeys declare no locale dimension: %w", err)
	}
	if facts.Locales = journeyLocales(string(raw)); len(facts.Locales) == 0 {
		return facts, fmt.Errorf("%s declares no JOURNEY_LOCALES", support)
	}
	sort.Strings(facts.Locales)

	specs, err := filepath.Glob(filepath.Join(root, "tools", "e2e", "specs", "*.spec.js"))
	if err != nil {
		return facts, err
	}
	for _, spec := range specs {
		facts.Specs = append(facts.Specs, filepath.Base(spec))
	}
	sort.Strings(facts.Specs)

	harness, err := os.ReadFile(filepath.Join(root, "tools", "e2e", "harness.sh"))
	if err != nil {
		return facts, err
	}
	facts.ContentLanguageHarness = harnessContentLanguage(string(harness))
	seed, err := os.ReadFile(filepath.Join(root, "tools", "e2e", "seed", "main.go"))
	if err != nil {
		return facts, err
	}
	facts.ContentLanguageSeed = seedLanguage(string(seed))

	if len(facts.Locales) != len(catalog.locales) {
		return facts, nil
	}
	return facts, nil
}

// commitOf reads the revision the measurement was taken at, or reports that it
// is unknown rather than inventing one.
func commitOf(root string) string {
	raw, err := os.ReadFile(filepath.Join(root, ".git", "HEAD"))
	if err != nil {
		return "unknown"
	}
	head := strings.TrimSpace(string(raw))
	if strings.HasPrefix(head, "ref: ") {
		ref, err := os.ReadFile(filepath.Join(root, ".git", filepath.FromSlash(strings.TrimPrefix(head, "ref: "))))
		if err != nil {
			return "unknown"
		}
		head = strings.TrimSpace(string(ref))
	}
	return head
}

// isCommentLine reports whether the line an offset falls on is a comment, so a
// mention of a forbidden call in prose is not counted as a call.
func isCommentLine(text string, offset int) bool {
	start := strings.LastIndex(text[:offset], "\n") + 1
	line := text[start:offset]
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "#")
}

// namesAFile reports whether a dotted literal is the name of a file rather than
// a message key: `transparency.sql` is a path the SQL tooling is asked for, and
// calling it an unknown key would be a false positive that teaches the reader to
// ignore the rule.
func namesAFile(literal string) bool {
	index := strings.LastIndex(literal, ".")
	if index < 0 {
		return false
	}
	switch strings.ToLower(literal[index+1:]) {
	case "sql", "json", "go", "js", "ts", "html", "css", "md", "golden", "yaml", "yml", "txt", "sh", "test":
		return true
	}
	return false
}

// lineOf returns the 1-based offset's line, for a caller that needs a position.
func lineOf(text string, offset int) int {
	if offset > len(text) {
		return 0
	}
	return strings.Count(text[:offset], "\n") + 1
}

// equalStrings reports whether two sorted string slices hold the same values.
func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// contains reports whether a sorted slice holds a value.
func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
