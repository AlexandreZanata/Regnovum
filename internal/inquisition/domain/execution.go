package domain

import (
	"time"
)

// OfficeCarrasco names the office that alone performs the digital
// execution. The office travels by value: this module never imports
// the crown, and any other executor is refused.
const OfficeCarrasco = "carrasco"

// ExecutionID identifies one authorized digital execution.
type ExecutionID string

// ParseExecutionID validates one execution token.
func ParseExecutionID(raw string) (ExecutionID, error) {
	token, err := parseToken(raw)
	if err != nil {
		return "", ErrInvalidReport
	}
	return ExecutionID(token), nil
}

// String returns the stored execution value.
func (e ExecutionID) String() string { return string(e) }

// PardonID identifies one royal pardon.
type PardonID string

// ParsePardonID validates one pardon token.
func ParsePardonID(raw string) (PardonID, error) {
	token, err := parseToken(raw)
	if err != nil {
		return "", ErrInvalidReport
	}
	return PardonID(token), nil
}

// String returns the stored pardon value.
func (p PardonID) String() string { return string(p) }

// Execution is one authorized digital execution of a severe
// sentence: which sentence and case it answers, who answers, who
// authorized it, the window the carrasco act must meet, who
// performed it and when, and whether a royal pardon or a lapse
// stopped it. It moves no value: liquidation belongs to a later
// phase, and other sanctions stand whatever happens here.
type Execution struct {
	ID           ExecutionID
	Sentence     SentenceID
	Case         CaseID
	Accused      Subject
	Authority    Subject
	AuthorizedAt time.Time
	Deadline     time.Time
	Executor     Subject
	ExecutedAt   time.Time
	Pardon       PardonID
	Granter      Subject
	Executed     bool
	Lapsed       bool
	Pardoned     bool
}

// Pardon is one royal pardon before the digital act: which
// sentence and case it spares, who answers, who grants it as
// royal, why, and when. It stops only the execution: the
// sentence and its other sanctions remain.
type Pardon struct {
	ID        PardonID
	Sentence  SentenceID
	Case      CaseID
	Accused   Subject
	Granter   Subject
	Detail    string
	GrantedAt time.Time
}

// AuthorizeRequest carries one execution authorization: the new
// execution identity, the sentence it answers, who authorizes,
// when, and the authorized window with the longest span the
// caller allows.
type AuthorizeRequest struct {
	ID        ExecutionID
	Sentence  Sentence
	Authority Subject
	At        time.Time
	Window    time.Duration
	MaxWindow time.Duration
}

// PardonRequest carries one royal pardon: the sentence it
// spares, who grants it as royal, why, and when.
type PardonRequest struct {
	ID       PardonID
	Sentence Sentence
	Granter  Subject
	Detail   string
	At       time.Time
}

// PerformRequest carries one digital act: the authorized
// execution, who performs it in which office, and when.
type PerformRequest struct {
	Execution Execution
	Executor  Subject
	Office    string
	At        time.Time
}

// checkAuthorizeIdentity validates authorization tokens: a whole
// execution identity and authority, a live instant, and a
// positive window within the allowed cap.
func checkAuthorizeIdentity(req AuthorizeRequest) error {
	if _, err := ParseExecutionID(string(req.ID)); err != nil {
		return err
	}
	if _, err := ParseSubject(string(req.Authority)); err != nil {
		return err
	}
	if req.At.IsZero() {
		return ErrUntimelyExecution
	}
	if req.Window <= 0 || req.MaxWindow <= 0 || req.Window > req.MaxWindow {
		return ErrUntimelyExecution
	}
	return nil
}

// checkAuthorizeSentence binds the authorization to one decided
// sentence: whole sentence and holders, the authority never the
// accused, and the authorization strictly after the decision.
// The sentence sanction itself is untouched here.
func checkAuthorizeSentence(req AuthorizeRequest) error {
	if _, err := ParseSentenceID(string(req.Sentence.ID)); err != nil {
		return err
	}
	if _, err := ParseCaseID(string(req.Sentence.Case)); err != nil {
		return err
	}
	if _, err := ParseSubject(string(req.Sentence.Accused)); err != nil {
		return err
	}
	if _, err := ParseSubject(string(req.Sentence.Decider)); err != nil {
		return err
	}
	if req.Sentence.DecidedAt.IsZero() {
		return ErrInvalidReport
	}
	if req.Authority == req.Sentence.Accused {
		return ErrInterestedProsecution
	}
	if !req.At.UTC().After(req.Sentence.DecidedAt.UTC()) {
		return ErrUntimelyExecution
	}
	return nil
}

// AuthorizeExecution authorizes one digital execution inside a
// bounded window. Every refusal arrives before any effect: a
// shapeless order, an interested authority and an authorization
// that precedes the sentence all stop here.
func AuthorizeExecution(req AuthorizeRequest) (Execution, error) {
	if err := checkAuthorizeIdentity(req); err != nil {
		return Execution{}, err
	}
	if err := checkAuthorizeSentence(req); err != nil {
		return Execution{}, err
	}
	start := req.At.UTC()
	return Execution{
		ID: req.ID, Sentence: req.Sentence.ID, Case: req.Sentence.Case,
		Accused: req.Sentence.Accused, Authority: req.Authority,
		AuthorizedAt: start, Deadline: start.Add(req.Window),
	}, nil
}

// checkPardonIdentity validates pardon tokens: a whole pardon
// identity and granter, a stated reason, and a live instant.
func checkPardonIdentity(req PardonRequest) error {
	if _, err := ParsePardonID(string(req.ID)); err != nil {
		return err
	}
	if _, err := ParseSubject(string(req.Granter)); err != nil {
		return err
	}
	if _, err := parseText(req.Detail); err != nil {
		return ErrInvalidReport
	}
	if req.At.IsZero() {
		return ErrInvalidReport
	}
	return nil
}

// checkPardonSentence binds the pardon to one decided sentence:
// whole sentence and holders, the granter never the accused and
// never the decider, and the grant strictly after the decision.
// The granter travels named: an anonymous pardon pardons
// nothing.
func checkPardonSentence(req PardonRequest) error {
	if _, err := ParseSentenceID(string(req.Sentence.ID)); err != nil {
		return err
	}
	if _, err := ParseCaseID(string(req.Sentence.Case)); err != nil {
		return err
	}
	if _, err := ParseSubject(string(req.Sentence.Accused)); err != nil {
		return err
	}
	if _, err := ParseSubject(string(req.Sentence.Decider)); err != nil {
		return err
	}
	if req.Sentence.DecidedAt.IsZero() {
		return ErrInvalidReport
	}
	if req.Granter == req.Sentence.Accused {
		return ErrInterestedProsecution
	}
	if req.Granter == req.Sentence.Decider {
		return ErrSelfReview
	}
	if !req.At.UTC().After(req.Sentence.DecidedAt.UTC()) {
		return ErrPardonedExecution
	}
	return nil
}

// GrantPardon grants one royal pardon for a decided sentence.
// The pardon stops only a later digital act: it rewrites no
// facts and lifts no other sanction.
func GrantPardon(req PardonRequest) (Pardon, error) {
	if err := checkPardonIdentity(req); err != nil {
		return Pardon{}, err
	}
	if err := checkPardonSentence(req); err != nil {
		return Pardon{}, err
	}
	return Pardon{
		ID: req.ID, Sentence: req.Sentence.ID, Case: req.Sentence.Case,
		Accused: req.Sentence.Accused, Granter: req.Granter,
		Detail: req.Detail, GrantedAt: req.At.UTC(),
	}, nil
}

// checkPardonBinding binds the pardon to the execution it
// spares: the same sentence, case and accused, granted before
// the execution deadline. A pardon for another fact spares
// nothing here.
func checkPardonBinding(exec Execution, pardon Pardon) error {
	if pardon.Sentence != exec.Sentence || pardon.Case != exec.Case {
		return ErrInvalidReport
	}
	if pardon.Accused != exec.Accused {
		return ErrInvalidReport
	}
	if pardon.GrantedAt.IsZero() {
		return ErrInvalidReport
	}
	if !pardon.GrantedAt.UTC().Before(exec.Deadline.UTC()) {
		return ErrUntimelyExecution
	}
	return nil
}

// ApplyPardon blocks one pending execution with a royal pardon
// granted before the act. An identical replay returns the same
// blocked state; a divergent pardon over a blocked order, a
// pardon over an executed order, and a pardon over a lapsed
// order all refuse: one order reaches one terminal state.
func ApplyPardon(exec Execution, pardon Pardon) (Execution, error) {
	if exec.Executed {
		return Execution{}, ErrDuplicateExecution
	}
	if exec.Lapsed {
		return Execution{}, ErrUntimelyExecution
	}
	if exec.Pardoned {
		if exec.Pardon == pardon.ID && exec.Granter == pardon.Granter {
			return exec, nil
		}
		return Execution{}, ErrDuplicateExecution
	}
	if err := checkPardonBinding(exec, pardon); err != nil {
		return Execution{}, err
	}
	exec.Pardoned = true
	exec.Pardon = pardon.ID
	exec.Granter = pardon.Granter
	return exec, nil
}

// checkPerformIdentity validates the digital act shape: a whole
// executor, the carrasco office, and a live instant. Any other
// office stops here as a missing executioner.
func checkPerformIdentity(req PerformRequest) error {
	if _, err := ParseSubject(string(req.Executor)); err != nil {
		return ErrMissingExecutioner
	}
	if req.Office != OfficeCarrasco {
		return ErrMissingExecutioner
	}
	if req.At.IsZero() {
		return ErrUntimelyExecution
	}
	return nil
}

// checkPerformStanding refuses an act over a terminal order: a
// pardoned order, a lapsed order, and a second act over an
// executed order. An identical retry returns the same executed
// state before this check runs.
func checkPerformStanding(exec Execution) error {
	if exec.Pardoned {
		return ErrPardonedExecution
	}
	if exec.Lapsed {
		return ErrUntimelyExecution
	}
	if exec.Executed {
		return ErrDuplicateExecution
	}
	return nil
}

// checkPerformWindow binds the act to the authorized window:
// at or after the authorization and strictly before the
// deadline. The deadline is exclusive: at the exact tick the
// penalty already lapses.
func checkPerformWindow(exec Execution, at time.Time) error {
	instant := at.UTC()
	if instant.Before(exec.AuthorizedAt.UTC()) {
		return ErrUntimelyExecution
	}
	if !instant.Before(exec.Deadline.UTC()) {
		return ErrUntimelyExecution
	}
	return nil
}

// checkPerformSeparation keeps the act disinterested: the
// executor is never the accused.
func checkPerformSeparation(exec Execution, executor Subject) error {
	if executor == exec.Accused {
		return ErrInterestedProsecution
	}
	return nil
}

// PerformExecution performs one digital act inside the
// authorized window with a named carrasco. A retry with the
// same executor and instant returns the same executed state; a
// divergent second act, a pardoned order, and an act at or past
// the deadline all refuse.
func PerformExecution(req PerformRequest) (Execution, error) {
	exec := req.Execution
	if exec.Executed {
		if req.Executor == exec.Executor && req.At.UTC().Equal(exec.ExecutedAt.UTC()) && req.Office == OfficeCarrasco {
			return exec, nil
		}
		return Execution{}, ErrDuplicateExecution
	}
	if err := checkPerformIdentity(req); err != nil {
		return Execution{}, err
	}
	if err := checkPerformStanding(exec); err != nil {
		return Execution{}, err
	}
	if err := checkPerformWindow(exec, req.At); err != nil {
		return Execution{}, err
	}
	if err := checkPerformSeparation(exec, req.Executor); err != nil {
		return Execution{}, err
	}
	exec.Executor = req.Executor
	exec.ExecutedAt = req.At.UTC()
	exec.Executed = true
	return exec, nil
}

// ExpireExecution lapses one pending execution past its
// deadline without any act. A lapsed order executes nothing and
// a pardoned order lapses into nothing: other sanctions stand.
// A lapsed replay returns the same lapsed state.
func ExpireExecution(exec Execution, now time.Time) (Execution, error) {
	if exec.Executed {
		return Execution{}, ErrDuplicateExecution
	}
	if exec.Pardoned {
		return exec, nil
	}
	if exec.Lapsed {
		return exec, nil
	}
	if now.IsZero() {
		return Execution{}, ErrUntimelyExecution
	}
	if now.UTC().Before(exec.Deadline.UTC()) {
		return Execution{}, ErrUntimelyExecution
	}
	exec.Lapsed = true
	return exec, nil
}
