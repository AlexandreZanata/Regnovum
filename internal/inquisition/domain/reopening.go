package domain

import (
	"time"
)

// ReopenGround is the closed vocabulary of grounds that
// admit a new proceeding over the same fact: new material
// proof, procedural fraud and decisive error. A larger bond
// is absent on purpose: money alone never reopens.
type ReopenGround string

const (
	// GroundNewProof names new material proof over the fact.
	GroundNewProof ReopenGround = "prova-nova"
	// GroundProceduralFraud names fraud inside the proceeding.
	GroundProceduralFraud ReopenGround = "fraude-processual"
	// GroundDecisiveError names a decisive error in the case.
	GroundDecisiveError ReopenGround = "erro-decisivo"
)

// ParseReopenGround validates a ground token. Matching is
// exact: no trimming, no case folding, no inference. A
// larger bond and any other token refuse as insufficient
// ground, never as a fourth way in.
func ParseReopenGround(raw string) (ReopenGround, error) {
	switch ReopenGround(raw) {
	case GroundNewProof, GroundProceduralFraud, GroundDecisiveError:
		return ReopenGround(raw), nil
	default:
		return "", ErrInsufficientGround
	}
}

// String returns the stored ground value.
func (g ReopenGround) String() string { return string(g) }

// ReopeningID identifies one reopening decision.
type ReopeningID string

// ParseReopeningID validates one reopening token.
func ParseReopeningID(raw string) (ReopeningID, error) {
	token, err := parseToken(raw)
	if err != nil {
		return "", ErrInvalidReport
	}
	return ReopeningID(token), nil
}

// String returns the stored reopening value.
func (r ReopeningID) String() string { return string(r) }

// DecreeID identifies one exceptional royal decree.
type DecreeID string

// ParseDecreeID validates one decree token.
func ParseDecreeID(raw string) (DecreeID, error) {
	token, err := parseToken(raw)
	if err != nil {
		return "", ErrInvalidReport
	}
	return DecreeID(token), nil
}

// String returns the stored decree value.
func (d DecreeID) String() string { return string(d) }

// DeniedPlea records one denied plea: which ground was
// alleged and over which proof. The ground travels raw:
// the denial log records what was alleged, including a
// larger bond. Repetition is judged on both: the same
// ground with genuinely new proof is a new plea, not a
// replay.
type DeniedPlea struct {
	Ground string
	Digest string
}

// Reopening is one admitted new proceeding over the same
// fact: which case and sentence it revisits, on which of
// the three grounds, decided by which independent reviewer,
// with which new proof when the ground is new proof, why,
// and whether an exceptional decree ordered it. An
// exceptional decree is identified as royal: it travels
// named with its authority, never silently.
type Reopening struct {
	ID        ReopeningID
	Case      CaseID
	Sentence  SentenceID
	Ground    ReopenGround
	Reviewer  Subject
	NewDigest string
	Detail    string
	ByDecree  bool
	Decree    DecreeID
	Authority Subject
	DecidedAt time.Time
}

// ReopenRequest carries one reopening plea: the sentence it
// revisits, the ground with the new proof when the ground
// is new proof, who reviews, why, the denied pleas that
// refuse repetition, and the exceptional decree when one
// orders it.
type ReopenRequest struct {
	ID        ReopeningID
	Sentence  Sentence
	GroundRaw string
	Reviewer  Subject
	NewDigest string
	Detail    string
	Denied    []DeniedPlea
	ByDecree  bool
	DecreeRaw string
	Authority Subject
	DecidedAt time.Time
}

// checkReopenIdentity validates plea tokens: whole
// identifiers and reviewer, a stated reason and a live
// decision instant.
func checkReopenIdentity(req ReopenRequest) error {
	if _, err := ParseReopeningID(string(req.ID)); err != nil {
		return err
	}
	if _, err := ParseSentenceID(string(req.Sentence.ID)); err != nil {
		return err
	}
	if _, err := ParseSubject(string(req.Reviewer)); err != nil {
		return err
	}
	if _, err := parseText(req.Detail); err != nil {
		return ErrInvalidReport
	}
	if req.DecidedAt.IsZero() {
		return ErrInvalidReport
	}
	return nil
}

// checkReopenReviewer keeps the second look independent:
// the reviewer is never the decider of the sentence under
// review.
func checkReopenReviewer(req ReopenRequest) error {
	if req.Reviewer == req.Sentence.Decider {
		return ErrSelfReview
	}
	return nil
}

// checkReopenGround binds the plea to one of the three
// admitted grounds. New material proof arrives as a valid
// digest distinct from the sentenced facts; a larger bond
// and any other token stop here.
func checkReopenGround(req ReopenRequest) (ReopenGround, error) {
	ground, err := ParseReopenGround(req.GroundRaw)
	if err != nil {
		return "", err
	}
	if ground != GroundNewProof {
		return ground, nil
	}
	if !isDigest(req.NewDigest) {
		return "", ErrInsufficientGround
	}
	if req.NewDigest == req.Sentence.FactDigest {
		return "", ErrInsufficientGround
	}
	return ground, nil
}

// checkReopenRepetition refuses a plea already denied on the
// same alleged ground and proof: repetition decides nothing
// new and opens no new case. It judges the raw allegation
// before its merit: a replayed denial is a replay,
// whatever its merit would be.
func checkReopenRepetition(req ReopenRequest) error {
	for _, denied := range req.Denied {
		if denied.Ground == req.GroundRaw && denied.Digest == req.NewDigest {
			return ErrRepeatedPetition
		}
	}
	return nil
}

// checkReopenDecree binds an exceptional decree plea: when
// the plea invokes a decree, the decree arrives named with
// its authority. The decree is identified as royal on the
// admitted reopening, never silently.
func checkReopenDecree(req ReopenRequest) (DecreeID, error) {
	if !req.ByDecree {
		return "", nil
	}
	decree, err := ParseDecreeID(req.DecreeRaw)
	if err != nil {
		return "", err
	}
	if _, err := ParseSubject(string(req.Authority)); err != nil {
		return "", err
	}
	return decree, nil
}

// DecideReopening admits one new proceeding over the same
// fact on a sufficient ground with an independent reviewer.
// Every refusal arrives before any effect: interested
// review, repeated pleas, insufficient grounds and unnamed
// decrees all stop here, and twenty repeated pleas open no
// twenty cases.
func DecideReopening(req ReopenRequest) (Reopening, error) {
	if err := checkReopenIdentity(req); err != nil {
		return Reopening{}, err
	}
	if err := checkReopenReviewer(req); err != nil {
		return Reopening{}, err
	}
	if err := checkReopenRepetition(req); err != nil {
		return Reopening{}, err
	}
	ground, err := checkReopenGround(req)
	if err != nil {
		return Reopening{}, err
	}
	decree, err := checkReopenDecree(req)
	if err != nil {
		return Reopening{}, err
	}
	newDigest := ""
	if ground == GroundNewProof {
		newDigest = req.NewDigest
	}
	return Reopening{
		ID: req.ID, Case: req.Sentence.Case, Sentence: req.Sentence.ID,
		Ground: ground, Reviewer: req.Reviewer, NewDigest: newDigest,
		Detail: req.Detail, ByDecree: req.ByDecree,
		Decree: decree, Authority: req.Authority,
		DecidedAt: req.DecidedAt.UTC(),
	}, nil
}
