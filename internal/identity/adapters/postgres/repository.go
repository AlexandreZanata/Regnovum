package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlexandreZanata/Regnovum/internal/identity/application"
	"github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// Repository implements identity application repositories using PostgreSQL.
type Repository struct {
	pool    *pgxpool.Pool
	queries *platformpg.Queries
}

var (
	_ application.AccountRepository            = (*Repository)(nil)
	_ application.PasswordCredentialRepository = (*Repository)(nil)
	_ application.VerificationTokenRepository  = (*Repository)(nil)
	_ application.PasswordResetTokenRepository = (*Repository)(nil)
	_ application.SessionRepository            = (*Repository)(nil)
)

// NewRepository creates a PostgreSQL repository adapter for identity.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{
		pool:    pool,
		queries: platformpg.New(pool),
	}
}

// CreateAccountWithPassword atomically creates an account in Pending status alongside its password credential.
func (r *Repository) CreateAccountWithPassword(ctx context.Context, email domain.Email, passwordHash string) (*domain.Account, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	qtx := r.queries.WithTx(tx)

	accRow, err := qtx.CreateAccount(ctx, platformpg.CreateAccountParams{
		Email:  email.String(),
		Status: string(domain.AccountStatusPending),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, application.ErrDuplicateEmail
		}
		return nil, fmt.Errorf("create account: %w", err)
	}

	err = qtx.CreatePasswordCredential(ctx, platformpg.CreatePasswordCredentialParams{
		AccountID:    accRow.ID,
		PasswordHash: passwordHash,
		Algorithm:    "argon2id",
		Version:      1,
	})
	if err != nil {
		return nil, fmt.Errorf("create password credential: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	return mapAccountRow(accRow)
}

// GetAccountByEmail looks up an account by its case-insensitive email address.
func (r *Repository) GetAccountByEmail(ctx context.Context, email domain.Email) (*domain.Account, error) {
	row, err := r.queries.GetAccountByEmail(ctx, email.String())
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrAccountNotFound
		}
		return nil, fmt.Errorf("get account by email: %w", err)
	}
	return mapAccountRow(row)
}

// GetAccountByID looks up an account by its unique identifier.
func (r *Repository) GetAccountByID(ctx context.Context, id domain.AccountID) (*domain.Account, error) {
	var pgUUID pgtype.UUID
	if err := pgUUID.Scan(id.String()); err != nil {
		return nil, fmt.Errorf("invalid account id format: %w", err)
	}

	row, err := r.queries.GetAccountByID(ctx, pgUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrAccountNotFound
		}
		return nil, fmt.Errorf("get account by id: %w", err)
	}
	return mapAccountRow(row)
}

// SetEmailVerified marks an account as Active and records the email verification timestamp.
func (r *Repository) SetEmailVerified(ctx context.Context, id domain.AccountID, verifiedAt time.Time) error {
	var pgUUID pgtype.UUID
	if err := pgUUID.Scan(id.String()); err != nil {
		return fmt.Errorf("invalid account id format: %w", err)
	}

	_, err := r.queries.SetEmailVerified(ctx, pgUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrAccountNotFound
		}
		return fmt.Errorf("set email verified: %w", err)
	}
	return nil
}

// CreateVerificationToken stores a new cryptographic token hash for an account.
func (r *Repository) CreateVerificationToken(ctx context.Context, accountID domain.AccountID, tokenHash []byte, expiresAt time.Time) error {
	var pgUUID pgtype.UUID
	if err := pgUUID.Scan(accountID.String()); err != nil {
		return fmt.Errorf("invalid account id format: %w", err)
	}

	_, err := r.queries.CreateEmailVerificationToken(ctx, platformpg.CreateEmailVerificationTokenParams{
		AccountID: pgUUID,
		TokenHash: tokenHash,
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("create verification token: %w", err)
	}
	return nil
}

// GetVerificationToken retrieves a verification token record by its binary hash.
func (r *Repository) GetVerificationToken(ctx context.Context, tokenHash []byte) (*application.VerificationTokenRecord, error) {
	row, err := r.queries.GetEmailVerificationTokenByHash(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrInvalidToken
		}
		return nil, fmt.Errorf("get verification token: %w", err)
	}

	var usedAt *time.Time
	if row.UsedAt.Valid {
		t := row.UsedAt.Time
		usedAt = &t
	}

	return &application.VerificationTokenRecord{
		ID:        uuidToString(row.ID),
		AccountID: domain.AccountID(uuidToString(row.AccountID)),
		TokenHash: row.TokenHash,
		ExpiresAt: row.ExpiresAt.Time,
		UsedAt:    usedAt,
		CreatedAt: row.CreatedAt.Time,
	}, nil
}

// MarkTokenUsed records that the token has been consumed, preventing replay.
func (r *Repository) MarkTokenUsed(ctx context.Context, tokenID string, usedAt time.Time) error {
	var pgUUID pgtype.UUID
	if err := pgUUID.Scan(tokenID); err != nil {
		return fmt.Errorf("invalid token id format: %w", err)
	}
	rows, err := r.queries.MarkEmailVerificationTokenUsed(ctx, pgUUID)
	if err != nil {
		return fmt.Errorf("mark token used: %w", err)
	}
	if rows == 0 {
		return application.ErrTokenAlreadyUsed
	}
	return nil
}

// InvalidateActiveTokens marks all existing unconsumed verification tokens for the account as used.
func (r *Repository) InvalidateActiveTokens(ctx context.Context, accountID domain.AccountID) error {
	var pgUUID pgtype.UUID
	if err := pgUUID.Scan(accountID.String()); err != nil {
		return fmt.Errorf("invalid account id format: %w", err)
	}
	if err := r.queries.InvalidateActiveEmailVerificationTokens(ctx, pgUUID); err != nil {
		return fmt.Errorf("invalidate active tokens: %w", err)
	}
	return nil
}

// CreatePasswordResetToken stores a new cryptographic reset token hash for an account.
func (r *Repository) CreatePasswordResetToken(ctx context.Context, accountID domain.AccountID, tokenHash []byte, expiresAt time.Time) error {
	var pgUUID pgtype.UUID
	if err := pgUUID.Scan(accountID.String()); err != nil {
		return fmt.Errorf("invalid account id format: %w", err)
	}

	_, err := r.queries.CreatePasswordResetToken(ctx, platformpg.CreatePasswordResetTokenParams{
		AccountID: pgUUID,
		TokenHash: tokenHash,
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("create password reset token: %w", err)
	}
	return nil
}

// GetPasswordResetToken retrieves a password reset token record by its binary hash.
func (r *Repository) GetPasswordResetToken(ctx context.Context, tokenHash []byte) (*application.PasswordResetTokenRecord, error) {
	row, err := r.queries.GetPasswordResetTokenByHash(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrInvalidToken
		}
		return nil, fmt.Errorf("get password reset token: %w", err)
	}

	var usedAt *time.Time
	if row.UsedAt.Valid {
		t := row.UsedAt.Time
		usedAt = &t
	}

	return &application.PasswordResetTokenRecord{
		ID:        uuidToString(row.ID),
		AccountID: domain.AccountID(uuidToString(row.AccountID)),
		TokenHash: row.TokenHash,
		ExpiresAt: row.ExpiresAt.Time,
		UsedAt:    usedAt,
		CreatedAt: row.CreatedAt.Time,
	}, nil
}

// MarkPasswordResetTokenUsed records that the token has been consumed, preventing replay.
func (r *Repository) MarkPasswordResetTokenUsed(ctx context.Context, tokenID string, usedAt time.Time) error {
	var pgUUID pgtype.UUID
	if err := pgUUID.Scan(tokenID); err != nil {
		return fmt.Errorf("invalid token id format: %w", err)
	}
	rows, err := r.queries.MarkPasswordResetTokenUsed(ctx, pgUUID)
	if err != nil {
		return fmt.Errorf("mark password reset token used: %w", err)
	}
	if rows == 0 {
		return application.ErrTokenAlreadyUsed
	}
	return nil
}

// InvalidateActivePasswordResetTokens marks all existing unconsumed reset tokens for the account as used.
func (r *Repository) InvalidateActivePasswordResetTokens(ctx context.Context, accountID domain.AccountID) error {
	var pgUUID pgtype.UUID
	if err := pgUUID.Scan(accountID.String()); err != nil {
		return fmt.Errorf("invalid account id format: %w", err)
	}
	if err := r.queries.InvalidateActivePasswordResetTokens(ctx, pgUUID); err != nil {
		return fmt.Errorf("invalidate active password reset tokens: %w", err)
	}
	return nil
}

// GetPasswordCredential retrieves the password credential record for an account.
func (r *Repository) GetPasswordCredential(ctx context.Context, accountID domain.AccountID) (*application.PasswordCredentialRecord, error) {
	var pgUUID pgtype.UUID
	if err := pgUUID.Scan(accountID.String()); err != nil {
		return nil, fmt.Errorf("invalid account id format: %w", err)
	}

	row, err := r.queries.GetPasswordCredentialByAccountID(ctx, pgUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrCredentialNotFound
		}
		return nil, fmt.Errorf("get password credential: %w", err)
	}

	return &application.PasswordCredentialRecord{
		AccountID:    domain.AccountID(uuidToString(row.AccountID)),
		PasswordHash: row.PasswordHash,
		Algorithm:    row.Algorithm,
		Version:      row.Version,
	}, nil
}

// UpdatePasswordCredential updates the stored hash (e.g. during transparent rehash).
func (r *Repository) UpdatePasswordCredential(ctx context.Context, accountID domain.AccountID, passwordHash string, algorithm string, version int32) error {
	var pgUUID pgtype.UUID
	if err := pgUUID.Scan(accountID.String()); err != nil {
		return fmt.Errorf("invalid account id format: %w", err)
	}

	err := r.queries.UpdatePasswordCredential(ctx, platformpg.UpdatePasswordCredentialParams{
		AccountID:    pgUUID,
		PasswordHash: passwordHash,
		Algorithm:    algorithm,
		Version:      version,
	})
	if err != nil {
		return fmt.Errorf("update password credential: %w", err)
	}
	return nil
}

// CreateSession stores a newly created session record and returns the reconstituted domain Session.
func (r *Repository) CreateSession(
	ctx context.Context,
	accountID domain.AccountID,
	tokenHash []byte,
	expiresAt time.Time,
	ipAddress, userAgent string,
) (*domain.Session, error) {
	var pgUUID pgtype.UUID
	if err := pgUUID.Scan(accountID.String()); err != nil {
		return nil, fmt.Errorf("invalid account id format: %w", err)
	}

	var ipText, uaText pgtype.Text
	if strings.TrimSpace(ipAddress) != "" {
		ipText = pgtype.Text{String: strings.TrimSpace(ipAddress), Valid: true}
	}
	if strings.TrimSpace(userAgent) != "" {
		uaText = pgtype.Text{String: strings.TrimSpace(userAgent), Valid: true}
	}

	row, err := r.queries.CreateSession(ctx, platformpg.CreateSessionParams{
		AccountID: pgUUID,
		TokenHash: tokenHash,
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
		IpAddress: ipText,
		UserAgent: uaText,
	})
	if err != nil {
		return nil, fmt.Errorf("create session in postgres: %w", err)
	}

	return mapSessionRow(row)
}

// GetSessionByTokenHash retrieves a session by its SHA-256 token hash (including expired or revoked).
func (r *Repository) GetSessionByTokenHash(ctx context.Context, tokenHash []byte) (*domain.Session, error) {
	row, err := r.queries.GetSessionByTokenHash(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrSessionNotFound
		}
		return nil, fmt.Errorf("get session by token hash: %w", err)
	}

	return mapSessionRow(row)
}

// TouchSession updates the last_seen_at and expires_at timestamps of an active session.
func (r *Repository) TouchSession(ctx context.Context, id domain.SessionID, lastSeenAt, expiresAt time.Time) error {
	var pgUUID pgtype.UUID
	if err := pgUUID.Scan(id.String()); err != nil {
		return fmt.Errorf("invalid session id format: %w", err)
	}

	err := r.queries.TouchSession(ctx, platformpg.TouchSessionParams{
		ID:         pgUUID,
		LastSeenAt: pgtype.Timestamptz{Time: lastSeenAt, Valid: true},
		ExpiresAt:  pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

// RevokeSession marks a single session as revoked by its token hash.
func (r *Repository) RevokeSession(ctx context.Context, tokenHash []byte) error {
	if err := r.queries.RevokeSession(ctx, tokenHash); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

// RevokeAllAccountSessions revokes all active sessions for a given account.
func (r *Repository) RevokeAllAccountSessions(ctx context.Context, accountID domain.AccountID) error {
	var pgUUID pgtype.UUID
	if err := pgUUID.Scan(accountID.String()); err != nil {
		return fmt.Errorf("invalid account id format: %w", err)
	}

	if err := r.queries.RevokeAllAccountSessions(ctx, pgUUID); err != nil {
		return fmt.Errorf("revoke all account sessions: %w", err)
	}
	return nil
}

// ListActiveSessions returns the account's usable sessions, most recently seen
// first (P16-T06).
//
// The statement does not select the token hash, so the listing cannot leak a
// credential even by accident: what is not read cannot be returned.
func (r *Repository) ListActiveSessions(ctx context.Context, accountID domain.AccountID, window application.SessionWindow) ([]application.SessionRecord, error) {
	var pgUUID pgtype.UUID
	if err := pgUUID.Scan(accountID.String()); err != nil {
		return nil, fmt.Errorf("invalid account id format: %w", err)
	}
	maxRows := window.MaxRows
	if maxRows <= 0 {
		maxRows = application.DefaultSessionListingRows
	}

	rows, err := r.queries.ListActiveAccountSessions(ctx, platformpg.ListActiveAccountSessionsParams{
		AccountID:  pgUUID,
		ExpiresAt:  pgtype.Timestamptz{Time: window.Now, Valid: true},
		LastSeenAt: pgtype.Timestamptz{Time: window.IdleCutoff, Valid: true},
		CreatedAt:  pgtype.Timestamptz{Time: window.AbsoluteCutoff, Valid: true},
		Limit:      int32(maxRows),
	})
	if err != nil {
		return nil, fmt.Errorf("list active account sessions: %w", err)
	}

	sessions := make([]application.SessionRecord, 0, len(rows))
	for _, row := range rows {
		sessions = append(sessions, application.SessionRecord{
			ID:         domain.SessionID(row.ID.String()),
			AccountID:  accountID,
			CreatedAt:  row.CreatedAt.Time.UTC(),
			LastSeenAt: row.LastSeenAt.Time.UTC(),
			ExpiresAt:  row.ExpiresAt.Time.UTC(),
			IPAddress:  row.IpAddress.String,
			UserAgent:  row.UserAgent.String,
		})
	}
	return sessions, nil
}

// RevokeSessionByID revokes one session of one account, reporting whether an
// active row changed (P16-T06).
func (r *Repository) RevokeSessionByID(ctx context.Context, accountID domain.AccountID, sessionID domain.SessionID) (bool, error) {
	var accountUUID, sessionUUID pgtype.UUID
	if err := accountUUID.Scan(accountID.String()); err != nil {
		return false, fmt.Errorf("invalid account id format: %w", err)
	}
	if err := sessionUUID.Scan(sessionID.String()); err != nil {
		return false, fmt.Errorf("invalid session id format: %w", err)
	}

	changed, err := r.queries.RevokeAccountSessionByID(ctx, platformpg.RevokeAccountSessionByIDParams{
		ID:        sessionUUID,
		AccountID: accountUUID,
	})
	if err != nil {
		return false, fmt.Errorf("revoke account session: %w", err)
	}
	return changed == 1, nil
}

// RevokeSessionsPastDeadline revokes every session past its policy deadline,
// reporting how many rows changed (P16-T06).
func (r *Repository) RevokeSessionsPastDeadline(ctx context.Context, window application.SessionWindow) (int64, error) {
	changed, err := r.queries.RevokeSessionsPastDeadline(ctx, platformpg.RevokeSessionsPastDeadlineParams{
		ExpiresAt:  pgtype.Timestamptz{Time: window.Now, Valid: true},
		LastSeenAt: pgtype.Timestamptz{Time: window.IdleCutoff, Valid: true},
		CreatedAt:  pgtype.Timestamptz{Time: window.AbsoluteCutoff, Valid: true},
	})
	if err != nil {
		return 0, fmt.Errorf("revoke sessions past deadline: %w", err)
	}
	return changed, nil
}

func mapAccountRow(row platformpg.AppAccount) (*domain.Account, error) {
	email, err := domain.ParseEmail(row.Email)
	if err != nil {
		return nil, fmt.Errorf("parse account email: %w", err)
	}

	var verifiedAt *time.Time
	if row.EmailVerifiedAt.Valid {
		t := row.EmailVerifiedAt.Time
		verifiedAt = &t
	}

	return domain.ReconstituteAccount(
		domain.AccountID(uuidToString(row.ID)),
		email,
		domain.AccountStatus(row.Status),
		verifiedAt,
		row.CreatedAt.Time,
		row.UpdatedAt.Time,
	)
}

func mapSessionRow(row platformpg.AppSession) (*domain.Session, error) {
	var revokedAt *time.Time
	if row.RevokedAt.Valid {
		t := row.RevokedAt.Time
		revokedAt = &t
	}

	ip := ""
	if row.IpAddress.Valid {
		ip = row.IpAddress.String
	}
	ua := ""
	if row.UserAgent.Valid {
		ua = row.UserAgent.String
	}

	return domain.ReconstituteSession(
		domain.SessionID(uuidToString(row.ID)),
		domain.AccountID(uuidToString(row.AccountID)),
		row.TokenHash,
		row.CreatedAt.Time,
		row.ExpiresAt.Time,
		row.LastSeenAt.Time,
		revokedAt,
		ip,
		ua,
	)
}

func uuidToString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7],
		b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}
