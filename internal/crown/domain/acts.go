package domain

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// maxActTextRunes bounds free decree prose: reason, target and
// effect. Long enough to name the intervention, short enough to stay
// out of log abuse.
const maxActTextRunes = 2000

// ActKind is the closed vocabulary of royal decrees: the six species
// the constitution names. Season alterations of deadline, Genesis,
// wealth criteria, tie-break or accepted terms never become a kind:
// they are absent here on purpose, so any such string refuses with
// ErrUnknownAct instead of executing.
type ActKind string

const (
	// ActNormative names a normative decree: it states a rule and
	// the charter version that carries it.
	ActNormative ActKind = "normativo"
	// ActOffice names a decree over account, office or status.
	ActOffice ActKind = "conta-cargo-status"
	// ActProcess names a decree over a proceeding: it appears as a
	// royal exception, never as an ordinary decision.
	ActProcess ActKind = "processo"
	// ActEconomic names a decree moving existing INK between an
	// identified origin and destination. Only this kind carries
	// origin and amount.
	ActEconomic ActKind = "economico"
	// ActPardon names a royal pardon: it stops an unconsumed
	// execution while keeping the sentence visible.
	ActPardon ActKind = "perdao"
	// ActBlessing names a royal blessing: it restores an account
	// after digital death while keeping both facts visible.
	ActBlessing ActKind = "bencao"
)

// ParseActKind validates a kind token. Matching is exact: no
// trimming, no case folding, no inference.
func ParseActKind(raw string) (ActKind, error) {
	kind := ActKind(raw)
	switch kind {
	case ActNormative, ActOffice, ActProcess, ActEconomic, ActPardon, ActBlessing:
		return kind, nil
	default:
		return "", ErrUnknownAct
	}
}

// String returns the stored kind value.
func (k ActKind) String() string { return string(k) }

// Economic reports whether the kind moves value: only the economic
// decree does, so only it carries origin and amount.
func (k ActKind) Economic() bool { return k == ActEconomic }

// CharterVersion is one explicit constitutional version: v followed
// by a positive integer, ASCII only. It mirrors the charter module
// without importing it: domains stay disjoint by architecture, and
// "current" or "latest" never name a version here.
type CharterVersion string

// ParseCharterVersion validates a version token. Matching is exact:
// no trimming, no case folding, no zero-padded numbers.
func ParseCharterVersion(raw string) (CharterVersion, error) {
	if len(raw) < 2 || raw[0] != 'v' {
		return "", ErrAmbiguousCharter
	}
	number, err := strconv.Atoi(raw[1:])
	if err != nil || number < 1 || raw[1] == '0' {
		return "", ErrAmbiguousCharter
	}
	if "v"+strconv.Itoa(number) != raw {
		return "", ErrAmbiguousCharter
	}
	return CharterVersion(raw), nil
}

// String returns the stored version token.
func (v CharterVersion) String() string { return string(v) }

// RoyalAct is one explicit versioned decree before any effect: its
// identity, author, season book and reign, required competence,
// closed kind, declared reason, exact target and effect, decree date
// and vigour window, explicit charter version, optional correction
// link, and — only for the economic kind — funding origin and
// amount in minor units. It moves nothing: later tasks authorize,
// check and execute it.
type RoyalAct struct {
	ID         ActID
	Author     HolderSubject
	Season     SeasonID
	Reign      ReignVersion
	Competence Competence
	Kind       ActKind
	Reason     string
	Target     string
	Effect     string
	DecreedAt  time.Time
	Effective  time.Time
	EndsAt     time.Time
	Charter    CharterVersion
	Corrects   ActID
	Origin     string
	Amount     int64
}

// parseActText validates one prose field: exact match, bounded,
// never blank. Blank means incomplete; control characters or
// overflow mean malformed authority.
func parseActText(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrIncompleteAct
	}
	if utf8.RuneCountInString(raw) > maxActTextRunes {
		return "", ErrInvalidAuthority
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidAuthority
		}
	}
	return raw, nil
}

// checkActIdentity validates identity and authority tokens of one
// decree: every token whole, reign from 1, kind closed.
func checkActIdentity(act RoyalAct) error {
	if _, err := ParseActID(string(act.ID)); err != nil {
		return err
	}
	if _, err := ParseHolderSubject(string(act.Author)); err != nil {
		return err
	}
	if _, err := ParseSeasonID(string(act.Season)); err != nil {
		return err
	}
	if _, err := ParseReignVersion(int(act.Reign)); err != nil {
		return err
	}
	if _, err := ParseCompetence(string(act.Competence)); err != nil {
		return err
	}
	if _, err := ParseActKind(string(act.Kind)); err != nil {
		return err
	}
	return nil
}

// checkActProse validates reason, target and effect: all three
// arrive whole, or the decree is incomplete.
func checkActProse(act RoyalAct) error {
	if _, err := parseActText(act.Reason); err != nil {
		return err
	}
	if _, err := parseActText(act.Target); err != nil {
		return err
	}
	if _, err := parseActText(act.Effect); err != nil {
		return err
	}
	return nil
}

// checkActVigour validates decree date and vigour: both present, the
// optional end strictly after the start, and never backdated.
// Backdating refuses as harmful: a correction arrives as a new
// prospective act linked to the original, never as a past effect.
func checkActVigour(act RoyalAct) error {
	if act.DecreedAt.IsZero() || act.Effective.IsZero() {
		return ErrIncompleteAct
	}
	decreed, effective := act.DecreedAt.UTC(), act.Effective.UTC()
	if effective.Before(decreed) {
		return ErrRetroactiveAct
	}
	if act.EndsAt.IsZero() {
		return nil
	}
	if !act.EndsAt.UTC().After(effective) {
		return ErrInvalidAuthority
	}
	return nil
}

// checkActCharter validates the explicit charter version: one
// published vN, never blank or inferred.
func checkActCharter(act RoyalAct) error {
	if _, err := ParseCharterVersion(string(act.Charter)); err != nil {
		return err
	}
	return nil
}

// checkActEconomics validates value payload by kind: the economic
// decree names a known origin and a positive amount; every other
// kind carries neither, so no office quietly moves value.
func checkActEconomics(act RoyalAct) error {
	if !act.Kind.Economic() {
		if act.Origin != "" || act.Amount != 0 {
			return ErrIncompleteAct
		}
		return nil
	}
	if act.Origin == "" || strings.TrimSpace(act.Origin) != act.Origin {
		return ErrUnknownOrigin
	}
	if _, err := parseToken(act.Origin); err != nil {
		return ErrUnknownOrigin
	}
	if act.Amount <= 0 {
		return ErrIncompleteAct
	}
	return nil
}

// checkActCorrection validates the optional correction link: when
// present it names a whole act different from the decree itself.
// The link never rewrites history: the original stays, the new act
// takes effect prospectively.
func checkActCorrection(act RoyalAct) error {
	if act.Corrects == "" {
		return nil
	}
	corrects, err := ParseActID(string(act.Corrects))
	if err != nil {
		return err
	}
	if corrects == act.ID {
		return ErrInvalidAuthority
	}
	return nil
}

var prohibitedAlterationTerms = []string{
	"alterar-prazo", "prorrogar-temporada", "encurtar-temporada", "alterar-duracao",
	"alterar-genesis", "mudar-oferta", "alterar-supply",
	"alterar-riqueza", "mudar-limiar", "alterar-desempate",
	"vetar-sucessao", "impedir-sucessor",
}

func checkProhibitedAlterations(act RoyalAct) error {
	lowerTarget := strings.ToLower(act.Target)
	lowerEffect := strings.ToLower(act.Effect)
	lowerReason := strings.ToLower(act.Reason)

	for _, term := range prohibitedAlterationTerms {
		if strings.Contains(lowerTarget, term) ||
			strings.Contains(lowerEffect, term) ||
			strings.Contains(lowerReason, term) {
			return ErrProhibitedAlteration
		}
	}
	return nil
}

// DefineAct seals one explicit versioned decree before any effect.
// Every refusal arrives before execution: unknown kinds (including
// season alterations of deadline, Genesis or wealth criteria),
// incomplete decrees, harmful backdating, missing origins and
// ambiguous charter versions all stop here.
func DefineAct(act RoyalAct) (RoyalAct, error) {
	if err := checkActIdentity(act); err != nil {
		return RoyalAct{}, err
	}
	if err := checkActProse(act); err != nil {
		return RoyalAct{}, err
	}
	if err := checkProhibitedAlterations(act); err != nil {
		return RoyalAct{}, err
	}
	if err := checkActVigour(act); err != nil {
		return RoyalAct{}, err
	}
	if err := checkActCharter(act); err != nil {
		return RoyalAct{}, err
	}
	if err := checkActEconomics(act); err != nil {
		return RoyalAct{}, err
	}
	if err := checkActCorrection(act); err != nil {
		return RoyalAct{}, err
	}
	sealed := act
	sealed.DecreedAt = act.DecreedAt.UTC()
	sealed.Effective = act.Effective.UTC()
	if !act.EndsAt.IsZero() {
		sealed.EndsAt = act.EndsAt.UTC()
	}
	return sealed, nil
}
