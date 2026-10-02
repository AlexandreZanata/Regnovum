package domain

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// maxInquisitionRunes bounds opaque inquisition tokens and prose:
// long enough for operation ids and motives, short enough to stay
// out of log abuse.
const maxInquisitionRunes = 2000

// ViolationKind is the closed vocabulary of charter violations
// that authorize institutional action without a specific
// acceptance: threat, fraud, harassment, data abuse and malware.
// Private debt and private challenge are absent here on purpose:
// they refuse with ErrPrivateMatter instead of opening a case.
type ViolationKind string

const (
	// ViolationThreat names a safety threat report.
	ViolationThreat ViolationKind = "ameaca"
	// ViolationFraud names a fraud report.
	ViolationFraud ViolationKind = "fraude"
	// ViolationHarassment names a harassment report.
	ViolationHarassment ViolationKind = "assedio"
	// ViolationDataAbuse names a personal data abuse report.
	ViolationDataAbuse ViolationKind = "dados"
	// ViolationMalware names a malware report.
	ViolationMalware ViolationKind = "malware"
)

// ParseViolationKind validates a kind token. Matching is exact:
// no trimming, no case folding, no inference.
func ParseViolationKind(raw string) (ViolationKind, error) {
	switch ViolationKind(raw) {
	case ViolationThreat, ViolationFraud, ViolationHarassment,
		ViolationDataAbuse, ViolationMalware:
		return ViolationKind(raw), nil
	default:
		return "", ErrUnknownViolation
	}
}

// String returns the stored violation value.
func (v ViolationKind) String() string { return string(v) }

// isPrivateMatter reports whether raw names a bilateral matter:
// debt collection and private challenge belong to private
// dispute, never to institutional sanction.
func isPrivateMatter(raw string) bool {
	return raw == "divida" || raw == "desafio"
}

// CharterVersion is one explicit constitutional version: v
// followed by a positive integer, ASCII only. It mirrors the
// charter module without importing it: domains stay disjoint by
// architecture, and "current" or "latest" never name a version.
type CharterVersion string

// ParseCharterVersion validates a version token. Matching is
// exact: no trimming, no case folding, no zero-padded numbers.
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

// ReportID identifies one safety report.
type ReportID string

// ParseReportID validates one report token.
func ParseReportID(raw string) (ReportID, error) {
	token, err := parseToken(raw)
	if err != nil {
		return "", err
	}
	return ReportID(token), nil
}

// String returns the stored report value.
func (r ReportID) String() string { return string(r) }

// Subject identifies one participant account in a report. The
// account is never created, altered or deleted here: reports
// name subjects, never touch them.
type Subject string

// ParseSubject validates one subject token.
func ParseSubject(raw string) (Subject, error) {
	token, err := parseToken(raw)
	if err != nil {
		return "", err
	}
	return Subject(token), nil
}

// String returns the stored subject value.
func (s Subject) String() string { return string(s) }

// ContainmentID identifies one temporary containment order.
type ContainmentID string

// ParseContainmentID validates one containment token.
func ParseContainmentID(raw string) (ContainmentID, error) {
	token, err := parseToken(raw)
	if err != nil {
		return "", err
	}
	return ContainmentID(token), nil
}

// String returns the stored containment value.
func (c ContainmentID) String() string { return string(c) }

// parseToken validates one opaque token: exact match, no control
// characters, bounded length.
func parseToken(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidReport
	}
	if utf8.RuneCountInString(raw) > 128 {
		return "", ErrInvalidReport
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidReport
		}
	}
	return raw, nil
}

// parseText validates one prose field: exact match, bounded,
// never blank.
func parseText(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidReport
	}
	if utf8.RuneCountInString(raw) > maxInquisitionRunes {
		return "", ErrInvalidReport
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidReport
		}
	}
	return raw, nil
}

// isDigest reports whether raw is 64 lowercase hex digits: the
// sealed evidence shape. Hashes never carry the proof itself,
// only its binding.
func isDigest(raw string) bool {
	if len(raw) != 64 {
		return false
	}
	for _, c := range raw {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Report is one filed safety report: which closed violation, by
// whom, against whom, under which explicit charter version, why,
// with which sealed evidence when present, and when. Filing
// moves nothing and sanctions nobody: it only opens the safety
// track a later containment may use.
type Report struct {
	ID       ReportID
	Kind     ViolationKind
	Reporter Subject
	Subject  Subject
	Charter  CharterVersion
	Reason   string
	Evidence string
	FiledAt  time.Time
}

// ReportDraft carries one filing request: the kind arrives raw
// so private matters refuse with their own code instead of
// passing as unknown.
type ReportDraft struct {
	ID       ReportID
	KindRaw  string
	Reporter Subject
	Subject  Subject
	Charter  string
	Reason   string
	Evidence string
	FiledAt  time.Time
}

// checkReportIdentity validates filing tokens: whole
// identifiers, two distinct subjects, a stated reason, a live
// filing instant.
func checkReportIdentity(draft ReportDraft) error {
	if _, err := ParseReportID(string(draft.ID)); err != nil {
		return err
	}
	reporter, err := ParseSubject(string(draft.Reporter))
	if err != nil {
		return err
	}
	subject, err := ParseSubject(string(draft.Subject))
	if err != nil {
		return err
	}
	if reporter == subject {
		return ErrInvalidReport
	}
	if _, err := parseText(draft.Reason); err != nil {
		return ErrInvalidReport
	}
	if draft.FiledAt.IsZero() {
		return ErrInvalidReport
	}
	return nil
}

// checkReportKind binds the raw kind: private debt and challenge
// refuse as bilateral matters, the five safety species pass, and
// anything else refuses as unknown.
func checkReportKind(raw string) (ViolationKind, error) {
	if isPrivateMatter(raw) {
		return "", ErrPrivateMatter
	}
	return ParseViolationKind(raw)
}

// checkReportEvidence validates the optional sealed proof: when
// present it arrives as 64 lowercase hex digits, never raw
// content.
func checkReportEvidence(raw string) error {
	if raw == "" {
		return nil
	}
	if !isDigest(raw) {
		return ErrUnprovenAccusation
	}
	return nil
}

// FileReport files one safety report on the persistent safety
// track. Private debt and private challenge refuse here: they
// belong to bilateral dispute. Filing sanctions nobody: proof
// is judged when containment is requested.
func FileReport(draft ReportDraft) (Report, error) {
	if err := checkReportIdentity(draft); err != nil {
		return Report{}, err
	}
	kind, err := checkReportKind(draft.KindRaw)
	if err != nil {
		return Report{}, err
	}
	charter, err := ParseCharterVersion(draft.Charter)
	if err != nil {
		return Report{}, err
	}
	if err := checkReportEvidence(draft.Evidence); err != nil {
		return Report{}, err
	}
	return Report{
		ID: draft.ID, Kind: kind, Reporter: draft.Reporter,
		Subject: draft.Subject, Charter: charter, Reason: draft.Reason,
		Evidence: draft.Evidence, FiledAt: draft.FiledAt.UTC(),
	}, nil
}

// Containment is one temporary safety hold over a reported
// subject: which report it answers, who ordered it, why, and
// the bounded window it covers. It ends where its window ends:
// extension arrives only as a new grounded order.
type Containment struct {
	ID        ContainmentID
	Report    ReportID
	Subject   Subject
	Authority Subject
	Motive    string
	StartsAt  time.Time
	EndsAt    time.Time
}

// ContainOrder carries one containment request: its identity,
// the recorded authority and motive, the requested temporary
// window with the longest window the caller allows, and the
// decision instant. Durations are policy parameters of the
// call, never ratified values of this module.
type ContainOrder struct {
	ID        ContainmentID
	Authority Subject
	Motive    string
	Window    time.Duration
	MaxWindow time.Duration
	At        time.Time
}

// checkContainOrder validates the order shape: a whole identity,
// a recorded authority, a stated motive, a live instant, and a
// positive window within the allowed cap.
func checkContainOrder(order ContainOrder) error {
	if _, err := ParseContainmentID(string(order.ID)); err != nil {
		return ErrUngroundedContainment
	}
	if _, err := ParseSubject(string(order.Authority)); err != nil {
		return ErrUngroundedContainment
	}
	if _, err := parseText(order.Motive); err != nil {
		return ErrUngroundedContainment
	}
	if order.At.IsZero() {
		return ErrUngroundedContainment
	}
	if order.Window <= 0 || order.MaxWindow <= 0 || order.Window > order.MaxWindow {
		return ErrUngroundedContainment
	}
	return nil
}

// AuthorizeContainment holds one reported subject temporarily on
// a risk report with sealed proof. The report must carry
// evidence: a private accusation without proof activates no
// sanction. Authority and motive travel on record, and the hold
// never exceeds the allowed window.
func AuthorizeContainment(report Report, order ContainOrder) (Containment, error) {
	if _, err := ParseReportID(string(report.ID)); err != nil {
		return Containment{}, err
	}
	if _, err := ParseViolationKind(string(report.Kind)); err != nil {
		return Containment{}, err
	}
	if _, err := ParseSubject(string(report.Subject)); err != nil {
		return Containment{}, err
	}
	if err := checkContainOrder(order); err != nil {
		return Containment{}, err
	}
	if !isDigest(report.Evidence) {
		return Containment{}, ErrUnprovenAccusation
	}
	start := order.At.UTC()
	return Containment{
		ID: order.ID, Report: report.ID, Subject: report.Subject,
		Authority: order.Authority, Motive: order.Motive,
		StartsAt: start, EndsAt: start.Add(order.Window),
	}, nil
}
