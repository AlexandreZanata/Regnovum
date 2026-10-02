package domain

import (
	"strings"
	"time"
)

// EmergencyReviewWindow bounds the independent review of a
// preventive suspension: the reviewer confirms or reverts
// within 24h of the imposition, or the measure lapses. It
// is a policy constant, not configuration: without a
// deadline a preventive measure could pose as a final one,
// and with a per-case deadline reviewers could not plan.
const EmergencyReviewWindow = 24 * time.Hour

// OfficeJusticeiro names the crown office that alone hides
// a severe-case piece in concrete urgency. The office
// travels by value: this module never imports the crown,
// and any other concealer is refused.
const OfficeJusticeiro = "justiceiro"

// SevereMeasureKind names which severe measure applies. The
// first two are emergency: they contain without judging,
// and they never read as final guilt. Quarantine is final:
// it follows a conviction only, under the ratified cap.
type SevereMeasureKind string

const (
	// MeasureConcealment hides one piece of the accused.
	MeasureConcealment SevereMeasureKind = "ocultacao-peca"
	// MeasurePreventiveSuspension suspends the accused
	// account until the independent review.
	MeasurePreventiveSuspension SevereMeasureKind = "suspensao-preventiva"
	// MeasureQuarantine holds the convicted account after
	// the sentence, within the ratified cap.
	MeasureQuarantine SevereMeasureKind = "quarentena-pos-condenacao"
)

// IsEmergency reports whether the kind contains without
// judging: concealment and preventive suspension only.
func (k SevereMeasureKind) IsEmergency() bool {
	return k == MeasureConcealment || k == MeasurePreventiveSuspension
}

// parseSevereCaseRef validates the severe case reference: a
// non-blank stable identifier chosen by the issuer.
func parseSevereCaseRef(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > MaxRuleLength {
		return "", ErrInvalidRule
	}
	return trimmed, nil
}

// Concealment is one hidden severe-case piece: which case
// and piece, whose piece, who hid it in which concrete
// urgency and when, and whether an independent reviewer
// already restored it.
type Concealment struct {
	ID        string
	CaseRef   string
	PieceID   string
	Owner     AccountID
	Accused   AccountID
	Concealer string
	Urgency   string
	ImposedAt time.Time
	Reverted  bool
}

// IsFinal reports whether the concealment reads as final
// guilt: never, it only hides.
func (c Concealment) IsFinal() bool { return false }

// ConcealRequest carries one concealment order: which piece
// of which case and whose, who hides it in which concrete
// urgency and when.
type ConcealRequest struct {
	ID        string
	CaseRef   string
	PieceID   string
	Owner     AccountID
	Accused   AccountID
	Concealer string
	Urgency   string
	At        time.Time
}

// ConcealPiece hides one piece of the accused in concrete
// urgency. Only the justiceiro hides, only with a stated
// urgency, and only the accused piece: third-party content
// stops here.
func ConcealPiece(req ConcealRequest) (Concealment, error) {
	if strings.TrimSpace(req.ID) == "" || strings.TrimSpace(req.PieceID) == "" {
		return Concealment{}, ErrEmptyTargetID
	}
	ref, err := parseSevereCaseRef(req.CaseRef)
	if err != nil {
		return Concealment{}, err
	}
	if req.Owner.IsZero() || req.Accused.IsZero() {
		return Concealment{}, ErrEmptyAccountID
	}
	if req.Owner != req.Accused {
		return Concealment{}, ErrThirdPartyTarget
	}
	if req.Concealer != OfficeJusticeiro {
		return Concealment{}, ErrRoleNotAuthorized
	}
	motive, err := ParseJustification(req.Urgency)
	if err != nil {
		return Concealment{}, err
	}
	if req.At.IsZero() {
		return Concealment{}, ErrInvalidExpiry
	}
	return Concealment{
		ID: req.ID, CaseRef: ref, PieceID: strings.TrimSpace(req.PieceID),
		Owner: req.Owner, Accused: req.Accused, Concealer: req.Concealer,
		Urgency: motive, ImposedAt: req.At.UTC(),
	}, nil
}

// ReverseConcealment restores one hidden piece by
// independent review: the reviewer is never the concealer.
func ReverseConcealment(c Concealment, reviewer string) (Concealment, error) {
	if strings.TrimSpace(reviewer) == "" {
		return Concealment{}, ErrEmptyAccountID
	}
	if reviewer == c.Concealer {
		return Concealment{}, ErrConflictOfInterest
	}
	c.Reverted = true
	return c, nil
}

// PreventiveSuspension is one suspended accused account
// awaiting independent review: who imposed it, why, when,
// the deadline the reviewer meets, and whether it was
// confirmed or reverted.
type PreventiveSuspension struct {
	ID             string
	CaseRef        string
	Account        AccountID
	Accused        AccountID
	Imposer        string
	Motive         string
	ImposedAt      time.Time
	ReviewDeadline time.Time
	Reviewed       bool
	Reverted       bool
}

// IsFinal reports whether the suspension reads as final
// guilt: never, it only waits for review.
func (s PreventiveSuspension) IsFinal() bool { return false }

// SuspendRequest carries one preventive suspension order:
// whose account of which case, who imposes it, why and
// when.
type SuspendRequest struct {
	ID      string
	CaseRef string
	Account AccountID
	Accused AccountID
	Imposer string
	Motive  string
	At      time.Time
}

// SuspendPreventively suspends the accused account with a
// stated motive and a 24h review deadline. Third-party
// accounts stop here.
func SuspendPreventively(req SuspendRequest) (PreventiveSuspension, error) {
	if strings.TrimSpace(req.ID) == "" {
		return PreventiveSuspension{}, ErrEmptyTargetID
	}
	ref, err := parseSevereCaseRef(req.CaseRef)
	if err != nil {
		return PreventiveSuspension{}, err
	}
	if req.Account.IsZero() || req.Accused.IsZero() {
		return PreventiveSuspension{}, ErrEmptyAccountID
	}
	if req.Account != req.Accused {
		return PreventiveSuspension{}, ErrThirdPartyTarget
	}
	if strings.TrimSpace(req.Imposer) == "" {
		return PreventiveSuspension{}, ErrEmptyAccountID
	}
	reason, err := ParseJustification(req.Motive)
	if err != nil {
		return PreventiveSuspension{}, err
	}
	if req.At.IsZero() {
		return PreventiveSuspension{}, ErrInvalidExpiry
	}
	imposed := req.At.UTC()
	return PreventiveSuspension{
		ID: req.ID, CaseRef: ref, Account: req.Account, Accused: req.Accused,
		Imposer: strings.TrimSpace(req.Imposer), Motive: reason,
		ImposedAt: imposed, ReviewDeadline: imposed.Add(EmergencyReviewWindow),
	}, nil
}

// ReviewSuspension confirms or reverts one preventive
// suspension by independent review inside the 24h window.
// A review past the exact deadline refuses: the measure
// already lapsed, and a lapsed measure reviews nothing.
func ReviewSuspension(s PreventiveSuspension, reviewer string, revert bool, at time.Time) (PreventiveSuspension, error) {
	if strings.TrimSpace(reviewer) == "" {
		return PreventiveSuspension{}, ErrEmptyAccountID
	}
	if reviewer == s.Imposer {
		return PreventiveSuspension{}, ErrConflictOfInterest
	}
	if at.IsZero() || !at.UTC().Before(s.ReviewDeadline) {
		return PreventiveSuspension{}, ErrReviewTimeout
	}
	s.Reviewed = true
	s.Reverted = revert
	return s, nil
}

// SuspensionActive reports whether the suspension stands at
// the instant: before the exact deadline and neither
// reverted nor lapsed. The deadline is exclusive: at the
// exact tick the measure already lapsed.
func SuspensionActive(s PreventiveSuspension, now time.Time) bool {
	if s.Reverted || now.IsZero() {
		return false
	}
	return now.UTC().Before(s.ReviewDeadline)
}

// Quarantine is one convicted account held after the
// sentence: which sentence convicted it, who imposed the
// hold, why, and the end inside the ratified cap.
type Quarantine struct {
	ID          string
	CaseRef     string
	Account     AccountID
	Accused     AccountID
	SentenceRef string
	Imposer     string
	Motive      string
	ImposedAt   time.Time
	EndsAt      time.Time
}

// IsFinal reports whether the quarantine follows final
// guilt: always, it exists only after a conviction.
func (q Quarantine) IsFinal() bool { return true }

// QuarantineRequest carries one post-conviction hold order:
// whose account of which case, which prior sentence, who
// imposes it, why, when it starts and ends, and the
// ratified cap it respects.
type QuarantineRequest struct {
	ID            string
	CaseRef       string
	Account       AccountID
	Accused       AccountID
	SentenceRef   string
	SentencedAt   time.Time
	Imposer       string
	Motive        string
	At            time.Time
	EndsAt        time.Time
	MaxQuarantine time.Duration
}

// QuarantineAfterConviction holds the convicted account
// after the sentence inside the ratified cap. Without a
// prior sentence there is no quarantine, and past the cap
// there is no hold: the cap travels by parameter, never by
// inference.
func QuarantineAfterConviction(req QuarantineRequest) (Quarantine, error) {
	if strings.TrimSpace(req.ID) == "" {
		return Quarantine{}, ErrEmptyTargetID
	}
	ref, err := parseSevereCaseRef(req.CaseRef)
	if err != nil {
		return Quarantine{}, err
	}
	if req.Account.IsZero() || req.Accused.IsZero() {
		return Quarantine{}, ErrEmptyAccountID
	}
	if req.Account != req.Accused {
		return Quarantine{}, ErrThirdPartyTarget
	}
	if strings.TrimSpace(req.SentenceRef) == "" || req.SentencedAt.IsZero() {
		return Quarantine{}, ErrMissingConviction
	}
	if strings.TrimSpace(req.Imposer) == "" {
		return Quarantine{}, ErrEmptyAccountID
	}
	reason, err := ParseJustification(req.Motive)
	if err != nil {
		return Quarantine{}, err
	}
	if req.At.IsZero() || req.EndsAt.IsZero() {
		return Quarantine{}, ErrInvalidExpiry
	}
	imposed := req.At.UTC()
	if req.SentencedAt.UTC().After(imposed) {
		return Quarantine{}, ErrMissingConviction
	}
	end := req.EndsAt.UTC()
	if !end.After(imposed) {
		return Quarantine{}, ErrInvalidExpiry
	}
	if req.MaxQuarantine <= 0 || end.Sub(imposed) > req.MaxQuarantine {
		return Quarantine{}, ErrExcessiveQuarantine
	}
	return Quarantine{
		ID: req.ID, CaseRef: ref, Account: req.Account, Accused: req.Accused,
		SentenceRef: strings.TrimSpace(req.SentenceRef),
		Imposer:     strings.TrimSpace(req.Imposer), Motive: reason,
		ImposedAt: imposed, EndsAt: end,
	}, nil
}

// MeasureAppeal is one appeal against an emergency measure:
// which measure, who appeals, who reviews, which outcome
// and when. Only the affected account appeals, and the
// reviewer is never the imposer.
type MeasureAppeal struct {
	MeasureID string
	Appellant AccountID
	Reviewer  string
	Outcome   Outcome
	FiledAt   time.Time
}

// checkMeasureAppeal binds the appeal parties and outcome:
// the affected appeals, a stranger never does; the imposer
// never reviews its own measure; the outcome stays in the
// closed vocabulary.
func checkMeasureAppeal(measureID string, accused, appellant AccountID, imposer, reviewer string, outcome Outcome) error {
	if strings.TrimSpace(measureID) == "" {
		return ErrEmptyTargetID
	}
	if appellant.IsZero() || accused.IsZero() {
		return ErrEmptyAccountID
	}
	if appellant != accused {
		return ErrThirdPartyTarget
	}
	if strings.TrimSpace(reviewer) == "" {
		return ErrEmptyAccountID
	}
	if reviewer == imposer {
		return ErrConflictOfInterest
	}
	if _, err := ParseOutcome(string(outcome)); err != nil {
		return err
	}
	return nil
}

// AppealSuspension appeals one standing preventive
// suspension: the filing arrives before the exact review
// deadline. A reversed outcome lifts the suspension.
func AppealSuspension(s PreventiveSuspension, appellant AccountID, reviewer string, outcome Outcome, at time.Time) (MeasureAppeal, PreventiveSuspension, error) {
	if err := checkMeasureAppeal(s.ID, s.Accused, appellant, s.Imposer, reviewer, outcome); err != nil {
		return MeasureAppeal{}, PreventiveSuspension{}, err
	}
	if at.IsZero() || !at.UTC().Before(s.ReviewDeadline) {
		return MeasureAppeal{}, PreventiveSuspension{}, ErrReviewTimeout
	}
	if outcome == OutcomeReversed {
		s.Reviewed = true
		s.Reverted = true
	}
	return MeasureAppeal{
		MeasureID: s.ID, Appellant: appellant,
		Reviewer: reviewer, Outcome: outcome, FiledAt: at.UTC(),
	}, s, nil
}

// AppealConcealment appeals one hidden piece: the affected
// appeals, the concealer never reviews. A reversed outcome
// restores the piece.
func AppealConcealment(c Concealment, appellant AccountID, reviewer string, outcome Outcome, at time.Time) (MeasureAppeal, Concealment, error) {
	if err := checkMeasureAppeal(c.ID, c.Accused, appellant, c.Concealer, reviewer, outcome); err != nil {
		return MeasureAppeal{}, Concealment{}, err
	}
	if at.IsZero() {
		return MeasureAppeal{}, Concealment{}, ErrReviewTimeout
	}
	if outcome == OutcomeReversed {
		c.Reverted = true
	}
	return MeasureAppeal{
		MeasureID: c.ID, Appellant: appellant,
		Reviewer: reviewer, Outcome: outcome, FiledAt: at.UTC(),
	}, c, nil
}
