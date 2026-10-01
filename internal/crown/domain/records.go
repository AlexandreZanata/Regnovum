package domain

import (
	"strconv"
	"strings"
	"time"
)

// PublicRecord is the privacy-safe public summary of one royal
// act: its existence, authorship, vigour, effect category and
// appeal reference, bound to the payload digest. It carries no
// reason, target, effect text, beneficiary, vault, origin or
// amount: the full text lives in the protected annex the digest
// seals, and victims never appear in public. The digest is
// auditable without exposing proof: anyone holding the sealed act
// recomputes it, nobody reads the act from it.
type PublicRecord struct {
	Act       ActID
	Author    HolderSubject
	Season    SeasonID
	Reign     ReignVersion
	Kind      ActKind
	Charter   CharterVersion
	DecreedAt time.Time
	Effective time.Time
	EndsAt    time.Time
	Digest    string
	Corrects  ActID
	Appeal    string
}

// Canonical joins the public fields in a stable order: the exact
// bytes auditors compare, with no payload text to leak.
func (r PublicRecord) Canonical() string {
	end := ""
	if !r.EndsAt.IsZero() {
		end = r.EndsAt.UTC().Format(time.RFC3339Nano)
	}
	return strings.Join([]string{
		string(r.Act), string(r.Author), string(r.Season),
		strconv.Itoa(int(r.Reign)),
		string(r.Kind), string(r.Charter),
		r.DecreedAt.UTC().Format(time.RFC3339Nano),
		r.Effective.UTC().Format(time.RFC3339Nano),
		end, r.Digest, string(r.Corrects), r.Appeal,
	}, "\x00")
}

// validateRecordShape checks one public summary: whole identity
// tokens, a named author, a closed kind, an explicit charter
// version, an ordered vigour window, a digest-shaped seal, and an
// appeal reference that names no act of its own when present.
func validateRecordShape(r PublicRecord) error {
	if _, err := ParseActID(string(r.Act)); err != nil {
		return err
	}
	if _, err := ParseHolderSubject(string(r.Author)); err != nil {
		return ErrAnonymousRecord
	}
	if _, err := ParseSeasonID(string(r.Season)); err != nil {
		return err
	}
	if _, err := ParseReignVersion(int(r.Reign)); err != nil {
		return err
	}
	if _, err := ParseActKind(string(r.Kind)); err != nil {
		return err
	}
	if _, err := ParseCharterVersion(string(r.Charter)); err != nil {
		return err
	}
	if r.DecreedAt.IsZero() || r.Effective.IsZero() {
		return ErrIncompleteAct
	}
	if r.Effective.UTC().Before(r.DecreedAt.UTC()) {
		return ErrRetroactiveAct
	}
	if !r.EndsAt.IsZero() && !r.EndsAt.UTC().After(r.Effective.UTC()) {
		return ErrInvalidAuthority
	}
	if !isDigest(r.Digest) {
		return ErrInvalidAuthority
	}
	if r.Corrects != "" {
		corrects, err := ParseActID(string(r.Corrects))
		if err != nil {
			return err
		}
		if corrects == r.Act {
			return ErrInvalidAuthority
		}
	}
	if r.Appeal != "" {
		appeal, err := parseToken(r.Appeal)
		if err != nil {
			return err
		}
		if appeal == string(r.Act) {
			return ErrInvalidAuthority
		}
	}
	return nil
}

// Summarize publishes the privacy-safe summary of one sealed act
// with its optional appeal reference. The digest binds the exact
// payload T02 sealed and T03 checked: it verifies here, and only
// the seal travels to the public. Reason, target, effect text,
// beneficiary, vault, origin and amount never leave the annex.
func Summarize(act RoyalAct, appeal string) (PublicRecord, error) {
	sealed, err := DefineAct(act)
	if err != nil {
		return PublicRecord{}, err
	}
	digest := actDigestOf(sealed)
	if appeal != "" {
		token, err := parseToken(appeal)
		if err != nil {
			return PublicRecord{}, err
		}
		if token == string(sealed.ID) {
			return PublicRecord{}, ErrInvalidAuthority
		}
		appeal = token
	}
	record := PublicRecord{
		Act: sealed.ID, Author: sealed.Author, Season: sealed.Season,
		Reign: sealed.Reign, Kind: sealed.Kind, Charter: sealed.Charter,
		DecreedAt: sealed.DecreedAt.UTC(), Effective: sealed.Effective.UTC(),
		Digest: digest, Corrects: sealed.Corrects, Appeal: appeal,
	}
	if !sealed.EndsAt.IsZero() {
		record.EndsAt = sealed.EndsAt.UTC()
	}
	if err := validateRecordShape(record); err != nil {
		return PublicRecord{}, err
	}
	return record, nil
}

// RoyalBook is the append-only sequence of public royal facts:
// every act is published once, corrections link their original,
// and no fact is ever rewritten or erased. There is no update
// and no delete: the method set is the guarantee.
type RoyalBook struct {
	records []PublicRecord
	index   map[ActID]int
}

// Append publishes one summary: whole shape, named author,
// digest intact, correction linked to a recorded original.
// Replaying the identical summary returns nil with no second
// entry; a divergent summary under a recorded act refuses.
func (b *RoyalBook) Append(record PublicRecord) error {
	if err := validateRecordShape(record); err != nil {
		return err
	}
	if b.index == nil {
		b.index = map[ActID]int{}
	}
	if at, ok := b.index[record.Act]; ok {
		if b.records[at].Digest != record.Digest {
			return ErrTamperedAct
		}
		return nil
	}
	if record.Corrects != "" {
		if _, ok := b.index[record.Corrects]; !ok {
			return ErrDetachedCorrection
		}
	}
	b.index[record.Act] = len(b.records)
	b.records = append(b.records, record)
	return nil
}

// Find returns one recorded summary by act.
func (b RoyalBook) Find(act ActID) (PublicRecord, error) {
	if _, err := ParseActID(string(act)); err != nil {
		return PublicRecord{}, err
	}
	at, ok := b.index[act]
	if !ok {
		return PublicRecord{}, ErrUnrecordedAct
	}
	return b.records[at], nil
}

// Records returns the recorded summaries in publication order.
func (b RoyalBook) Records() []PublicRecord {
	return append([]PublicRecord(nil), b.records...)
}

// AuditCoverage proves no executed act stays hidden: every
// executed act must name a recorded summary, or the audit names
// the first missing one.
func AuditCoverage(executed []ActID, book RoyalBook) error {
	for _, act := range executed {
		if _, err := ParseActID(string(act)); err != nil {
			return err
		}
		if _, ok := book.index[act]; !ok {
			return ErrUnrecordedAct
		}
	}
	return nil
}
