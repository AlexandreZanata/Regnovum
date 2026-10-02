package domain

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// maxInstitutionRunes bounds opaque institutional tokens: long
// enough for act, author, office, competence, rule, season and
// mandate references, short enough to stay out of log abuse.
const maxInstitutionRunes = 128

// maxInstitutionDetailRunes bounds one effect detail: long enough
// to name the institutional effect, short enough to stay out of
// log abuse.
const maxInstitutionDetailRunes = 2000

// parseInstitutionToken validates one opaque token: exact match,
// bounded, never blank, no control characters.
func parseInstitutionToken(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidAct
	}
	if utf8.RuneCountInString(raw) > maxInstitutionRunes {
		return "", ErrInvalidAct
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidAct
		}
	}
	return raw, nil
}

// parseInstitutionDetail validates one effect detail.
func parseInstitutionDetail(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidAct
	}
	if utf8.RuneCountInString(raw) > maxInstitutionDetailRunes {
		return "", ErrInvalidAct
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidAct
		}
	}
	return raw, nil
}

// SeasonKey is the identity of one season book the act belongs
// to. Acts never cross books: offices expire with the season,
// and history stays identified by its own book.
type SeasonKey string

// ParseSeasonKey validates a season book key.
func ParseSeasonKey(raw string) (SeasonKey, error) {
	token, err := parseInstitutionToken(raw)
	if err != nil {
		return "", err
	}
	return SeasonKey(token), nil
}

// String returns the stored season key value.
func (k SeasonKey) String() string { return string(k) }

// Audit is the independent audit stamp of one office act: who
// audited it and when. The auditor is never the author.
type Audit struct {
	Auditor   string
	AuditedAt time.Time
}

// OfficeAct is one recorded office act: its identity, who
// authored it in which office, under which competence and rule,
// in which season book, what mandate authorized it, what effect
// it had and when, the act it corrects when it is a correction,
// and its independent audit. It moves nothing: later tasks
// authorize effects from it, never inside it.
type OfficeAct struct {
	ID         string
	Author     string
	Office     string
	Competence string
	Rule       string
	Season     SeasonKey
	Mandate    string
	Effect     string
	At         time.Time
	Corrects   string
	Audit      Audit
}

// ActSummary is the privacy-safe public view of one office act:
// identity, office function, competence, rule, season, effect,
// date and digest. It names the function, never the person: no
// personal identifier crosses into the public summary.
type ActSummary struct {
	ID         string
	Office     string
	Competence string
	Rule       string
	Season     SeasonKey
	Effect     string
	At         time.Time
	Digest     string
}

// ActRequest carries one act recording: the act fields with the
// audit stamp and the readers allowed into the integra.
type ActRequest struct {
	ID         string
	Author     string
	Office     string
	Competence string
	Rule       string
	Season     SeasonKey
	Mandate    string
	Effect     string
	At         time.Time
	Corrects   string
	Auditor    string
	AuditedAt  time.Time
	Viewers    []string
}

// Record is one book entry: the act with the readers allowed
// into its integra. The public is never listed: it reads the
// summary, never the full record.
type Record struct {
	Act     OfficeAct
	Viewers []string
}

// Book is the append-only institutional book: entries in record
// order with corrections beside their originals.
type Book struct {
	Records []Record
}

// checkActShape validates the act shape: every token whole, the
// season named, the effect stated, live instants with audit at
// or after the act.
func checkActShape(req ActRequest) error {
	for _, raw := range []string{req.ID, req.Author, req.Office, req.Competence, req.Rule, req.Mandate, req.Auditor} {
		if _, err := parseInstitutionToken(raw); err != nil {
			return err
		}
	}
	if _, err := ParseSeasonKey(string(req.Season)); err != nil {
		return err
	}
	if _, err := parseInstitutionDetail(req.Effect); err != nil {
		return err
	}
	if req.At.IsZero() || req.AuditedAt.IsZero() {
		return ErrInvalidAct
	}
	if req.AuditedAt.UTC().Before(req.At.UTC()) {
		return ErrInvalidAct
	}
	for _, viewer := range req.Viewers {
		if _, err := parseInstitutionToken(viewer); err != nil {
			return err
		}
	}
	return nil
}

// checkActAuthority binds authority and audit: the mandate is
// named and the auditor is independent of the author. An act
// without authority or audit fails here, before any record.
func checkActAuthority(req ActRequest) error {
	if req.Mandate == "" || req.Auditor == "" {
		return ErrUnauthorizedAct
	}
	if req.Auditor == req.Author {
		return ErrUnauthorizedAct
	}
	return nil
}

// findRecord answers whether the identity already records an act.
func findRecord(book Book, id string) (Record, bool) {
	for _, record := range book.Records {
		if record.Act.ID == id {
			return record, true
		}
	}
	return Record{}, false
}

// sameAct reports whether the recorded act matches the request.
func sameAct(record Record, req ActRequest) bool {
	act := record.Act
	return act.Author == req.Author && act.Office == req.Office &&
		act.Competence == req.Competence && act.Rule == req.Rule &&
		act.Season == req.Season && act.Mandate == req.Mandate &&
		act.Effect == req.Effect && act.At.UTC().Equal(req.At.UTC()) &&
		act.Corrects == req.Corrects && act.Audit.Auditor == req.Auditor &&
		act.Audit.AuditedAt.UTC().Equal(req.AuditedAt.UTC())
}

// Append records one office act with its audit and readers. A
// correction binds a recorded original, kept visible beside it.
// An identical replay returns the book unchanged; a divergent
// payload under a recorded identity conflicts.
func Append(book Book, req ActRequest) (Book, error) {
	if err := checkActShape(req); err != nil {
		return Book{}, err
	}
	if err := checkActAuthority(req); err != nil {
		return Book{}, err
	}
	if req.Corrects != "" {
		if _, found := findRecord(book, req.Corrects); !found {
			return Book{}, ErrUnknownCorrection
		}
		if req.Corrects == req.ID {
			return Book{}, ErrInvalidAct
		}
	}
	if recorded, found := findRecord(book, req.ID); found {
		if sameAct(recorded, req) {
			return book, nil
		}
		return Book{}, ErrEntryConflict
	}
	viewers := append([]string{}, req.Viewers...)
	return Book{Records: append(append([]Record{}, book.Records...), Record{
		Act: OfficeAct{
			ID: req.ID, Author: req.Author, Office: req.Office,
			Competence: req.Competence, Rule: req.Rule, Season: req.Season,
			Mandate: req.Mandate, Effect: req.Effect, At: req.At.UTC(),
			Corrects: req.Corrects,
			Audit:    Audit{Auditor: req.Auditor, AuditedAt: req.AuditedAt.UTC()},
		},
		Viewers: viewers,
	})}, nil
}

// Summarize derives the privacy-safe public view of one recorded
// act: function, competence, rule, season, effect, date and a
// digest bound to the act identity. The person stays out.
func Summarize(record Record) ActSummary {
	return ActSummary{
		ID: record.Act.ID, Office: record.Act.Office,
		Competence: record.Act.Competence, Rule: record.Act.Rule,
		Season: record.Act.Season, Effect: record.Act.Effect,
		At:     record.Act.At,
		Digest: "ato:" + record.Act.ID + "|oficio:" + record.Act.Office + "|regra:" + record.Act.Rule,
	}
}

// ReadFull answers the integra of one recorded act to a listed
// reader. Anyone else — including the public — is refused: they
// read the summary, never the full record.
func ReadFull(book Book, id, reader string) (Record, error) {
	recorded, found := findRecord(book, id)
	if !found {
		return Record{}, ErrUnknownCorrection
	}
	for _, viewer := range recorded.Viewers {
		if viewer == reader {
			return recorded, nil
		}
	}
	return Record{}, ErrAccessDenied
}
