package domain

import "fmt"

// ErrorCode is a stable machine-readable identifier for domain rule violations.
type ErrorCode string

const (
	// CodeInvalidSeason names a malformed season or manifesto: a
	// blank or abusive token, a zero ordinal, a missing instant, a
	// broken hash or a window the calendar cannot hold.
	CodeInvalidSeason ErrorCode = "SEASONS_INVALID_SEASON"
	// CodeInvalidTransition names a state move outside the staged
	// machine: only one step forward, never back, never a jump,
	// never a reopen.
	CodeInvalidTransition ErrorCode = "SEASONS_INVALID_TRANSITION"
	// CodeStaleClock names an instant behind the season record: the
	// clock never runs backwards, and downtime never fills lapsed
	// seasons by itself.
	CodeStaleClock ErrorCode = "SEASONS_STALE_CLOCK"
)

// DomainError represents an invariant or rule failure in the seasons domain.
type DomainError struct {
	Code    ErrorCode
	Message string
}

func (e DomainError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e DomainError) Is(target error) bool {
	t, ok := target.(DomainError)
	if !ok {
		return false
	}
	return e.Code == t.Code
}

var (
	// ErrInvalidSeason refuses a season that cannot seal: abusive
	// tokens, a zero ordinal, a missing start, a date overflow or a
	// manifesto whose hash no longer agrees.
	ErrInvalidSeason = DomainError{
		Code:    CodeInvalidSeason,
		Message: "season needs an id, an ordinal from 1, a UTC start, a charter version, a policy reference and a sealed manifesto",
	}
	// ErrInvalidTransition refuses a move outside the staged
	// machine: PREPARED/ACTIVE/CLOSING/SEALED/ARCHIVED advance one
	// step forward only.
	ErrInvalidTransition = DomainError{
		Code:    CodeInvalidTransition,
		Message: "season states advance one step forward only: prepared, active, closing, sealed, archived",
	}
	// ErrStaleClock refuses an instant behind the season record:
	// transitions and plans move with the clock, never against it.
	ErrStaleClock = DomainError{
		Code:    CodeStaleClock,
		Message: "season instants never run backwards: transitions carry a live instant at or past the record",
	}
)
