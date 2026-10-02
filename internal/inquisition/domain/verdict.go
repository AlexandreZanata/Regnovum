package domain

import (
	"time"
)

// SanctionKind is the closed vocabulary of severe sanctions:
// warning, temporary restriction and temporary suspension.
// Every sanction is motivated and temporary; later phases
// detail execution, never widen this list by inference.
type SanctionKind string

const (
	// SanctionWarning names a severe warning.
	SanctionWarning SanctionKind = "advertencia"
	// SanctionRestriction names a temporary restriction.
	SanctionRestriction SanctionKind = "restricao"
	// SanctionSuspension names a temporary suspension.
	SanctionSuspension SanctionKind = "suspensao"
)

// ParseSanctionKind validates a sanction token. Matching is
// exact: no trimming, no case folding, no inference.
func ParseSanctionKind(raw string) (SanctionKind, error) {
	switch SanctionKind(raw) {
	case SanctionWarning, SanctionRestriction, SanctionSuspension:
		return SanctionKind(raw), nil
	default:
		return "", ErrOutOfCompetence
	}
}

// String returns the stored sanction value.
func (s SanctionKind) String() string { return string(s) }

// SentenceID identifies one severe sentence.
type SentenceID string

// ParseSentenceID validates one sentence token.
func ParseSentenceID(raw string) (SentenceID, error) {
	token, err := parseToken(raw)
	if err != nil {
		return "", ErrInvalidReport
	}
	return SentenceID(token), nil
}

// String returns the stored sentence value.
func (s SentenceID) String() string { return string(s) }

// AppealID identifies one appeal.
type AppealID string

// ParseAppealID validates one appeal token.
func ParseAppealID(raw string) (AppealID, error) {
	token, err := parseToken(raw)
	if err != nil {
		return "", ErrInvalidReport
	}
	return AppealID(token), nil
}

// String returns the stored appeal value.
func (a AppealID) String() string { return string(a) }

// Sentence is one motivated severe decision: which case and
// accused, which closed sanction under the case competence,
// under which charter version in force at the fact, over
// which fact digest, why, by whom and when. A conviction
// never rewrites the debated facts: the digest repeats the
// sealed proof, and proportionality travels in the recorded
// motive for the distinct reviewer to judge.
type Sentence struct {
	ID         SentenceID
	Case       CaseID
	Accused    Subject
	Sanction   SanctionKind
	Competence string
	Charter    CharterVersion
	FactDigest string
	Motive     string
	Decider    Subject
	DecidedAt  time.Time
}

// SentenceRequest carries one sentencing request: the case
// with its published competence, the report in force at the
// fact, the sealed envelope, the fulfilled sealed phase, the
// defense disclosure when the phase is sealed, and the
// decision itself.
type SentenceRequest struct {
	ID            SentenceID
	Case          SevereCase
	Report        Report
	Envelope      SealedEnvelope
	HasSeal       bool
	Phase         SealedPhase
	Disclosure    DisclosureView
	HasDisclosure bool
	Decider       Subject
	SanctionRaw   string
	Competence    string
	CharterRaw    string
	FactDigest    string
	Motive        string
	DecidedAt     time.Time
}

// checkSentenceIdentity validates sentencing tokens: whole
// identifiers and holders, the case binding, a stated
// motive and a live decision instant.
func checkSentenceIdentity(req SentenceRequest) error {
	if _, err := ParseSentenceID(string(req.ID)); err != nil {
		return err
	}
	if _, err := ParseCaseID(string(req.Case.ID)); err != nil {
		return err
	}
	if _, err := ParseSubject(string(req.Case.Accused)); err != nil {
		return err
	}
	if _, err := ParseSubject(string(req.Decider)); err != nil {
		return err
	}
	if _, err := parseText(req.Motive); err != nil {
		return ErrInvalidReport
	}
	if _, err := parseText(req.Competence); err != nil {
		return ErrOutOfCompetence
	}
	if req.DecidedAt.IsZero() {
		return ErrInvalidReport
	}
	return nil
}

// checkSentencePhase binds the sentence to the fulfilled
// sealed phase of the same case: the phase decision names
// this case. A bare case with neither seal nor waiver never
// reaches a sentence.
func checkSentencePhase(req SentenceRequest) error {
	if req.Phase.Case != req.Case.ID {
		return ErrUnsealedPhase
	}
	return nil
}

// checkSentenceDefense requires the defense before a sealed
// sentence: when the phase carries a seal, the disclosure
// names the same case and digest and precedes the decision.
// A motivated waiver excuses the disclosure, never the
// phase.
func checkSentenceDefense(req SentenceRequest) error {
	if req.Phase.Waived {
		return nil
	}
	if !req.HasDisclosure {
		return ErrDefenselessSentence
	}
	if req.Disclosure.Case != req.Case.ID {
		return ErrDefenselessSentence
	}
	if req.Disclosure.Digest != req.Envelope.Digest {
		return ErrDefenselessSentence
	}
	if req.Disclosure.RevealedAt.IsZero() {
		return ErrDefenselessSentence
	}
	if req.DecidedAt.UTC().Before(req.Disclosure.RevealedAt.UTC()) {
		return ErrDefenselessSentence
	}
	return nil
}

// checkSentenceFacts binds the sentence to the debated
// facts and the contemporary rule: the fact digest repeats
// the sealed proof, or the report proof under a motivated
// waiver, and the charter repeats the version in force at
// the fact. A conviction rewrites nothing.
func checkSentenceFacts(req SentenceRequest) error {
	if _, err := ParseCharterVersion(req.CharterRaw); err != nil {
		return err
	}
	if _, err := ParseCharterVersion(string(req.Report.Charter)); err != nil {
		return err
	}
	if CharterVersion(req.CharterRaw) != req.Report.Charter {
		return ErrCharterMismatch
	}
	if !isDigest(req.FactDigest) {
		return ErrTamperedEvidence
	}
	if req.Phase.Waived {
		if req.FactDigest != req.Report.Evidence {
			return ErrTamperedEvidence
		}
		return nil
	}
	if req.FactDigest != req.Envelope.Digest {
		return ErrTamperedEvidence
	}
	return nil
}

// checkSentenceSanction binds the sanction to the published
// competence: a closed sanction under the very competence
// of the case. Unknown sanctions and divorced competences
// stop here; proportionality lives in the recorded motive.
func checkSentenceSanction(req SentenceRequest) error {
	sanction, err := ParseSanctionKind(req.SanctionRaw)
	if err != nil {
		return err
	}
	if req.Competence != req.Case.Competence {
		return ErrOutOfCompetence
	}
	_ = sanction
	return nil
}

// DecideSentence issues one motivated severe sentence. Every
// refusal arrives before any effect: defenseless sealed
// cases, rewritten facts, wrong charter versions and
// sanctions outside the published competence all stop here.
func DecideSentence(req SentenceRequest) (Sentence, error) {
	if err := checkSentenceIdentity(req); err != nil {
		return Sentence{}, err
	}
	if err := checkSentencePhase(req); err != nil {
		return Sentence{}, err
	}
	if err := checkSentenceDefense(req); err != nil {
		return Sentence{}, err
	}
	if err := checkSentenceFacts(req); err != nil {
		return Sentence{}, err
	}
	if err := checkSentenceSanction(req); err != nil {
		return Sentence{}, err
	}
	sanction, _ := ParseSanctionKind(req.SanctionRaw)
	return Sentence{
		ID: req.ID, Case: req.Case.ID, Accused: req.Case.Accused,
		Sanction: sanction, Competence: req.Competence,
		Charter: CharterVersion(req.CharterRaw), FactDigest: req.FactDigest,
		Motive: req.Motive, Decider: req.Decider,
		DecidedAt: req.DecidedAt.UTC(),
	}, nil
}

// Appeal is one timely appeal: which sentence and case, who
// appeals, who reviews distinctly, when it arrives and the
// deadline it met.
type Appeal struct {
	ID        AppealID
	Sentence  SentenceID
	Case      CaseID
	Appellant Subject
	Reviewer  Subject
	FiledAt   time.Time
	Deadline  time.Time
}

// AppealRequest carries one appeal filing: the sentence
// under review, who appeals and who reviews, when it
// arrives, and the appeal window with the longest span the
// caller allows.
type AppealRequest struct {
	ID        AppealID
	Sentence  Sentence
	Appellant Subject
	Reviewer  Subject
	FiledAt   time.Time
	Window    time.Duration
	MaxWindow time.Duration
}

// checkAppealIdentity validates appeal tokens: whole appeal
// and sentence, whole holders, and a well-shaped window.
func checkAppealIdentity(req AppealRequest) error {
	if _, err := ParseAppealID(string(req.ID)); err != nil {
		return err
	}
	if _, err := ParseSentenceID(string(req.Sentence.ID)); err != nil {
		return err
	}
	if _, err := ParseSubject(string(req.Appellant)); err != nil {
		return err
	}
	if _, err := ParseSubject(string(req.Reviewer)); err != nil {
		return err
	}
	if req.FiledAt.IsZero() {
		return ErrUntimelyAppeal
	}
	if req.Window <= 0 || req.MaxWindow <= 0 || req.Window > req.MaxWindow {
		return ErrUntimelyAppeal
	}
	return nil
}

// FileAppeal files one appeal before the published deadline
// with a distinct reviewer. The reviewer is never the
// decider and never the appellant; filings before the
// sentence or past the deadline refuse as untimely.
func FileAppeal(req AppealRequest) (Appeal, error) {
	if err := checkAppealIdentity(req); err != nil {
		return Appeal{}, err
	}
	if req.Reviewer == req.Sentence.Decider || req.Reviewer == req.Appellant {
		return Appeal{}, ErrSelfReview
	}
	deadline := req.Sentence.DecidedAt.UTC().Add(req.Window)
	filed := req.FiledAt.UTC()
	if !filed.After(req.Sentence.DecidedAt.UTC()) || filed.After(deadline) {
		return Appeal{}, ErrUntimelyAppeal
	}
	return Appeal{
		ID: req.ID, Sentence: req.Sentence.ID, Case: req.Sentence.Case,
		Appellant: req.Appellant, Reviewer: req.Reviewer,
		FiledAt: filed, Deadline: deadline,
	}, nil
}
