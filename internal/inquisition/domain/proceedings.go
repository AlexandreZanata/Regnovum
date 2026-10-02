package domain

import (
	"time"
)

// CaseID identifies one severe proceeding.
type CaseID string

// ParseCaseID validates one case token.
func ParseCaseID(raw string) (CaseID, error) {
	token, err := parseToken(raw)
	if err != nil {
		return "", ErrInvalidReport
	}
	return CaseID(token), nil
}

// String returns the stored case value.
func (c CaseID) String() string { return string(c) }

// Mandate is the opener competence window: which holder may
// open, from when until when. The window is half-open:
// the start admits, the exact end already belongs out.
type Mandate struct {
	Holder    Subject
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// SevereCase is one opened severe proceeding: which proven
// report it answers, who opens, who judges, who audits, who
// answers, which King reigns at the opening, which holders
// count as financially or relationally interested, under
// which published competence and deadline. It moves no value
// and blocks no succession by itself: denunciation alone
// never impedes succession, and an interested King neither
// seats the panel nor reviews a rival.
type SevereCase struct {
	ID         CaseID
	Report     ReportID
	Accused    Subject
	Inquisitor Subject
	Arbiter    Subject
	Auditor    Subject
	King       Subject
	Interested []Subject
	Competence string
	OpenedAt   time.Time
	Deadline   time.Time
}

// OpenRequest carries one opening request: the new case
// identity, the proven report it answers, the three distinct
// offices, the reigning King, the interested set, the
// published competence and deadline with the longest span
// the caller allows, the opener mandate, and the registries
// that refuse duplicates.
type OpenRequest struct {
	ID              CaseID
	Report          Report
	Inquisitor      Subject
	Arbiter         Subject
	Auditor         Subject
	King            Subject
	Interested      []Subject
	Competence      string
	Mandate         Mandate
	OpenedAt        time.Time
	Deadline        time.Time
	MaxDuration     time.Duration
	ExistingIDs     []CaseID
	ExistingReports []ReportID
}

// containsSubject reports whether holder belongs to the set.
func containsSubject(holder Subject, set []Subject) bool {
	for _, party := range set {
		if holder == party {
			return true
		}
	}
	return false
}

// containsCase reports whether id is already registered.
func containsCase(id CaseID, existing []CaseID) bool {
	for _, other := range existing {
		if id == other {
			return true
		}
	}
	return false
}

// containsReport reports whether the report already opened a case.
func containsReport(id ReportID, existing []ReportID) bool {
	for _, other := range existing {
		if id == other {
			return true
		}
	}
	return false
}

// checkOpenIdentity validates opening tokens: whole case,
// holders and competence, a live ordered opening with a
// published deadline, and a well-shaped mandate.
func checkOpenIdentity(req OpenRequest) error {
	if _, err := ParseCaseID(string(req.ID)); err != nil {
		return err
	}
	for _, holder := range []Subject{req.Inquisitor, req.Arbiter, req.Auditor, req.King} {
		if _, err := ParseSubject(string(holder)); err != nil {
			return err
		}
	}
	for _, party := range req.Interested {
		if _, err := ParseSubject(string(party)); err != nil {
			return err
		}
	}
	if _, err := parseText(req.Competence); err != nil {
		return ErrUnpublishedProceeding
	}
	if _, err := ParseSubject(string(req.Mandate.Holder)); err != nil {
		return ErrExpiredMandate
	}
	if req.Mandate.IssuedAt.IsZero() || req.Mandate.ExpiresAt.IsZero() {
		return ErrExpiredMandate
	}
	if !req.Mandate.ExpiresAt.After(req.Mandate.IssuedAt.UTC()) {
		return ErrExpiredMandate
	}
	if req.OpenedAt.IsZero() || req.Deadline.IsZero() {
		return ErrUnpublishedProceeding
	}
	if req.MaxDuration <= 0 {
		return ErrUnpublishedProceeding
	}
	opened := req.OpenedAt.UTC()
	deadline := req.Deadline.UTC()
	if !deadline.After(opened) {
		return ErrUnpublishedProceeding
	}
	if deadline.Sub(opened) > req.MaxDuration {
		return ErrUnpublishedProceeding
	}
	return nil
}

// checkOpenReport binds the opening to one proven report: a
// closed safety kind, two distinct parties, an explicit
// charter version and sealed proof. A bare denunciation
// without proof opens no severe case.
func checkOpenReport(report Report) error {
	if _, err := ParseReportID(string(report.ID)); err != nil {
		return err
	}
	if _, err := ParseViolationKind(string(report.Kind)); err != nil {
		return err
	}
	reporter, err := ParseSubject(string(report.Reporter))
	if err != nil {
		return err
	}
	subject, err := ParseSubject(string(report.Subject))
	if err != nil {
		return err
	}
	if reporter == subject {
		return ErrInvalidReport
	}
	if _, err := ParseCharterVersion(string(report.Charter)); err != nil {
		return err
	}
	if !isDigest(report.Evidence) {
		return ErrUnprovenAccusation
	}
	return nil
}

// checkOpenMandate binds the opener to its mandate: the
// mandate holder is the inquisitor and the opening instant
// falls inside the issued window.
func checkOpenMandate(req OpenRequest) error {
	if req.Mandate.Holder != req.Inquisitor {
		return ErrExpiredMandate
	}
	opened := req.OpenedAt.UTC()
	if opened.Before(req.Mandate.IssuedAt.UTC()) {
		return ErrExpiredMandate
	}
	if !opened.Before(req.Mandate.ExpiresAt.UTC()) {
		return ErrExpiredMandate
	}
	return nil
}

// checkOpenDistinctness refuses one holder accumulating
// offices: inquisitor, arbiter and auditor are three distinct
// holders.
func checkOpenDistinctness(req OpenRequest) error {
	if req.Inquisitor == req.Arbiter || req.Inquisitor == req.Auditor || req.Arbiter == req.Auditor {
		return ErrSelfReview
	}
	return nil
}

// checkOpenImpartiality refuses interested prosecution: the
// opener is never the reporter or the accused and never
// belongs to the interested set, and the arbiter and the
// auditor never belong to it and never coincide with the
// parties.
func checkOpenImpartiality(req OpenRequest) error {
	if req.Inquisitor == req.Report.Reporter || req.Inquisitor == req.Report.Subject {
		return ErrInterestedProsecution
	}
	if containsSubject(req.Inquisitor, req.Interested) {
		return ErrInterestedProsecution
	}
	for _, judge := range []Subject{req.Arbiter, req.Auditor} {
		if judge == req.Report.Reporter || judge == req.Report.Subject {
			return ErrInterestedProsecution
		}
		if containsSubject(judge, req.Interested) {
			return ErrInterestedProsecution
		}
	}
	return nil
}

// checkOpenThrone refuses an interested King seating the
// severe panel: when the reigning holder is the reporter
// or the accused, or belongs to the interested set, it
// decides nothing in that proceeding.
func checkOpenThrone(req OpenRequest) error {
	interested := req.King == req.Report.Reporter || req.King == req.Report.Subject
	if !interested {
		interested = containsSubject(req.King, req.Interested)
	}
	if !interested {
		return nil
	}
	if req.King == req.Inquisitor || req.King == req.Arbiter || req.King == req.Auditor {
		return ErrConflictedThrone
	}
	return nil
}

// checkOpenDuplicate refuses a second opening of the same
// case identity or of the same report.
func checkOpenDuplicate(req OpenRequest) error {
	if containsCase(req.ID, req.ExistingIDs) {
		return ErrDuplicateCase
	}
	if containsReport(req.Report.ID, req.ExistingReports) {
		return ErrDuplicateCase
	}
	return nil
}

// OpenSevereCase opens one impartial severe proceeding from
// a proven report. Every refusal arrives before any effect:
// unevidenced reports, interested prosecution, self-review,
// an interested King deciding, duplicates, expired mandates
// and unpublished competence or deadline all stop here.
func OpenSevereCase(req OpenRequest) (SevereCase, error) {
	if err := checkOpenIdentity(req); err != nil {
		return SevereCase{}, err
	}
	if err := checkOpenReport(req.Report); err != nil {
		return SevereCase{}, err
	}
	if err := checkOpenMandate(req); err != nil {
		return SevereCase{}, err
	}
	if err := checkOpenDistinctness(req); err != nil {
		return SevereCase{}, err
	}
	if err := checkOpenThrone(req); err != nil {
		return SevereCase{}, err
	}
	if err := checkOpenImpartiality(req); err != nil {
		return SevereCase{}, err
	}
	if err := checkOpenDuplicate(req); err != nil {
		return SevereCase{}, err
	}
	return SevereCase{
		ID: req.ID, Report: req.Report.ID, Accused: req.Report.Subject,
		Inquisitor: req.Inquisitor, Arbiter: req.Arbiter, Auditor: req.Auditor,
		King: req.King, Interested: req.Interested, Competence: req.Competence,
		OpenedAt: req.OpenedAt.UTC(), Deadline: req.Deadline.UTC(),
	}, nil
}

// ReviewSuccessionEligibility judges whether reviewer may
// review the eligibility of candidate for the throne.
// Denunciation alone never blocks: a filed report without
// a conviction keeps the candidate eligible, so this check
// takes no report and refuses only a conflicted reviewer.
// An interested King never reviews a rival, and nobody
// reviews its own eligibility.
func ReviewSuccessionEligibility(candidate, reviewer, king Subject, interested []Subject) error {
	if _, err := ParseSubject(string(candidate)); err != nil {
		return err
	}
	if _, err := ParseSubject(string(reviewer)); err != nil {
		return err
	}
	if _, err := ParseSubject(string(king)); err != nil {
		return err
	}
	for _, party := range interested {
		if _, err := ParseSubject(string(party)); err != nil {
			return err
		}
	}
	if reviewer == candidate {
		return ErrSelfReview
	}
	if reviewer == king && containsSubject(king, interested) {
		return ErrConflictedThrone
	}
	return nil
}
