package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ChampionEntry is one wealth fact at the cutoff: the stable subject
// id, the published pseudonym, the display name at archive time,
// the cutoff wealth, the revision it was attained at and whether the
// account is anonymized. Facts are keyed by Subject: a later rename
// or anonymization changes display only, never the fact.
type ChampionEntry struct {
	Subject          string
	Pseudonym        string
	DisplayName      string
	Wealth           int64
	AttainedRevision int64
	Anonymized       bool
}

// ReignFact is one reign segment at the cutoff: holder, version and
// window. Duration derives from the window; the archive never edits
// the sealed book, it only cites it.
type ReignFact struct {
	Holder       string
	ReignVersion int
	StartedAt    time.Time
	EndsAt       time.Time
}

// CutoffSnapshot is the frozen ranking input: the book, its cutoff
// revision, the registered wealth policy, the cutoff wealth entries,
// the last King and the reign segments. Only cutoff data enters:
// post-cutoff releases never reach this shape.
type CutoffSnapshot struct {
	Season         string
	CutoffRevision int64
	Policy         string
	Entries        []ChampionEntry
	LastKing       string
	Reigns         []ReignFact
}

// Correction is one linked historical amendment: it cites the
// archive hash it amends, carries author/reason/instant and an
// optional amended display. The original fact stays preserved
// beside it; the sealed book is never rewritten.
type Correction struct {
	LinkedHash string
	Reason     string
	Author     string
	At         time.Time
	Note       string
}

// ChampionArchive is the sealed historical record of one book: the
// cutoff identity, the co-leaders at cutoff, the last King, the
// reigns, the content hash, the schema version and the linked
// corrections. Richest and last King are distinct facts: one prize
// never asserts the other.
type ChampionArchive struct {
	Season         string
	CutoffRevision int64
	Policy         string
	Hash           string
	Version        int
	CoLeaders      []ChampionEntry
	LastKing       string
	Reigns         []ReignFact
	Corrections    []Correction
}

// ChampionTitles carries the history words in one locale. Dates,
// wealth and holders travel as canonical values; only these
// dictionary strings are translated.
type ChampionTitles struct {
	Richest  string
	LastKing string
	History  string
}

// ChampionTitlesFor renders the closed champion dictionary in one
// locale: Portuguese or English. Unknown locales fall back to
// Portuguese wording without touching facts.
func ChampionTitlesFor(locale SeasonLocale) ChampionTitles {
	if locale == SeasonLocaleEnglish {
		return ChampionTitles{
			Richest:  "Richest at cutoff",
			LastKing: "Last King",
			History:  "Season history",
		}
	}
	return ChampionTitles{
		Richest:  "Mais rico no corte",
		LastKing: "Último Rei",
		History:  "Histórico de temporadas",
	}
}

func parseChampionToken(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidSeason
	}
	if utf8.RuneCountInString(raw) > maxSeasonRunes {
		return "", ErrInvalidSeason
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidSeason
		}
	}
	return raw, nil
}

func validChampionEntry(e ChampionEntry) error {
	if _, err := parseChampionToken(e.Subject); err != nil {
		return err
	}
	if _, err := parseChampionToken(e.Pseudonym); err != nil {
		return err
	}
	if e.DisplayName != "" {
		if strings.TrimSpace(e.DisplayName) != e.DisplayName {
			return ErrInvalidSeason
		}
		if utf8.RuneCountInString(e.DisplayName) > maxSeasonRunes {
			return ErrInvalidSeason
		}
		for _, r := range e.DisplayName {
			if unicode.IsControl(r) {
				return ErrInvalidSeason
			}
		}
	}
	if e.Wealth < 0 || e.AttainedRevision < 0 {
		return ErrInvalidSeason
	}
	return nil
}

// SelectCoLeaders returns every entry tied at the maximum cutoff
// wealth in deterministic subject order. Ties conserve co-leaders:
// no technical tiebreak invents financial superiority.
func SelectCoLeaders(entries []ChampionEntry) ([]ChampionEntry, error) {
	if len(entries) == 0 {
		return nil, ErrInvalidSeason
	}
	seen := make(map[string]bool, len(entries))
	var maxWealth int64 = -1
	for _, e := range entries {
		if err := validChampionEntry(e); err != nil {
			return nil, err
		}
		if seen[e.Subject] {
			return nil, ErrInvalidSeason
		}
		seen[e.Subject] = true
		if e.Wealth > maxWealth {
			maxWealth = e.Wealth
		}
	}
	var leaders []ChampionEntry
	for _, e := range entries {
		if e.Wealth == maxWealth {
			leaders = append(leaders, e)
		}
	}
	sort.Slice(leaders, func(i, j int) bool { return leaders[i].Subject < leaders[j].Subject })
	return leaders, nil
}

// ResolvePublicName renders the display name of one fact: the
// pseudonym when the account is anonymized or the policy omits
// names, otherwise the archived display name falling back to the
// pseudonym. The fact stays keyed by Subject either way.
func ResolvePublicName(e ChampionEntry, omitNames bool) string {
	if omitNames || e.Anonymized {
		return e.Pseudonym
	}
	if e.DisplayName != "" {
		return e.DisplayName
	}
	return e.Pseudonym
}

// hashChampions binds season, cutoff, policy, co-leaders, last King
// and reigns: any post-cutoff edit breaks the seal path.
func hashChampions(season string, cutoff int64, policy string, leaders []ChampionEntry, lastKing string, reigns []ReignFact) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\x00%d\x00%s\x00%s\x00", season, cutoff, policy, lastKing)
	for _, l := range leaders {
		fmt.Fprintf(&b, "%s\x00%s\x00%d\x00%d\x00", l.Subject, l.Pseudonym, l.Wealth, l.AttainedRevision)
	}
	for _, r := range reigns {
		fmt.Fprintf(&b, "%s\x00%d\x00%s\x00%s\x00",
			r.Holder, r.ReignVersion,
			r.StartedAt.UTC().Format(time.RFC3339Nano),
			r.EndsAt.UTC().Format(time.RFC3339Nano))
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// DeriveChampions derives the privacy-safe archive strictly from the
// cutoff snapshot: co-leaders at cutoff, last King, reigns and the
// content hash. Post-cutoff liberations never enter: the caller
// passes cutoff data only, and later revisions are judged by
// IsPostCutoffRevision.
func DeriveChampions(snapshot CutoffSnapshot) (ChampionArchive, error) {
	season, err := parseChampionToken(snapshot.Season)
	if err != nil {
		return ChampionArchive{}, err
	}
	if snapshot.CutoffRevision < 0 {
		return ChampionArchive{}, ErrInvalidSeason
	}
	policy, err := parseChampionToken(snapshot.Policy)
	if err != nil {
		return ChampionArchive{}, err
	}
	if snapshot.LastKing != "" {
		if _, err := parseChampionToken(snapshot.LastKing); err != nil {
			return ChampionArchive{}, err
		}
	}
	for _, r := range snapshot.Reigns {
		if _, err := parseChampionToken(r.Holder); err != nil {
			return ChampionArchive{}, err
		}
		if r.ReignVersion < 1 {
			return ChampionArchive{}, ErrInvalidSeason
		}
		if r.StartedAt.IsZero() || r.EndsAt.IsZero() || !r.EndsAt.After(r.StartedAt.UTC()) {
			return ChampionArchive{}, ErrInvalidSeason
		}
	}
	leaders, err := SelectCoLeaders(snapshot.Entries)
	if err != nil {
		return ChampionArchive{}, err
	}
	sortedReigns := append([]ReignFact(nil), snapshot.Reigns...)
	sort.Slice(sortedReigns, func(i, j int) bool { return sortedReigns[i].ReignVersion < sortedReigns[j].ReignVersion })
	hash := hashChampions(season, snapshot.CutoffRevision, policy, leaders, snapshot.LastKing, sortedReigns)
	return ChampionArchive{
		Season: snapshot.Season, CutoffRevision: snapshot.CutoffRevision,
		Policy: policy, Hash: hash, Version: 1,
		CoLeaders: leaders, LastKing: snapshot.LastKing, Reigns: sortedReigns,
	}, nil
}

// IsPostCutoffRevision reports whether one revision came after the
// cutoff: late refunds, releases and corrections never grant new
// cutoff wealth and never re-rank the archive.
func IsPostCutoffRevision(archive ChampionArchive, revision int64) bool {
	return revision > archive.CutoffRevision
}

// AppendCorrection links one historical amendment without altering
// the sealed facts: the linked hash must cite the archive, the
// original co-leaders and reigns stay byte-equal, and the correction
// is appended. The sealed book is never rewritten.
func AppendCorrection(archive ChampionArchive, correction Correction) (ChampionArchive, error) {
	if correction.LinkedHash == "" || correction.LinkedHash != archive.Hash {
		return ChampionArchive{}, ErrInvalidSeason
	}
	if _, err := parseChampionToken(correction.Author); err != nil {
		return ChampionArchive{}, err
	}
	if strings.TrimSpace(correction.Reason) != correction.Reason || correction.Reason == "" {
		return ChampionArchive{}, ErrInvalidSeason
	}
	if utf8.RuneCountInString(correction.Reason) > maxSeasonRunes {
		return ChampionArchive{}, ErrInvalidSeason
	}
	if correction.At.IsZero() {
		return ChampionArchive{}, ErrInvalidSeason
	}
	if utf8.RuneCountInString(correction.Note) > maxCloseDetailRunes {
		return ChampionArchive{}, ErrInvalidSeason
	}
	for _, r := range correction.Reason + correction.Note {
		if unicode.IsControl(r) {
			return ChampionArchive{}, ErrInvalidSeason
		}
	}
	archive.Corrections = append(archive.Corrections, correction)
	return archive, nil
}

// ChampionCacheKey builds the cache key of one champion view: the
// book, the person and the locale travel together, so a cache never
// mixes persons or locales.
func ChampionCacheKey(season, subject string, locale SeasonLocale) (string, error) {
	s, err := parseChampionToken(season)
	if err != nil {
		return "", err
	}
	sub, err := parseChampionToken(subject)
	if err != nil {
		return "", err
	}
	if !locale.IsValid() {
		return "", ErrInvalidSeason
	}
	return s + "\x00" + sub + "\x00" + string(locale), nil
}

// ExportedChampion is the redacted view of one fact: exact wealth
// travels only with explicit permission, otherwise only the
// pseudonym and rank travel.
type ExportedChampion struct {
	Subject      string
	Display      string
	Wealth       int64
	WealthHidden bool
}

// ExportChampions redacts one archive for one viewer: exact wealth
// leaves only with canSeeExact (titular or authorized auditor);
// third parties receive pseudonyms and hidden wealth. Names follow
// the omitNames policy or the anonymized flag. The íntegra never
// leaves through the third-party path.
func ExportChampions(archive ChampionArchive, omitNames, canSeeExact bool) []ExportedChampion {
	out := make([]ExportedChampion, 0, len(archive.CoLeaders))
	for _, l := range archive.CoLeaders {
		display := ResolvePublicName(l, omitNames)
		wealth, hidden := l.Wealth, false
		if !canSeeExact {
			wealth, hidden = 0, true
		}
		out = append(out, ExportedChampion{
			Subject: l.Subject, Display: display,
			Wealth: wealth, WealthHidden: hidden,
		})
	}
	return out
}
