package domain

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// maxReputationRunes bounds opaque reputation tokens: long enough
// for subject, role, season and fact links, short enough to stay
// out of log and index abuse.
const maxReputationRunes = 128

// maxReputationDetailRunes bounds one fact detail: long enough to
// name the auditable event, short enough to stay out of log abuse.
const maxReputationDetailRunes = 2000

// EventKind is the closed vocabulary of auditable professional
// events: a deadline met, a reversal suffered, a conflict
// declared and a decision upheld on review. Payment, purchase
// and anything else are absent here on purpose, so money never
// becomes standing by inference.
type EventKind string

const (
	// EventMetDeadline records a deadline the professional met.
	EventMetDeadline EventKind = "prazo-cumprido"
	// EventReversal records a decision later reversed against the
	// professional: a fact, not a sentence, kept visible.
	EventReversal EventKind = "reversao"
	// EventConflict records a conflict the professional declared
	// and stepped aside from.
	EventConflict EventKind = "conflito-declarado"
	// EventUpheldDecision records a decision that survived review.
	EventUpheldDecision EventKind = "decisao-mantida"
)

// ParseEventKind validates a kind token. Matching is exact: no
// trimming, no case folding, no inference.
func ParseEventKind(raw string) (EventKind, error) {
	switch EventKind(raw) {
	case EventMetDeadline, EventReversal, EventConflict, EventUpheldDecision:
		return EventKind(raw), nil
	default:
		return "", ErrUnknownEvent
	}
}

// String returns the stored kind value.
func (k EventKind) String() string { return string(k) }

// parseReputationToken validates one opaque token: exact match,
// bounded, never blank, no control characters.
func parseReputationToken(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidRecord
	}
	if utf8.RuneCountInString(raw) > maxReputationRunes {
		return "", ErrInvalidRecord
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidRecord
		}
	}
	return raw, nil
}

// parseReputationDetail validates one fact detail: exact match,
// bounded, never blank, no control characters.
func parseReputationDetail(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidRecord
	}
	if utf8.RuneCountInString(raw) > maxReputationDetailRunes {
		return "", ErrInvalidRecord
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidRecord
		}
	}
	return raw, nil
}

// SeasonKey is the identity of one season book the standing
// belongs to. Standing never crosses books: the same work in
// another season is another record, and historic merit grants
// no current wealth or office.
type SeasonKey string

// ParseSeasonKey validates a season book key.
func ParseSeasonKey(raw string) (SeasonKey, error) {
	token, err := parseReputationToken(raw)
	if err != nil {
		return "", err
	}
	return SeasonKey(token), nil
}

// String returns the stored season key value.
func (k SeasonKey) String() string { return string(k) }

// StandingEvent is one recorded auditable fact: its identity,
// kind, when it happened, which professional fact it proves and
// why. It carries counts only through aggregation: no score, no
// weight, no payment lives here.
type StandingEvent struct {
	ID     string
	Kind   EventKind
	At     time.Time
	Link   string
	Detail string
}

// Correction is one correction linked to a recorded event: its
// identity, the fact it corrects, when and why. The original
// stays: a correction annotates, never rewrites.
type Correction struct {
	ID       string
	Corrects string
	At       time.Time
	Detail   string
}

// StandingRecord is the factual history of one subject in one
// role and season: the recorded events with the corrections
// linked to them. One profession never inherits another: events
// bind exactly one role, and aggregates filter by it.
type StandingRecord struct {
	Subject     string
	Role        string
	Season      SeasonKey
	Events      []StandingEvent
	Corrections []Correction
}

// EventRequest carries one event recording: the record it
// joins, the new event identity and kind, when it happened,
// which fact it proves and why.
type EventRequest struct {
	Record StandingRecord
	ID     string
	Kind   string
	At     time.Time
	Link   string
	Detail string
}

// CorrectionRequest carries one correction: the record it
// joins, the new correction identity, the recorded fact it
// corrects, when and why.
type CorrectionRequest struct {
	Record   StandingRecord
	ID       string
	Corrects string
	At       time.Time
	Detail   string
}

// ForgetRequest carries one lawful deletion: the record, the
// event to forget, whether its retention is due, whether a
// legal hold keeps it, and when the request arrived.
type ForgetRequest struct {
	Record       StandingRecord
	EventID      string
	RetentionDue bool
	LegalHold    bool
	At           time.Time
}

// checkRecordIdentity validates the standing identity: whole
// subject, role and season.
func checkRecordIdentity(record StandingRecord) error {
	if _, err := parseReputationToken(record.Subject); err != nil {
		return err
	}
	if _, err := parseReputationToken(record.Role); err != nil {
		return err
	}
	if _, err := ParseSeasonKey(string(record.Season)); err != nil {
		return err
	}
	return nil
}

// findEvent answers whether the identity already records an event.
func findEvent(record StandingRecord, id string) (StandingEvent, bool) {
	for _, event := range record.Events {
		if event.ID == id {
			return event, true
		}
	}
	return StandingEvent{}, false
}

// findCorrection answers whether the identity already records a correction.
func findCorrection(record StandingRecord, id string) (Correction, bool) {
	for _, correction := range record.Corrections {
		if correction.ID == id {
			return correction, true
		}
	}
	return Correction{}, false
}

// correctedBy answers whether any correction links to the event.
func correctedBy(record StandingRecord, eventID string) bool {
	for _, correction := range record.Corrections {
		if correction.Corrects == eventID {
			return true
		}
	}
	return false
}

// RecordEvent records one auditable event in its role and
// season. An identical replay returns the record unchanged; a
// divergent payload under a recorded identity conflicts instead
// of duplicating. Payment and unknown kinds stop here.
func RecordEvent(req EventRequest) (StandingRecord, error) {
	if err := checkRecordIdentity(req.Record); err != nil {
		return StandingRecord{}, err
	}
	kind, err := ParseEventKind(req.Kind)
	if err != nil {
		return StandingRecord{}, err
	}
	id, err := parseReputationToken(req.ID)
	if err != nil {
		return StandingRecord{}, err
	}
	link, err := parseReputationToken(req.Link)
	if err != nil {
		return StandingRecord{}, err
	}
	detail, err := parseReputationDetail(req.Detail)
	if err != nil {
		return StandingRecord{}, err
	}
	if req.At.IsZero() {
		return StandingRecord{}, ErrInvalidRecord
	}
	if recorded, found := findEvent(req.Record, id); found {
		if recorded.Kind == kind && recorded.At.UTC().Equal(req.At.UTC()) &&
			recorded.Link == link && recorded.Detail == detail {
			return req.Record, nil
		}
		return StandingRecord{}, ErrEventConflict
	}
	record := req.Record
	record.Events = append(record.Events, StandingEvent{
		ID: id, Kind: kind, At: req.At.UTC(), Link: link, Detail: detail,
	})
	return record, nil
}

// CorrectEvent links one correction to a recorded fact. The
// original stays beside it: twenty corrections annotate one
// fact, they never rewrite it. Unknown facts and divergent
// replays stop here.
func CorrectEvent(req CorrectionRequest) (StandingRecord, error) {
	if err := checkRecordIdentity(req.Record); err != nil {
		return StandingRecord{}, err
	}
	id, err := parseReputationToken(req.ID)
	if err != nil {
		return StandingRecord{}, err
	}
	corrects, err := parseReputationToken(req.Corrects)
	if err != nil {
		return StandingRecord{}, err
	}
	detail, err := parseReputationDetail(req.Detail)
	if err != nil {
		return StandingRecord{}, err
	}
	if req.At.IsZero() {
		return StandingRecord{}, ErrInvalidRecord
	}
	if _, found := findEvent(req.Record, corrects); !found {
		return StandingRecord{}, ErrUnknownFact
	}
	if recorded, found := findCorrection(req.Record, id); found {
		if recorded.Corrects == corrects && recorded.At.UTC().Equal(req.At.UTC()) &&
			recorded.Detail == detail {
			return req.Record, nil
		}
		return StandingRecord{}, ErrEventConflict
	}
	record := req.Record
	record.Corrections = append(record.Corrections, Correction{
		ID: id, Corrects: corrects, At: req.At.UTC(), Detail: detail,
	})
	return record, nil
}

// ForgetEvent removes one event the policy releases: retention
// due, no legal hold, and no correction linked to it. Anything
// else stays: holds, early requests, linked facts and unknown
// identities all refuse with the record untouched.
func ForgetEvent(req ForgetRequest) (StandingRecord, error) {
	if err := checkRecordIdentity(req.Record); err != nil {
		return StandingRecord{}, err
	}
	id, err := parseReputationToken(req.EventID)
	if err != nil {
		return StandingRecord{}, err
	}
	if req.At.IsZero() {
		return StandingRecord{}, ErrInvalidRecord
	}
	event, found := findEvent(req.Record, id)
	if !found {
		return StandingRecord{}, ErrUnknownFact
	}
	_ = event
	if req.LegalHold || !req.RetentionDue {
		return StandingRecord{}, ErrForgetBlocked
	}
	if correctedBy(req.Record, id) {
		return StandingRecord{}, ErrForgetBlocked
	}
	record := req.Record
	kept := make([]StandingEvent, 0, len(record.Events))
	for _, candidate := range record.Events {
		if candidate.ID != id {
			kept = append(kept, candidate)
		}
	}
	record.Events = kept
	return record, nil
}

// Standing is the identified aggregate of one record: whose,
// in which role and season, how many facts of each kind, and
// which facts. Counts only: no score, no rank, nothing a
// treasury or an office could read as entitlement.
type Standing struct {
	Subject      string
	Role         string
	Season       SeasonKey
	MetDeadlines int64
	Reversals    int64
	Conflicts    int64
	Upheld       int64
	EventIDs     []string
	Corrections  int64
}

// Total answers how many facts the standing aggregates.
func (s Standing) Total() int64 {
	return s.MetDeadlines + s.Reversals + s.Conflicts + s.Upheld
}

// Aggregate derives the identified standing of one record: the
// per-kind counts with the fact identities, in record order. It
// reads, never writes, and grants nothing beyond the facts.
func Aggregate(record StandingRecord) (Standing, error) {
	if err := checkRecordIdentity(record); err != nil {
		return Standing{}, err
	}
	standing := Standing{
		Subject: record.Subject, Role: record.Role, Season: record.Season,
		EventIDs:    make([]string, 0, len(record.Events)),
		Corrections: int64(len(record.Corrections)),
	}
	for _, event := range record.Events {
		switch event.Kind {
		case EventMetDeadline:
			standing.MetDeadlines++
		case EventReversal:
			standing.Reversals++
		case EventConflict:
			standing.Conflicts++
		case EventUpheldDecision:
			standing.Upheld++
		default:
			return Standing{}, ErrUnknownEvent
		}
		standing.EventIDs = append(standing.EventIDs, event.ID)
	}
	return standing, nil
}
