package application

import (
	"context"
	"time"
)

// SeasonView is one allowlisted season: the book identity, the ordinal,
// the exact UTC window and the lifecycle state. Balances, holders,
// accounts and personal data never enter this shape: history is
// public allowlist, never a ledger extract.
type SeasonView struct {
	SeasonKey string
	Ordinal   int
	StartsAt  time.Time
	EndsAt    time.Time
	State     string
}

// SeasonReads serves the staged season lifecycle reads: the current
// ACTIVE book, one allowlisted book and the allowlisted history. Every
// method requires the authenticated account id: empty callers refuse
// before any row is read. Failures are seasons domain errors with
// stable codes (mismatch, closed, archived, unknown) that the inbound
// adapter maps to problem+json answers.
type SeasonReads interface {
	// GetCurrent resolves the single ACTIVE book for one account.
	GetCurrent(ctx context.Context, accountID string) (SeasonView, error)
	// GetSeason resolves one allowlisted book for one account.
	GetSeason(ctx context.Context, accountID, seasonKey string) (SeasonView, error)
	// ListHistory resolves the allowlisted history for one account
	// in ordinal order, never carrying the inactive namespace.
	ListHistory(ctx context.Context, accountID string) ([]SeasonView, error)
}
