package application

import (
	"context"
	"fmt"
	"strings"

	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

// ListSessionsCommand identifies the account whose sessions are listed and the
// session asking, which is the entry marked as current.
type ListSessionsCommand struct {
	AccountID domain.AccountID
	// CurrentSessionID is the session of the caller. It is a fact the inbound
	// adapter already established, never a value the client states.
	CurrentSessionID domain.SessionID
}

// ListSessionsUseCase lists the account's own usable sessions (P16-T06).
//
// The use case answers one narrow question — which sessions may still act on
// behalf of this account, and which of them is the caller — and it answers it
// for the account that is authenticated, never for an identifier a caller
// supplies. There is no administrative variant: an operator with a role has a
// separate surface, and folding "list another account's sessions" into the
// owner's route would make one authorization mistake enough to read the
// sessions of every account.
type ListSessionsUseCase struct {
	sessions SessionRepository
	clock    Clock
	policy   domain.SessionPolicy
}

// NewListSessionsUseCase constructs a ListSessionsUseCase.
func NewListSessionsUseCase(sessions SessionRepository, clock Clock, policy domain.SessionPolicy) *ListSessionsUseCase {
	return &ListSessionsUseCase{sessions: sessions, clock: clock, policy: policy}
}

// Execute returns the account's usable sessions, most recently seen first.
func (uc *ListSessionsUseCase) Execute(ctx context.Context, cmd ListSessionsCommand) ([]SessionSummary, error) {
	if cmd.AccountID.IsZero() {
		return nil, domain.ErrEmptyAccountID
	}
	rows, err := uc.sessions.ListActiveSessions(ctx, cmd.AccountID, uc.window())
	if err != nil {
		return nil, fmt.Errorf("list active sessions: %w", err)
	}

	summaries := make([]SessionSummary, 0, len(rows))
	for _, session := range rows {
		summaries = append(summaries, SessionSummary{
			ID:         session.ID,
			CreatedAt:  session.CreatedAt,
			LastSeenAt: session.LastSeenAt,
			ExpiresAt:  session.ExpiresAt,
			IPAddress:  session.IPAddress,
			UserAgent:  sanitizeUserAgent(session.UserAgent),
			Current:    session.ID == cmd.CurrentSessionID,
		})
	}
	return summaries, nil
}

// window derives the policy boundaries of the instant the listing runs at. The
// boundaries travel to the statement, so the SQL asks the same question the
// evaluator answers instead of re-deriving one of its own.
func (uc *ListSessionsUseCase) window() SessionWindow {
	return SessionWindowFor(uc.clock.Now(), uc.policy, DefaultSessionListingRows)
}

// CleanupSessionsUseCase transitions the sessions that are past their policy
// deadline into the revoked state (P16-T06).
//
// It is the scheduled maintenance of the session store, and what it
// deliberately is not is a deletion: rows of terminal sessions are removed by
// the retention pass, under its own window and its holds, and a cleanup that
// deleted them would be a second, shorter retention policy nobody voted on.
// What this pass buys is that the refusal of a dead credential stops being only
// an arithmetic comparison: after the sweep the row is terminal, so a defect in
// the expiry evaluation cannot resurrect a session the policy already ended.
type CleanupSessionsUseCase struct {
	sessions SessionRepository
	clock    Clock
	policy   domain.SessionPolicy
}

// NewCleanupSessionsUseCase constructs a CleanupSessionsUseCase.
func NewCleanupSessionsUseCase(sessions SessionRepository, clock Clock, policy domain.SessionPolicy) *CleanupSessionsUseCase {
	return &CleanupSessionsUseCase{sessions: sessions, clock: clock, policy: policy}
}

// Execute revokes the sessions past their deadline and reports how many
// changed. The pass is idempotent: a second run at the same instant revokes
// nothing.
func (uc *CleanupSessionsUseCase) Execute(ctx context.Context) (int64, error) {
	revoked, err := uc.sessions.RevokeSessionsPastDeadline(ctx, SessionWindowFor(uc.clock.Now(), uc.policy, 0))
	if err != nil {
		return 0, fmt.Errorf("revoke sessions past deadline: %w", err)
	}
	return revoked, nil
}

// sanitizeUserAgent bounds the user agent a session list may carry.
//
// The value is whatever the client sent, so it is bounded before it leaves the
// server: a megabyte of header is not a device description, and control
// characters in it would forge lines in whatever reads it next. The bound is
// truncation, not rewriting — the list shows what the session recorded.
func sanitizeUserAgent(value string) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) > maxListedUserAgentBytes {
		return trimmed[:maxListedUserAgentBytes]
	}
	return trimmed
}

// maxListedUserAgentBytes bounds the user agent of one listed session.
const maxListedUserAgentBytes = 256
