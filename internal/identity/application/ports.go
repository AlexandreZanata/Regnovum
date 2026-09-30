// Package application defines the use cases, orchestrations, and consumer-oriented
// ports for the identity and authentication module.
package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
)

// Clock exposes wall-clock time to identity application use cases.
type Clock interface {
	Now() time.Time
}

// Random exposes cryptographically secure random bytes to identity application use cases.
type Random interface {
	Read(buffer []byte) (int, error)
}

// PasswordHasher abstracts secure password hashing, constant-time verification,
// rehash detection for evolving security parameters, and dummy hashes for
// uniform login timing (mitigating user enumeration timing attacks per THR-AUTH-02).
type PasswordHasher interface {
	// HashPassword derives a secure Argon2id hash from a plain-text password using
	// a fresh cryptographically random salt and configured cost parameters.
	HashPassword(password string) (string, error)

	// VerifyPassword checks a plain-text password against an encoded Argon2id hash
	// using constant-time comparison to prevent timing side-channels.
	VerifyPassword(password, encodedHash string) (bool, error)

	// NeedsRehash reports whether an encoded hash was produced with parameters weaker
	// or different from the current hasher configuration, indicating the credential
	// should be re-hashed upon successful authentication.
	NeedsRehash(encodedHash string) bool

	// DummyHash returns a pre-computed, validly formatted Argon2id hash matching
	// current parameters to be used when an account does not exist, ensuring
	// identical CPU and memory consumption to prevent user enumeration (THR-AUTH-02).
	DummyHash() string
}

// AccountRepository defines persistent storage operations for accounts and credentials.
type AccountRepository interface {
	// CreateAccountWithPassword atomically creates an account in Pending status alongside its password credential.
	CreateAccountWithPassword(ctx context.Context, email domain.Email, passwordHash string) (*domain.Account, error)

	// GetAccountByEmail looks up an account by its case-insensitive email address.
	GetAccountByEmail(ctx context.Context, email domain.Email) (*domain.Account, error)

	// GetAccountByID looks up an account by its unique identifier.
	GetAccountByID(ctx context.Context, id domain.AccountID) (*domain.Account, error)

	// SetEmailVerified marks an account as Active and records the email verification timestamp.
	SetEmailVerified(ctx context.Context, id domain.AccountID, verifiedAt time.Time) error
}

// VerificationTokenRecord represents a stored single-use email verification token.
type VerificationTokenRecord struct {
	ID        string
	AccountID domain.AccountID
	TokenHash []byte
	ExpiresAt time.Time
	UsedAt    *time.Time
	CreatedAt time.Time
}

// VerificationTokenRepository manages single-use email verification tokens.
type VerificationTokenRepository interface {
	// CreateVerificationToken stores a new cryptographic token hash for an account.
	CreateVerificationToken(ctx context.Context, accountID domain.AccountID, tokenHash []byte, expiresAt time.Time) error

	// GetVerificationToken retrieves a verification token record by its binary hash.
	GetVerificationToken(ctx context.Context, tokenHash []byte) (*VerificationTokenRecord, error)

	// MarkTokenUsed records that the token has been consumed, preventing replay.
	MarkTokenUsed(ctx context.Context, tokenID string, usedAt time.Time) error

	// InvalidateActiveTokens marks all existing unconsumed verification tokens for the account as used.
	InvalidateActiveTokens(ctx context.Context, accountID domain.AccountID) error
}

// EmailSender delivers or enqueues transactional verification and password
// recovery emails.
type EmailSender interface {
	// SendVerificationEmail delivers or enqueues an email containing the unhashed verification token.
	SendVerificationEmail(ctx context.Context, email domain.Email, token string) error

	// SendPasswordResetEmail delivers or enqueues an email containing the unhashed password reset token.
	SendPasswordResetEmail(ctx context.Context, email domain.Email, token string) error

	// SendPasswordChangedEmail delivers or enqueues the notice that the
	// account's password changed (P16-T06). It carries no secret: the message
	// announces a transition, and its value is that the owner learns about a
	// change they did not make.
	//
	// ChangeID identifies the change itself — the consumed recovery token, for
	// instance. It is not a secret and it is not rendered: it is what makes two
	// different changes two messages, so the second change of an account is not
	// deduplicated against the first.
	SendPasswordChangedEmail(ctx context.Context, email domain.Email, changeID string) error
}

// PasswordResetTokenRecord represents a stored single-use password recovery token.
type PasswordResetTokenRecord struct {
	ID        string
	AccountID domain.AccountID
	TokenHash []byte
	ExpiresAt time.Time
	UsedAt    *time.Time
	CreatedAt time.Time
}

// PasswordResetTokenRepository manages single-use password recovery tokens.
type PasswordResetTokenRepository interface {
	// CreatePasswordResetToken stores a new cryptographic reset token hash for an account.
	CreatePasswordResetToken(ctx context.Context, accountID domain.AccountID, tokenHash []byte, expiresAt time.Time) error

	// GetPasswordResetToken retrieves a password reset token record by its binary hash.
	GetPasswordResetToken(ctx context.Context, tokenHash []byte) (*PasswordResetTokenRecord, error)

	// MarkPasswordResetTokenUsed records that the token has been consumed, preventing replay.
	MarkPasswordResetTokenUsed(ctx context.Context, tokenID string, usedAt time.Time) error

	// InvalidateActivePasswordResetTokens marks all existing unconsumed reset tokens for the account as used.
	InvalidateActivePasswordResetTokens(ctx context.Context, accountID domain.AccountID) error
}

// PasswordCredentialRecord represents a stored password credential.
type PasswordCredentialRecord struct {
	AccountID    domain.AccountID
	PasswordHash string
	Algorithm    string
	Version      int32
}

// PasswordCredentialRepository manages storage and updates of password credentials.
type PasswordCredentialRepository interface {
	// GetPasswordCredential retrieves the password credential record for an account.
	GetPasswordCredential(ctx context.Context, accountID domain.AccountID) (*PasswordCredentialRecord, error)

	// UpdatePasswordCredential updates the stored hash (e.g. during transparent rehash).
	UpdatePasswordCredential(ctx context.Context, accountID domain.AccountID, passwordHash string, algorithm string, version int32) error
}

// SessionWindow is the evaluation instant and the policy boundaries a session
// statement is scoped to (P16-T06).
//
// The boundaries are passed in instead of computed by the adapter because the
// session policy is a domain rule: `IdleCutoff` is the oldest `last_seen_at`
// that is still usable and `AbsoluteCutoff` the oldest `created_at`, so the
// statement asks exactly the question `SessionPolicy.IsExpired` answers. Two
// places deriving "still alive" from the policy would be two answers.
type SessionWindow struct {
	// Now is the instant the evaluation happens at.
	Now time.Time
	// IdleCutoff is the instant before which inactivity has expired a session.
	IdleCutoff time.Time
	// AbsoluteCutoff is the instant before which a session is past its
	// absolute lifetime regardless of activity.
	AbsoluteCutoff time.Time
	// MaxRows bounds the listing. A non-positive value selects
	// DefaultSessionListingRows.
	MaxRows int
}

// DefaultSessionListingRows bounds how many sessions one listing returns.
//
// The limit closes the response size at the storage boundary instead of
// trusting an account to hold a reasonable number of sessions: an account with
// a script that logs in repeatedly would otherwise turn one request into an
// unbounded response.
const DefaultSessionListingRows = 50

// SessionWindowFor derives the policy boundaries of one instant. It is the
// single place where the session policy becomes timestamps, so every statement
// that scopes rows by liveness answers the same question. A non-positive
// maxRows selects DefaultSessionListingRows.
func SessionWindowFor(now time.Time, policy domain.SessionPolicy, maxRows int) SessionWindow {
	if maxRows <= 0 {
		maxRows = DefaultSessionListingRows
	}
	return SessionWindow{
		Now:            now,
		IdleCutoff:     now.Add(-policy.IdleTimeout),
		AbsoluteCutoff: now.Add(-policy.AbsoluteLifetime),
		MaxRows:        maxRows,
	}
}

// SessionRecord is one stored session as storage reports it.
//
// It is deliberately not a domain.Session: a listing has no use for the token
// hash, and returning the entity would mean fabricating a credential field the
// statement never read. The absence is the point — the port cannot hand back a
// credential it did not load.
type SessionRecord struct {
	ID         domain.SessionID
	AccountID  domain.AccountID
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	IPAddress  string
	UserAgent  string
}

// SessionSummary is one entry of the account's own session list (P16-T06).
//
// It carries the facts the session itself recorded when it was established —
// when it was created, when it was last seen, when it dies, the address it
// came from and the user agent it presented — and nothing derived from them.
// There is deliberately no device identifier, no fingerprint and no
// cross-session correlation here: the list is what the server already stores,
// not a new way to recognize a person.
type SessionSummary struct {
	ID         domain.SessionID
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	IPAddress  string
	UserAgent  string
	// Current reports that this entry is the session making the request, so
	// the owner can see which one to keep before ending the others.
	Current bool
}

// SessionRepository manages persistence, retrieval, and revocation of opaque user sessions.
type SessionRepository interface {
	// CreateSession stores a newly created session record and returns the reconstituted domain Session.
	CreateSession(ctx context.Context, accountID domain.AccountID, tokenHash []byte, expiresAt time.Time, ipAddress, userAgent string) (*domain.Session, error)

	// GetSessionByTokenHash retrieves a session by its SHA-256 token hash (including expired or revoked).
	GetSessionByTokenHash(ctx context.Context, tokenHash []byte) (*domain.Session, error)

	// TouchSession updates the last_seen_at and expires_at timestamps of an active session.
	TouchSession(ctx context.Context, id domain.SessionID, lastSeenAt, expiresAt time.Time) error

	// RevokeSession marks a single session as revoked by its token hash.
	RevokeSession(ctx context.Context, tokenHash []byte) error

	// RevokeAllAccountSessions revokes all active sessions for a given account.
	RevokeAllAccountSessions(ctx context.Context, accountID domain.AccountID) error

	// ListActiveSessions returns the account's usable sessions, most recently
	// seen first, bounded by the window's MaxRows.
	ListActiveSessions(ctx context.Context, accountID domain.AccountID, window SessionWindow) ([]SessionRecord, error)

	// RevokeSessionByID revokes one session of one account, reporting whether
	// an active row changed. A session that does not exist, belongs to another
	// account or is already revoked reports false — the three cases are one
	// answer on purpose, so the endpoint cannot be asked whether a session
	// identifier exists.
	RevokeSessionByID(ctx context.Context, accountID domain.AccountID, sessionID domain.SessionID) (bool, error)

	// RevokeSessionsPastDeadline revokes every session past its policy
	// deadline, reporting how many rows changed. It never deletes: removal
	// belongs to the retention pass, with its own window and holds.
	RevokeSessionsPastDeadline(ctx context.Context, window SessionWindow) (int64, error)
}

// The rate limiting hook P04-T08 declared here was replaced in P16-T03 by
// internal/platform/ratelimit. The hook was a bool keyed on a string the
// adapter built itself, and the identity adapter built it from RemoteAddr and
// the first X-Forwarded-For entry — a value any client controls, so rotating
// the header produced unlimited distinct keys. The policy now lives in one
// platform table keyed by action, the mechanism is bounded in memory, and the
// transport facts come from internal/platform/clientip, which honors a
// forwarding header only when the peer is a configured trusted proxy.
