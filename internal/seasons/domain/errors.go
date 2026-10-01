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
	// CodeCloseNotDue names a closing barrier recorded before the
	// book end: the cutoff is the database clock at or past ends_at,
	// never anticipated.
	CodeCloseNotDue ErrorCode = "SEASONS_CLOSE_NOT_DUE"
	// CodeCloseBlocked names a seal refused by open obligations: an
	// indecisive contract, an active hold or a diverged snapshot
	// preserves custody instead of choosing a destination.
	CodeCloseBlocked ErrorCode = "SEASONS_CLOSE_BLOCKED"
	// CodeStaleGeneration names a write from an older closing
	// generation: after a takeover only the new generation moves the
	// book.
	CodeStaleGeneration ErrorCode = "SEASONS_STALE_GENERATION"
	// CodeLeaseHeld names a takeover while another worker holds the
	// lease: the book changes hands only past leased_until on the
	// database clock.
	CodeLeaseHeld ErrorCode = "SEASONS_LEASE_HELD"
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
	// ErrCloseNotDue refuses a closing barrier before the book end:
	// the cutoff is observed on the database clock, never
	// anticipated.
	ErrCloseNotDue = DomainError{
		Code:    CodeCloseNotDue,
		Message: "closing starts at or past the book end: early barriers admit nothing and drain nothing",
	}
	// ErrCloseBlocked refuses a seal while obligations stay open: an
	// indecisive escrow, an active hold or a diverged snapshot keeps
	// custody instead of choosing a destination. Absence of data never
	// proves absence of litigation.
	ErrCloseBlocked = DomainError{
		Code:    CodeCloseBlocked,
		Message: "open obligations block the seal: custody is preserved, nothing is confiscated and no successor opens",
	}
	// ErrStaleGeneration refuses a write from an older closing
	// generation: after a takeover only the new generation moves the
	// book, and replays carry the current generation.
	ErrStaleGeneration = DomainError{
		Code:    CodeStaleGeneration,
		Message: "only the current closing generation moves the book: stale workers replays with the new generation or stand down",
	}
	// ErrLeaseHeld refuses a takeover while another worker holds the
	// lease: the book changes hands only past leased_until, never by
	// wall-clock guessing.
	ErrLeaseHeld = DomainError{
		Code:    CodeLeaseHeld,
		Message: "the book is already held: takeover waits for the lease to lapse on the database clock",
	}
)
